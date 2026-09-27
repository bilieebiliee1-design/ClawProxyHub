// port_test.go — 端口持久化回归（v1.3.0 方案 ①）：首启随机入库、复用同一端口、
// 被占用时重随机 + 端口变更通知、固定端口模式协同。
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGatewayPortPersistence 全生命周期：首启随机 → 复用 → 占用换口（通知）→ 复用新口。
func TestGatewayPortPersistence(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")

	// 1. 首启：随机端口并持久化
	p1, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if p1 <= 0 {
		t.Fatalf("first port = %d", p1)
	}
	if got := readPersistedPort(dir); got != p1 {
		t.Fatalf("persisted port = %d, want %d", got, p1)
	}
	if notice := PortChangeNotice(); notice != "" {
		t.Fatalf("first boot should have no port-change notice, got %q", notice)
	}
	if err := Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// 2. 重启：复用同一端口
	p2, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if p2 != p1 {
		t.Fatalf("port changed across restart: %d -> %d (should reuse)", p1, p2)
	}
	if err := Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// 3. 占用持久化端口 → 重新随机 + 明确的端口变更通知
	occupier, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p1))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	p3, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache})
	if err != nil {
		t.Fatalf("third Start: %v", err)
	}
	if p3 == p1 {
		t.Fatalf("port %d occupied but reused (rebind expected)", p1)
	}
	notice := PortChangeNotice()
	if !strings.Contains(notice, fmt.Sprintf("%d", p1)) || !strings.Contains(notice, fmt.Sprintf("%d", p3)) {
		t.Fatalf("port-change notice %q should mention old %d and new %d", notice, p1, p3)
	}
	// 变更同步落库：站内通知 + 运行日志（应用/面板双通道可见）
	code, body := httpCall(t, p3, http.MethodGet, "/health", "", nil)
	if code != 200 {
		t.Fatalf("new port /health = %d %s", code, body)
	}
	occupier.Close()
	if err := Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// 4. 释放后再启动：复用换口后的新端口（不再回落旧口）
	p4, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache})
	if err != nil {
		t.Fatalf("fourth Start: %v", err)
	}
	if p4 != p3 {
		t.Fatalf("port changed across restart after rebind: %d -> %d", p3, p4)
	}
	_ = Stop()
}

// TestGatewayPortFixedMode 固定端口协同：占用时明确报错（不静默换口）；
// 固定端口成功后落盘；切回自动模式从最近生效端口继续。
func TestGatewayPortFixedMode(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")

	// 占一个端口模拟被占用
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixed := occupier.Addr().(*net.TCPAddr).Port

	// 固定端口被占用 → Start 直接失败（固定端口语义：不静默回退）
	if _, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache, GatewayPort: fixed}); err == nil {
		t.Fatalf("Start with occupied fixed port %d should fail", fixed)
	}
	if Running() {
		_ = Stop()
		t.Fatalf("failed Start must not leave a running instance")
	}
	occupier.Close()

	// 固定端口可用 → 绑定成功并落盘
	p, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache, GatewayPort: fixed})
	if err != nil {
		t.Fatalf("Start fixed: %v", err)
	}
	if p != fixed {
		t.Fatalf("port = %d, want fixed %d", p, fixed)
	}
	if got := readPersistedPort(dir); got != fixed {
		t.Fatalf("persisted = %d, want %d", got, fixed)
	}
	if notice := PortChangeNotice(); notice != "" {
		t.Fatalf("fixed mode must not emit port-change notice, got %q", notice)
	}
	_ = Stop()

	// 切回自动模式：复用最近生效端口（固定 30011 → 自动仍 30011）
	p2, _, err := Start(nil, Options{DataDir: dir, CacheDir: cache})
	if err != nil {
		t.Fatalf("Start auto: %v", err)
	}
	if p2 != fixed {
		t.Fatalf("auto mode after fixed = %d, want reuse %d", p2, fixed)
	}
	_ = Stop()
}

// epCall 直连端点（http://host:port）发请求。
func epCall(t *testing.T, ep, method, path, token string, body []byte) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, ep+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s via %s: %v", method, path, ep, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestLanListener 局域网监听（方案 ④，默认开）：仅 /v1（强制密钥）+ /health；
// /admin 与面板拓扑不可达；开关热切换即时生效。
func TestLanListener(t *testing.T) {
	dir := t.TempDir()
	gwPort, _, err := Start(nil, Options{DataDir: dir, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = Stop() })

	ep := LanEndpoint()
	if ep != "" {
		// 端口与回环网关一致（同端口不同地址）
		if !strings.HasSuffix(ep, fmt.Sprintf(":%d", gwPort)) {
			t.Fatalf("lan endpoint %s should share gateway port %d", ep, gwPort)
		}
		if code, body := epCall(t, ep, http.MethodGet, "/health", "", nil); code != 200 || !strings.Contains(body, "ok") {
			t.Fatalf("lan /health = %d %s", code, body)
		}
		// 强制 API 密钥：无密钥 401
		if code, _ := epCall(t, ep, http.MethodGet, "/v1/models", "", nil); code != http.StatusUnauthorized {
			t.Fatalf("lan /v1/models without key = %d, want 401", code)
		}
		// /admin 与面板永不暴露（404 不暴露存在性）
		for _, p := range []string{"/admin/setup-status", "/admin/login", "/panel/", "/"} {
			if code, _ := epCall(t, ep, http.MethodGet, p, "", nil); code != http.StatusNotFound {
				t.Fatalf("lan %s = %d, want 404", p, code)
			}
		}
	}

	// 管理 API：建号 + 登录 → 关闭开关 → 热切换下线；再开启 → 恢复
	password := "Passw0rdLong!"
	if code, body := httpCall(t, gwPort, http.MethodPost, "/admin/setup", "", []byte(`{"username":"admin","password":"`+password+`"}`)); code != 200 {
		t.Fatalf("setup = %d %s", code, body)
	}
	code, body := httpCall(t, gwPort, http.MethodPost, "/admin/login", "", []byte(`{"username":"admin","password":"`+password+`"}`))
	if code != 200 {
		t.Fatalf("login = %d %s", code, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &lr); err != nil || lr.Token == "" {
		t.Fatalf("login resp: %s", body)
	}
	if code, body := httpCall(t, gwPort, http.MethodPut, "/admin/settings", lr.Token, []byte(`{"lan_enabled":false}`)); code != 200 {
		t.Fatalf("disable lan = %d %s", code, body)
	}
	if got := LanEndpoint(); got != "" {
		t.Fatalf("lan endpoint after disable = %q, want empty", got)
	}
	// system/info 的 lan_enabled 同步为 false
	code, body = httpCall(t, gwPort, http.MethodGet, "/admin/system/info", lr.Token, nil)
	if code != 200 || !strings.Contains(body, `"lan_enabled":false`) {
		t.Fatalf("system/info lan_enabled = %d %s", code, body)
	}
	// 重新开启（默认开）：恢复监听（无可用局域网地址时保持为空，跳过强断言）
	if code, _ := httpCall(t, gwPort, http.MethodPut, "/admin/settings", lr.Token, []byte(`{"lan_enabled":true}`)); code != 200 {
		t.Fatalf("enable lan failed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for LanEndpoint() == "" && time.Now().Before(deadline) && lanAddrAvailable() {
		time.Sleep(20 * time.Millisecond)
	}
	if LanEndpoint() != "" && lanAddrAvailable() {
		if code, _ := epCall(t, LanEndpoint(), http.MethodGet, "/admin/setup-status", "", nil); code != http.StatusNotFound {
			t.Fatalf("re-enabled lan /admin = %d, want 404", code)
		}
	}
}

// lanAddrAvailable 本机是否存在可绑定的局域网 IPv4（无则 LAN 相关强断言跳过）。
func lanAddrAvailable() bool {
	return lanListenIP() != ""
}

// TestTZOffsetApplied 应用时区偏移（方案 ⑤）：Start 后进程 Local 为指定偏移。
func TestTZOffsetApplied(t *testing.T) {
	old := time.Local
	t.Cleanup(func() { time.Local = old }) // 进程级全局，测试后还原

	dir := t.TempDir()
	if _, _, err := Start(nil, Options{DataDir: dir, CacheDir: filepath.Join(dir, "cache"), TZOffsetSeconds: 8 * 3600}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = Stop() })
	if _, off := time.Now().Zone(); off != 8*3600 {
		t.Fatalf("local zone offset = %d, want %d", off, 8*3600)
	}
	// 非法偏移拒绝启动（越界防护）
	_ = Stop()
	if _, _, err := Start(nil, Options{DataDir: dir, CacheDir: filepath.Join(dir, "cache"), TZOffsetSeconds: 100000}); err == nil {
		t.Fatalf("TZOffsetSeconds=100000 should be rejected")
	}
	if Running() {
		_ = Stop()
	}
}

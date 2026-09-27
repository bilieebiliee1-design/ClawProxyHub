// start_test.go — 核心启动端到端回归（方案 ②③，真实 db + 真实 HTTP）：
// 隧道落地页、面板暴露开关、/admin JWT 保护、run-logs 生命周期事件。
package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// httpCall 直连指定监听器发请求（gwPort=主监听器，tunPort=隧道目标监听器）。
func httpCall(t *testing.T, port int, method, path, token string, body []byte) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestStartTunnelLandingAndRunLogs 启动核心后：
//  1. 隧道 / 返回品牌落地页（非 404 —— v1.1.0 根因回归）；
//  2. /health 可用；
//  3. 面板暴露默认关：/admin/* 404（不暴露存在性）；
//  4. 经主监听器管理 API 开启 tunnel_expose_admin（默认关的新设置）：
//     隧道侧 /admin 登录 + JWT 逐请求保护，/panel 面板入口可用；
//  5. run-logs 落了核心启动事件（v1.1.0「运行日志页全程暂无日志」回归）。
func TestStartTunnelLandingAndRunLogs(t *testing.T) {
	dir := t.TempDir()
	gwPort, tunPort, err := Start(nil, Options{DataDir: dir, CacheDir: filepath.Join(dir, "cache")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = Stop() })

	// 1. 落地页（品牌 + 端点清单 + 用法）
	code, body := httpCall(t, tunPort, http.MethodGet, "/", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "服务在线") || !strings.Contains(body, "NexPort") {
		t.Fatalf("隧道根路径 = %d，应 200 且为品牌落地页: %.300s", code, body)
	}
	for _, ep := range []string{"/v1/models", "/v1/chat/completions", "/v1/messages", "/v1/responses", "/health"} {
		if !strings.Contains(body, ep) {
			t.Fatalf("落地页缺少端点 %s", ep)
		}
	}
	// 2. /health
	code, body = httpCall(t, tunPort, http.MethodGet, "/health", "", nil)
	if code != 200 || !strings.Contains(body, `"ok"`) {
		t.Fatalf("/health = %d %s", code, body)
	}
	// 3. 默认不暴露面板
	code, _ = httpCall(t, tunPort, http.MethodGet, "/admin/setup-status", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("默认 /admin/setup-status = %d, want 404（不暴露存在性）", code)
	}

	// 主监听器：首启建管理员 → 登录拿 JWT
	password := "Passw0rdLong!"
	code, body = httpCall(t, gwPort, http.MethodPost, "/admin/setup", "", []byte(`{"username":"admin","password":"`+password+`"}`))
	if code != 200 {
		t.Fatalf("setup = %d %s", code, body)
	}
	code, body = httpCall(t, gwPort, http.MethodPost, "/admin/login", "", []byte(`{"username":"admin","password":"`+password+`"}`))
	if code != 200 {
		t.Fatalf("login = %d %s", code, body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &lr); err != nil || lr.Token == "" {
		t.Fatalf("login 响应无 token: %s", body)
	}

	// 5. run-logs：核心启动事件已落库（默认 info 级别可见）
	code, body = httpCall(t, gwPort, http.MethodGet, "/admin/run-logs?module=core", lr.Token, nil)
	if code != 200 {
		t.Fatalf("run-logs = %d %s", code, body)
	}
	var rl struct {
		Logs []struct {
			Level  string `json:"level"`
			Module string `json:"module"`
			Action string `json:"action"`
		} `json:"logs"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &rl); err != nil {
		t.Fatalf("run-logs 解析: %v (%s)", err, body)
	}
	if rl.Total < 1 || len(rl.Logs) == 0 || rl.Logs[0].Action != "start" || rl.Logs[0].Module != "core" {
		t.Fatalf("run-logs 缺核心启动事件: total=%d logs=%v", rl.Total, rl.Logs)
	}

	// 4. 开启面板暴露（PUT 主监听器管理 API）→ 隧道侧 /admin 立即可达且受 JWT 保护
	code, body = httpCall(t, gwPort, http.MethodPut, "/admin/settings", lr.Token, []byte(`{"tunnel_expose_admin":true}`))
	if code != 200 {
		t.Fatalf("开启 expose_admin = %d %s", code, body)
	}
	code, _ = httpCall(t, tunPort, http.MethodGet, "/admin/me", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("隧道 /admin/me 无 token = %d, want 401", code)
	}
	code, body = httpCall(t, tunPort, http.MethodGet, "/admin/me", lr.Token, nil)
	if code != 200 || !strings.Contains(body, `"role"`) {
		t.Fatalf("隧道 /admin/me 带 token = %d %s", code, body)
	}
	// 面板入口（SPA fallback index）
	code, body = httpCall(t, tunPort, http.MethodGet, "/panel/login", "", nil)
	if code != 200 || !strings.Contains(body, `id="app"`) {
		t.Fatalf("隧道 /panel/login = %d，应为面板 index", code)
	}
	// 404 守卫仍生效（overlay 未注册路径落回落地页处理器 → 404）
	code, _ = httpCall(t, tunPort, http.MethodGet, "/secret-probe", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("隧道 /secret-probe = %d, want 404", code)
	}
}

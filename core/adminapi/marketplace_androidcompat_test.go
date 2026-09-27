// marketplace_androidcompat_test.go — 市场安卓适配与索引抓取性能回归（安卓化方案③ + 性能改造）。
// 注意文件名不能以 _android_test.go 结尾：会被 go 工具链按 GOOS 文件名约束规则
// （<name>_<GOOS>.go）判定为仅在 GOOS=android 下编译，桌面测试直接隐身。
package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"io.nexport.gateway/core/plugmgr"
	"time"
)

// TestBuildMarketViewUpdatable 安卓 Go 插件一律不出升级按钮；升级判定须严格版本更新
// （修复旧逻辑"版本不同即标升级"：内置 0.1.5 对市场 0.1.4 误报可升级）。
func TestBuildMarketViewUpdatable(t *testing.T) {
	entries := []MarketEntry{
		{Name: "gop", Version: "0.1.6", Author: "cph"},
		{Name: "lual", Version: "0.1.6", Author: "cph", Runtime: "lua"},
	}
	older := []MarketEntry{
		{Name: "gop", Version: "0.1.4", Author: "cph"},
		{Name: "lual", Version: "0.1.4", Author: "cph", Runtime: "lua"},
	}
	cases := []struct {
		name    string
		market  []MarketEntry
		local   string
		android bool
		wantGo  bool
		wantLua bool
	}{
		{"本地比市场新(内置0.1.5/市场0.1.4)", older, "0.1.5", false, false, false},
		{"同版本", entries, "0.1.6", false, false, false},
		{"市场更新-桌面", entries, "0.1.5", false, true, true},
		{"市场更新-安卓", entries, "0.1.5", true, false, true}, // 安卓 Go 随 APK 更新；Lua 仍可在线升级
	}
	for _, c := range cases {
		local := map[string]string{"cph/gop": c.local, "cph/lual": c.local}
		v := buildMarketView(c.market, local, c.android)
		if v[0].Updatable != c.wantGo {
			t.Fatalf("%s: go updatable = %v, want %v", c.name, v[0].Updatable, c.wantGo)
		}
		if v[1].Updatable != c.wantLua {
			t.Fatalf("%s: lua updatable = %v, want %v", c.name, v[1].Updatable, c.wantLua)
		}
	}
	// 版本补零语义：0.1 == 0.1.0 不误报
	v := buildMarketView(
		[]MarketEntry{{Name: "gop", Version: "0.1.0", Author: "cph"}},
		map[string]string{"cph/gop": "0.1"}, false)
	if v[0].Updatable {
		t.Fatalf("0.1.0 vs 0.1 不应标升级")
	}
}

// TestFetchIndexCachedTTL 短 TTL 缓存：TTL 内重复请求不回源（市场聚合 / 源列表 / 探测
// 三处共用，不再每次无条件回源）。过期后的行为由 SWR 两测覆盖。
func TestFetchIndexCachedTTL(t *testing.T) {
	var hits int64
	body := `[{"name":"p","version":"0.1.0","download_url":"https://example.com/p.cphplugin"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldTTL, oldFail := indexCacheTTL, indexFailTTL
	indexCacheTTL, indexFailTTL = 30*time.Millisecond, 5*time.Millisecond
	defer func() { indexCacheTTL, indexFailTTL = oldTTL, oldFail }()
	delete(indexCache, srv.URL)

	if _, err := fetchIndexCached(srv.URL); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := fetchIndexCached(srv.URL); err != nil {
			t.Fatalf("cached fetch: %v", err)
		}
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("TTL 内应命中缓存回源 1 次, got %d", n)
	}
}

// TestFetchIndexCachedSWR 过期 stale-while-revalidate：请求立即回陈旧值（不被回源
// 阻塞——面板冷路径回归的核心断言），后台刷新完成后请求拿到新值。
func TestFetchIndexCachedSWR(t *testing.T) {
	var hits int64
	v1 := `[{"name":"p","version":"0.1.0","download_url":"https://example.com/p.cphplugin"}]`
	v2 := `[{"name":"p","version":"0.2.0","download_url":"https://example.com/p.cphplugin"}]`
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&hits, 1) > 1 {
			<-release // 第 2+ 次回源挂起：若过期请求被同步阻塞将在此处暴露
		}
		if atomic.LoadInt64(&hits) == 1 {
			_, _ = w.Write([]byte(v1))
		} else {
			_, _ = w.Write([]byte(v2))
		}
	}))
	defer srv.Close()

	oldTTL, oldFail := indexCacheTTL, indexFailTTL
	indexCacheTTL, indexFailTTL = 30*time.Millisecond, 5*time.Millisecond
	defer func() { indexCacheTTL, indexFailTTL = oldTTL, oldFail }()
	delete(indexCache, srv.URL)

	if e, err := fetchIndexCached(srv.URL); err != nil || e[0].Version != "0.1.0" {
		t.Fatalf("first fetch: %v %v", e, err)
	}
	time.Sleep(40 * time.Millisecond) // 过期

	start := time.Now()
	e, err := fetchIndexCached(srv.URL)
	if err != nil {
		t.Fatalf("过期请求应回陈旧值不报错: %v", err)
	}
	if e[0].Version != "0.1.0" {
		t.Fatalf("过期请求应立即回陈旧值, got %s", e[0].Version)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("过期请求被同步回源阻塞 %v（SWR 失效）", d)
	}

	close(release) // 放行后台刷新（v2）
	deadline := time.Now().Add(2 * time.Second)
	for {
		e, err = fetchIndexCached(srv.URL)
		if err != nil {
			t.Fatalf("swr fetch: %v", err)
		}
		if e[0].Version == "0.2.0" {
			break // 后台刷新已落缓存
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台刷新未在 2s 内完成, 仍 %s", e[0].Version)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&hits); n != 2 {
		t.Fatalf("SWR 期间应恰好回源 2 次（初拉+后台刷新）, got %d", n)
	}
}

// TestFetchIndexCachedSWRFailureKeepsStale SWR 后台刷新失败：不得把错误写进缓存顶掉
// 陈旧成功值（后续请求仍回陈旧且不报错），并按 lastAttempt→fail TTL 限频重试。
func TestFetchIndexCachedSWRFailureKeepsStale(t *testing.T) {
	var hits int64
	v1 := `[{"name":"p","version":"0.1.0","download_url":"https://example.com/p.cphplugin"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n == 1 {
			_, _ = w.Write([]byte(v1))
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // 后续刷新一律失败
	}))
	defer srv.Close()

	oldTTL, oldFail := indexCacheTTL, indexFailTTL
	indexCacheTTL, indexFailTTL = 30*time.Millisecond, 30*time.Millisecond
	defer func() { indexCacheTTL, indexFailTTL = oldTTL, oldFail }()
	delete(indexCache, srv.URL)

	if e, err := fetchIndexCached(srv.URL); err != nil || e[0].Version != "0.1.0" {
		t.Fatalf("first fetch: %v %v", e, err)
	}
	time.Sleep(40 * time.Millisecond) // 过期 → 触发后台刷新（将失败）

	// 触发 SWR：过期请求立即回陈旧值，同时起后台刷新（该刷新将失败）
	if e, err := fetchIndexCached(srv.URL); err != nil || e[0].Version != "0.1.0" {
		t.Fatalf("过期请求应回陈旧值, got %v err=%v", e, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&hits) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&hits); n < 2 {
		t.Fatalf("后台刷新未发生, hits=%d", n)
	}
	// 刷新失败后：继续请求必须仍回陈旧成功值（不透传 500）
	if e, err := fetchIndexCached(srv.URL); err != nil || e[0].Version != "0.1.0" {
		t.Fatalf("刷新失败后应回陈旧成功值, got %v err=%v", e, err)
	}
	time.Sleep(35 * time.Millisecond) // 越过 lastAttempt 限频窗口 → 允许再试
	if e, err := fetchIndexCached(srv.URL); err != nil || e[0].Version != "0.1.0" {
		t.Fatalf("限频窗口后仍应回陈旧成功值, got %v err=%v", e, err)
	}
}

// TestFetchIndexCachedFailure 短失败缓存：上游 500 → 失败结果缓存（fail TTL 更短），
// 期间重复请求不回源且返回同一错误，过期后自动重试。
func TestFetchIndexCachedFailure(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	oldTTL, oldFail := indexCacheTTL, indexFailTTL
	indexCacheTTL, indexFailTTL = 30*time.Second, 5*time.Millisecond
	defer func() { indexCacheTTL, indexFailTTL = oldTTL, oldFail }()
	delete(indexCache, srv.URL)

	for i := 0; i < 3; i++ {
		if _, err := fetchIndexCached(srv.URL); err == nil {
			t.Fatalf("应透传错误")
		}
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Fatalf("失败响应应缓存, 回源 %d 次, want 1", n)
	}
	time.Sleep(10 * time.Millisecond)
	_, _ = fetchIndexCached(srv.URL)
	if n := atomic.LoadInt64(&hits); n != 2 {
		t.Fatalf("失败缓存过期后应回源, got %d", n)
	}
}

// TestFetchIndexConnectionTimeout 索引请求必须有连接级超时：对不可达地址在整体 15s
// 兜底内快速失败（此前整体 20s 且无 Dial/响应头超时，一个不可达源拖慢整个市场接口，
// 多源串行时成倍放大）。
func TestFetchIndexConnectionTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实网络等待")
	}
	// TEST-NET-1（RFC 5737）不可达地址：真实网络下 Dial 超时 5s 触发；
	// 若本机经代理出网（连接被代答）则由 ResponseHeaderTimeout 8s 兜底。
	// 两种路径都应在整体 15s 之内失败（旧实现要等满 20s）。
	start := time.Now()
	_, err := fetchIndex("http://192.0.2.1:9/index.json")
	if err == nil {
		t.Fatalf("不可达地址应失败")
	}
	if d := time.Since(start); d > 12*time.Second {
		t.Fatalf("分段超时应在 15s 兜底内快速失败, 耗时 %v", d)
	}
}

// TestBuildMarketViewAndroidReason 明确原因字段：非内置 Go 给不可安装原因；
// 内置 Go 且市场版本比内置新给不可在线升级原因；其余不带原因。
func TestBuildMarketViewAndroidReason(t *testing.T) {
	builtin := plugmgr.BuiltinPlugins()[len(plugmgr.BuiltinPlugins())-1] // 真实内置条目（如 zcode）
	entries := []MarketEntry{
		{Name: "notbuilt", Version: "0.1.0", Author: "cph"},             // 非内置 Go
		{Name: builtin.Name, Version: "9.9.9", Author: "cph"},           // 内置 Go，市场假想更版本
		{Name: "luap", Version: "0.2.0", Author: "cph", Runtime: "lua"}, // Lua：无原因
	}
	local := map[string]string{"cph/" + builtin.Name: builtin.Version}
	for _, v := range buildMarketView(entries, local, true) {
		switch v.Name {
		case "notbuilt":
			if v.AndroidSupported || v.AndroidReason == "" ||
				!strings.Contains(v.AndroidReason, "未随 APK 内置") {
				t.Fatalf("非内置 Go 应带不可安装原因: %+v", v)
			}
		case builtin.Name:
			if v.Updatable || v.AndroidReason == "" ||
				!strings.Contains(v.AndroidReason, "无法在线升级") {
				t.Fatalf("内置 Go 市场更新时应标不可在线升级原因: %+v", v)
			}
		case "luap":
			if !v.AndroidSupported || v.AndroidReason != "" {
				t.Fatalf("Lua 不应带原因: %+v", v)
			}
		}
	}
	// 内置 Go、市场版本不比内置新 → 无原因（无可升级语义）
	v := buildMarketView([]MarketEntry{{Name: builtin.Name, Version: "0.0.1", Author: "cph"}},
		map[string]string{"cph/" + builtin.Name: "9.9.9"}, true)[0]
	if v.AndroidReason != "" {
		t.Fatalf("市场版本更旧不应带原因: %+v", v)
	}
}

// tunnelpage_test.go — 隧道落地页与面板 overlay 回归（方案 ②）。
package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"io.nexport.gateway/core/version"
)

// stubSettings 测试用设置桩（绕开 sqlite 依赖）。
type stubSettings struct {
	name, abbr, logo string
	exposeAdmin      bool
}

func (s *stubSettings) SiteName() string        { return s.name }
func (s *stubSettings) SiteAbbr() string        { return s.abbr }
func (s *stubSettings) SiteLogo() string        { return s.logo }
func (s *stubSettings) TunnelExposeAdmin() bool { return s.exposeAdmin }

// TestTunnelLandingRoot 根路径必须是品牌化落地页：品牌、端点清单、用法；off 时无面板入口。
func TestTunnelLandingRoot(t *testing.T) {
	h := handleTunnelLanding(&stubSettings{name: "NexPort", abbr: "NX", exposeAdmin: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://demo.trycloudflare.com/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"NexPort", "服务在线", "/v1/models", "/v1/chat/completions", "/v1/messages", "/v1/responses",
		"/health", "Bearer", "https://demo.trycloudflare.com", version.String()} {
		if !strings.Contains(body, want) {
			t.Fatalf("落地页缺少 %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "/panel/") {
		t.Fatalf("expose off 时落地页不应有面板入口")
	}
	// X-Forwarded-Proto 优先（cloudflared 回源带 https）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:41000/", nil)
	req2.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "https://") || strings.Contains(rec2.Body.String(), "http://127.0.0.1") {
		t.Fatalf("X-Forwarded-Proto 应生效: %s", rec2.Body.String())
	}
}

// TestTunnelLandingGuard 非 "/" 路径 404；非 GET 405。
func TestTunnelLandingGuard(t *testing.T) {
	h := handleTunnelLanding(&stubSettings{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wp-login.php", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /wp-login.php = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / = %d, want 405", rec.Code)
	}
}

// TestTunnelMuxOverlay 面板暴露开关：off 时 /admin* 404；on 时 /admin、/panel、/assets、/favicon.ico 全部走 overlay。
// overlay 内部用真实 web.Handler（embed dist）：/panel/* 命中 SPA fallback（index.html），
// 判定依据是响应体为面板骨架而非落地页。
func TestTunnelMuxOverlay(t *testing.T) {
	gwCalled, adminCalled := "", ""
	gw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gwCalled = r.URL.Path; w.WriteHeader(200) })
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { adminCalled = r.URL.Path; w.WriteHeader(200) })

	off := newTunnelMux(gw, admin, &stubSettings{exposeAdmin: false})
	rec := httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("off: GET /admin/login = %d, want 404（不暴露存在性）", rec.Code)
	}
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if rec.Code != 200 || gwCalled != "/v1/chat/completions" {
		t.Fatalf("off: /v1 应直达网关 (code=%d path=%q)", rec.Code, gwCalled)
	}
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != 200 || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("off: /health = %d %q", rec.Code, rec.Body.String())
	}

	on := newTunnelMux(gw, admin, &stubSettings{exposeAdmin: true})
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/login", nil))
	if rec.Code != 200 || adminCalled != "/admin/login" {
		t.Fatalf("on: /admin/login 应走管理 API (code=%d path=%q)", rec.Code, adminCalled)
	}
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/login", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `id="app"`) || strings.Contains(body, "服务在线") {
		t.Fatalf("on: GET /panel/login 应返回面板 index（非落地页）: code=%d body=%.200s", rec.Code, body)
	}
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="app"`) {
		t.Fatalf("on: /assets/* 应走 web.Handler（SPA fallback）: code=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "服务在线") {
		t.Fatalf("on: /favicon.ico 应走静态资源: code=%d", rec.Code)
	}
}

// TestTunnelMuxLiveToggle 开关逐请求生效：同一 mux 实例，改设置立刻改变 /admin 可达性。
func TestTunnelMuxLiveToggle(t *testing.T) {
	gw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	st := &stubSettings{exposeAdmin: false}
	m := newTunnelMux(gw, admin, st)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("off: /admin/me = %d, want 404", rec.Code)
	}
	st.exposeAdmin = true
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/me", nil))
	if rec.Code != 200 {
		t.Fatalf("on: /admin/me = %d, want 200（无需重建 mux / 重启核心）", rec.Code)
	}
}

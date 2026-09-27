// tunnelpage.go — 隧道第二监听器的对外路由与品牌化落地页（NexPort fork 新增，方案 ②）。
//
// 修复 v1.1.0 遗留：tunMux 直接用 gw.Handler()，公网根路径 / 返回 "404 page not found"。
// 现在：
//   - "/" 恒为品牌化落地页（服务在线、可用端点清单、Bearer 用法与 curl 示例）；
//   - 新设置 tunnel.expose_admin（默认关）开启后，公网额外可达 /admin（管理 API，
//     服务端登录 + JWT 逐请求校验，与本地完全同权同限）与 /panel（面板 SPA 入口，
//     静态资源走 /assets）；关闭时这些路径无注册 → 404，不暴露存在性。
//
// 鉴权说明：adminapi 只认 Authorization: Bearer <JWT>（adminapi/auth.go parseBearer），
// 无 Cookie / 无 CORS 依赖，跨域隧道场景与本地同源等价安全；guest 角色只读约束不变。
//
// 设置即时生效：面板暴露开关逐请求读取（overlay 判定），改完设置无需重启核心。
package app

import (
	"html/template"
	"net/http"
	"strings"

	"io.nexport.gateway/core/version"
	"io.nexport.gateway/core/web"
)

// TunnelSettings 落地页与面板 overlay 所需的设置读取面（*setting.Store 实现）。
// 抽成接口便于无 DB 单测（sqlite 驱动依赖 CGO）。
type TunnelSettings interface {
	SiteName() string
	SiteAbbr() string
	SiteLogo() string
	TunnelExposeAdmin() bool
}

// endpointView 落地页端点行。
type endpointView struct {
	Method string
	Path   string
	Desc   string
}

// landingData 落地页模板数据（全部经 html/template 自动转义）。
type landingData struct {
	BrandName string
	BrandAbbr string
	BrandLogo string // data URL（空 = 用缩写徽标）
	Version   string
	Origin    string // 当前公网入口（scheme://host，示例 curl 用）
	ExposeOn  bool
	Endpoints []endpointView
}

// tunnelLandingTpl 落地页（内联 CSS，窄屏优先）。
var tunnelLandingTpl = template.Must(template.New("landing").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="robots" content="noindex,nofollow">
<title>{{.BrandName}} · 网关在线</title>
<style>
:root{color-scheme:light dark}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif;
background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:16px}
.card{width:100%;max-width:640px;background:#111b2e;border:1px solid #1e2c44;border-radius:16px;padding:28px 22px}
.brand{display:flex;align-items:center;gap:12px}
.logo{width:44px;height:44px;border-radius:10px;object-fit:contain}
.badge{width:44px;height:44px;border-radius:10px;background:#2563eb;color:#fff;font-weight:700;
display:flex;align-items:center;justify-content:center;font-size:18px}
h1{font-size:20px;font-weight:600}
.ver{font-size:12px;color:#7c8db0;margin-top:2px}
.status{display:inline-flex;align-items:center;gap:8px;margin:18px 0 4px;font-size:14px;color:#4ade80}
.dot{width:8px;height:8px;border-radius:50%;background:#4ade80;box-shadow:0 0 8px #4ade80}
.sub{font-size:13px;color:#7c8db0;margin-bottom:18px}
h2{font-size:13px;color:#9fb2d4;text-transform:uppercase;letter-spacing:.08em;margin:20px 0 8px}
table{width:100%;border-collapse:collapse;font-size:13px}
td{padding:7px 6px;border-top:1px solid #1e2c44;vertical-align:top}
td.m{white-space:nowrap;font-family:ui-monospace,Consolas,monospace;color:#93c5fd;width:1%}
td.p{white-space:nowrap;font-family:ui-monospace,Consolas,monospace}
td.d{color:#7c8db0}
.code{background:#0b1220;border:1px solid #1e2c44;border-radius:10px;padding:12px;overflow-x:auto;
font-family:ui-monospace,Consolas,monospace;font-size:12px;line-height:1.7;white-space:pre;color:#c9d6ee}
.note{font-size:13px;color:#7c8db0;margin-top:14px;line-height:1.7}
a.btn{display:inline-block;margin-top:14px;background:#2563eb;color:#fff;text-decoration:none;
padding:10px 18px;border-radius:10px;font-size:14px;font-weight:600}
.foot{margin-top:20px;font-size:12px;color:#55688c;line-height:1.8}
</style>
</head>
<body>
<div class="card">
  <div class="brand">
    {{if .BrandLogo}}<img class="logo" src="{{.BrandLogo}}" alt="logo">{{else}}<div class="badge">{{.BrandAbbr}}</div>{{end}}
    <div><h1>{{.BrandName}}</h1><div class="ver">{{.Version}}</div></div>
  </div>
  <div class="status"><span class="dot"></span>服务在线</div>
  <div class="sub">这是临时隧道入口，只暴露下方列出的网关端点。</div>

  <h2>可用端点</h2>
  <table>
    <tr><td class="m">GET</td><td class="p">/health</td><td class="d">存活检查（免鉴权）</td></tr>
    <tr><td class="m">GET</td><td class="p">/v1/models</td><td class="d">模型列表</td></tr>
    <tr><td class="m">POST</td><td class="p">/v1/chat/completions</td><td class="d">OpenAI Chat 兼容</td></tr>
    <tr><td class="m">POST</td><td class="p">/v1/messages</td><td class="d">Anthropic Messages 兼容</td></tr>
    <tr><td class="m">POST</td><td class="p">/v1/responses</td><td class="d">OpenAI Responses 兼容</td></tr>
  </table>

  <h2>用法</h2>
  <div class="code">Base URL: {{.Origin}}
鉴权: Authorization: Bearer &lt;在面板「API 密钥」签发的 Key&gt;

curl {{.Origin}}/v1/chat/completions \
  -H "Authorization: Bearer $NEXPORT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"&lt;模型名&gt;","messages":[{"role":"user","content":"hi"}]}'</div>
  <div class="note">Anthropic / Codex 客户端把 Base URL 设为 <b>{{.Origin}}</b> 即可；模型名以 /v1/models 返回为准。</div>

  {{if .ExposeOn}}<a class="btn" href="/panel/">打开管理面板（登录后可用）</a>
  <div class="note">面板与管理 API 经隧道暴露，仍受登录 + JWT 保护；可在 设置 → 隧道 关闭。</div>
  {{else}}<div class="note">管理面板未通过隧道暴露（可在 设置 → 隧道 开启「通过隧道暴露面板」）。</div>{{end}}

  <div class="foot">{{.BrandName}} · NexPort 网关 —— 本页由核心生成，仅表示服务在线。</div>
</div>
</body>
</html>
`))

// endpoints 落地页展示的端点（与 gw.Handler() 注册的路由一一对应）。
var tunnelEndpoints = []endpointView{
	{"GET", "/health", "存活检查（免鉴权）"},
	{"GET", "/v1/models", "模型列表"},
	{"POST", "/v1/chat/completions", "OpenAI Chat 兼容"},
	{"POST", "/v1/messages", "Anthropic Messages 兼容"},
	{"POST", "/v1/responses", "OpenAI Responses 兼容"},
}

// tunnelScheme 推断对外 scheme：cloudflared 回源带 X-Forwarded-Proto；本地测试按 TLS/明文回退。
func tunnelScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// handleTunnelLanding 根路径落地页；其余未注册路径一律 404（不泄露拓扑）。
func handleTunnelLanding(settings TunnelSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data := landingData{
			BrandName: settings.SiteName(),
			BrandAbbr: settings.SiteAbbr(),
			BrandLogo: settings.SiteLogo(),
			Version:   version.String(),
			Origin:    tunnelScheme(r) + "://" + r.Host,
			ExposeOn:  settings.TunnelExposeAdmin(),
			Endpoints: tunnelEndpoints,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = tunnelLandingTpl.Execute(w, data)
	}
}

// newTunnelMux 隧道第二监听器总路由。
//   - 固定暴露：/v1/*（网关）、/health、/（落地页）；
//   - expose_admin 开（逐请求判定，改设置即生效）：/admin/*（管理 API）、
//     /panel/（面板 SPA）、/assets/* 与 /favicon.ico（SPA 静态资源）；
//     关时这些路径落回 "/" 处理器 → 404。
func newTunnelMux(gw http.Handler, admin http.Handler, settings TunnelSettings) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", gw)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", handleTunnelLanding(settings))

	// 面板 overlay：默认关；开启后 /admin、/panel、/assets、/favicon.ico 走这里
	panel := http.NewServeMux()
	panel.Handle("/admin/", admin)
	panel.Handle("/assets/", web.Handler())
	panel.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/favicon.ico"
		web.Handler().ServeHTTP(w, r)
	})
	panel.Handle("/panel/", http.StripPrefix("/panel", web.Handler()))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if settings.TunnelExposeAdmin() &&
			(strings.HasPrefix(p, "/admin/") || strings.HasPrefix(p, "/assets/") ||
				strings.HasPrefix(p, "/panel/") || p == "/panel" || p == "/favicon.ico") {
			panel.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

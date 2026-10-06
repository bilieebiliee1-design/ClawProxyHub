// credential_test.go — 凭据外发回归（NexPort v1.5.0，PR #1 三偏离）。
// doubao 曾为全集中唯一确诊的 never-sent 插件：凭据被解析入库但 upstream.go 从不外发，
// 出站请求无 Cookie 头 → 服务端视为未登录恒 401（实测抓包证实）。修复三件套：
// ①upstream.go cookieHeader() 全量外发 Cookie 头；②main.go loginCookieHeader 的
// sessionid/sessionid_ss 建档预检（缺失 400 拒档）；③finalizeLogin msToken 三级兜底。
// 本文件逐项回归：
//   1) TestCookieHeader            —— Cookie 头键序拼装（sort.Strings 固定序，可复现）；
//   2) TestLoginCookieHeader       —— 建档预检拒档与放行两路径；
//   3) TestChatCompletionSendsCredentialCookie —— httptest 证明凭据 Cookie 头真实出现在
//      上游请求中（出站请求经真实 http.Client 栈原样送达本地 httptest 服务端后断言，
//      传输层仅改写目标地址为测试服务、不触碰任何请求头）。
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pb "github.com/ShadowSmallBaby/ClawProxyHub/sdk/proto/cphv1"
	"github.com/ShadowSmallBaby/ClawProxyHub/sdk"
)

// roundTripFunc 传输层桩：把函数伪装成 http.RoundTripper。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sdkHostStub 空 Host：未连接宿主时 Settings 静默降级为 nil（读设置回退默认值），
// 单测免起 gRPC 插件进程。
func sdkHostStub() *sdk.Host { return &sdk.Host{} }

// TestCookieHeader cookieHeader 键序固定拼装：sort.Strings 序 + "k=v" + "; " 连接。
// 键序可复现是对外行为（排障抓包可比对）；凭据 Map 迭代序随机，若实现回归为
// Map 直拼，本断言会随机失败。
func TestCookieHeader(t *testing.T) {
	c := &credential{Cookies: map[string]string{
		"ttwid":                "t1",
		"sessionid":            "s1",
		"msToken":              "m1",
		"passport_csrf_token":  "c1",
		"passport_csrf_token_default": "cd",
	}}
	got := cookieHeader(c)
	want := "msToken=m1; passport_csrf_token=c1; passport_csrf_token_default=cd; sessionid=s1; ttwid=t1"
	if got != want {
		t.Fatalf("cookieHeader = %q, want %q", got, want)
	}
	// 空凭据 → 空头（调用侧语义：无 Cookie 可发）
	if got := cookieHeader(&credential{}); got != "" {
		t.Fatalf("empty cookies header = %q, want empty", got)
	}
	// 值原样透传（不做二次 URL 编码：浏览器复制的头已编码）
	c2 := &credential{Cookies: map[string]string{"b": "2", "a": "1"}}
	if got := cookieHeader(c2); got != "a=1; b=2" {
		t.Fatalf("simple header = %q", got)
	}
}

// TestLoginCookieHeader 建档预检（401 修复②）：
//   - 解析失败 → 400；
//   - 缺 sessionid/sessionid_ss（未登录匿名 Cookie，如 ttwid）→ 400 拒档（防坏档静默入库）；
//   - sessionid 在 → 预检放行，Blob 内含全部 Cookies。
func TestLoginCookieHeader(t *testing.T) {
	p := &plugin{host: sdkHostStub()}

	res, err := p.loginCookieHeader(&pb.LoginRequest{Form: map[string]string{"cookie": ""}})
	if err != nil {
		t.Fatalf("loginCookieHeader: %v", err)
	}
	if res.GetError() == nil || res.GetError().Code != 400 {
		t.Fatalf("空 Cookie 应 400 拒档, got %+v", res)
	}

	res, err = p.loginCookieHeader(&pb.LoginRequest{Form: map[string]string{
		"cookie": "ttwid=t1; msToken=m1; passport_csrf_token=c1"}})
	if err != nil {
		t.Fatalf("loginCookieHeader: %v", err)
	}
	if res.GetError() == nil || res.GetError().Code != 400 {
		t.Fatalf("缺 sessionid 的匿名 Cookie 应 400 拒档, got %+v", res)
	}
	if !strings.Contains(res.GetError().Message, "sessionid") {
		t.Fatalf("拒档文案应指明 sessionid, got %q", res.GetError().Message)
	}

	res, err = p.loginCookieHeader(&pb.LoginRequest{Form: map[string]string{
		"cookie": "sessionid=s1; ttwid=t1; passport_csrf_token=c1"}})
	if err != nil {
		t.Fatalf("loginCookieHeader: %v", err)
	}
	if res.GetError() != nil || len(res.Blob) == 0 {
		t.Fatalf("sessionid 在场应放行建档, got %+v", res)
	}
	var c credential
	if err := json.Unmarshal(res.Blob, &c); err != nil {
		t.Fatalf("blob: %v", err)
	}
	if c.Cookies["sessionid"] != "s1" || c.Cookies["ttwid"] != "t1" {
		t.Fatalf("Blob Cookies 丢失: %+v", c.Cookies)
	}
}

// TestChatCompletionSendsCredentialCookie never-sent 核心回归（401 修复①）：
// chatCompletion 出站请求必须携带凭据 Cookies 全量 Cookie 头与 csrf 头。
// 通过预置 proxyClients 的传输层把发往 doubao 上游的请求原样改道本地 httptest，
// 服务端断言收到的头——请求头由被测代码设置，测试不改头，只改目标地址。
func TestChatCompletionSendsCredentialCookie(t *testing.T) {
	var gotCookie, gotCSRF string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotCSRF = r.Header.Get("x-tt-passport-csrf-token")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"ok\":true}\n\n")
	}))
	defer srv.Close()

	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		out := req.Clone(req.Context())
		u, err := url.Parse(srv.URL)
		if err != nil {
			return nil, err
		}
		out.URL.Scheme, out.URL.Host = u.Scheme, u.Host
		return srv.Client().Transport.RoundTrip(out)
	})
	proxyClients.Store("", &http.Client{Transport: rt})
	defer proxyClients.Delete("")

	cred := &credential{Cookies: map[string]string{
		"sessionid":           "s1",
		"ttwid":               "t1",
		"passport_csrf_token": "csrf1",
	}}
	p := &plugin{host: sdkHostStub()}
	req := &pb.ChatRequest{Messages: []*pb.EnvelopeMessage{{Role: "user", Text: "hi"}}}
	err := p.chatCompletion(context.Background(), cred, req, "doubao-seed-1.5", func(name string, data []byte) error {
		return nil
	})
	if err != nil {
		t.Fatalf("chatCompletion: %v", err)
	}
	want := "passport_csrf_token=csrf1; sessionid=s1; ttwid=t1"
	if gotCookie != want {
		t.Fatalf("上游请求 Cookie 头 = %q, want %q（never-sent 回归：凭据必须随请求外发）", gotCookie, want)
	}
	if gotCSRF != "csrf1" {
		t.Fatalf("上游请求 csrf 头 = %q, want %q", gotCSRF, "csrf1")
	}
}

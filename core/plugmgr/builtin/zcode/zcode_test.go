package zcode

// zcode httptest 单测组（借 doubao upstream.go v1.4.11 先例：httptest.Server +
// 前置校验 + 断言）。覆盖修复方案验收门第 1 层：
//   ① upstreamBase bigmodel 端点（P0-1）
//   ② applyStartPlanBody 恒 3 块、客户端文本入第三块（P0-2）
//   ③ V4 签名键统一 + fail-open 保持（P0-3 案 A）
//   ④ 静态表换代 + 动态目录解析（P0-4）
//   ⑥ metadata.user_id / X-Device-Mid / x-session-id 会话稳定 / 3.16.3（P1-5/6/7/8）
//   上游错误语义映射（P1-9）
//   登录全链 e2e（init→poll→z/login→customerInfo→api_keys→copy→目录校验，全 fake 上游）
// P1-10（authorize_url redirect 重写）未采纳（诊断实测服务器自含服务端回调），
// 故无对应断言——见 tools/build-plugins.sh 头注 4) 未采纳项。

import (
	"context"
	"crypto/aes"
	aescipher "crypto/cipher"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ShadowSmallBaby/ClawProxyHub/sdk"
	pb "github.com/ShadowSmallBaby/ClawProxyHub/sdk/proto/cphv1"
	"google.golang.org/grpc/metadata"
)

// ---------- 工具 ----------

// newTestPlugin 空宿主插件（sdk.Host 零值：Settings 静默返回 nil → 走内置默认）。
func newTestPlugin() *plugin {
	return &plugin{version: "test", host: &sdk.Host{}}
}

// fakeChatStream pb.ClawPlugin_ChatServer 内存实现（收集事件供断言）。
type fakeChatStream struct {
	events []*pb.StreamEvent
}

func (s *fakeChatStream) Send(ev *pb.StreamEvent) error { s.events = append(s.events, ev); return nil }
func (s *fakeChatStream) Context() context.Context      { return context.Background() }
func (s *fakeChatStream) SetHeader(metadata.MD) error   { return nil }
func (s *fakeChatStream) SendHeader(metadata.MD) error  { return nil }
func (s *fakeChatStream) SetTrailer(metadata.MD)        {}
func (s *fakeChatStream) SendMsg(interface{}) error     { return nil }
func (s *fakeChatStream) RecvMsg(interface{}) error     { return nil }

// sseUpstream 恒定 Anthropic SSE 响应的最小上游。
func sseUpstream(t *testing.T, capture *http.Header, captureBody *map[string]interface{}, status int, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture = r.Header.Clone()
		}
		if captureBody != nil {
			raw, _ := io.ReadAll(r.Body)
			m := map[string]interface{}{}
			_ = json.Unmarshal(raw, &m)
			*captureBody = m
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, payload)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, payload)
	}))
}

const sseOK = "data: {\"type\":\"message_start\",\"message\":{\"model\":\"GLM-5.3\"}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"

// testRulesJSON zcode-builtin 规则叠加层夹具（schema 同 rev 23 实测形态）。
const testRulesJSON = `{
  "schemaVersion": 1,
  "revision": 1,
  "config": {
    "providerConfigRules": {
      "providerRules": [
        {"providerId": "account:zai-individual-coding-plan", "builtinModelIds": ["GLM-5.3"]}
      ]
    },
    "modelConfigRules": {
      "modelRules": [
        {"modelMatch": ".*", "config": {"properties": {"contextWindow": 200000, "inputFormat": {"supportsImage": false}}, "optionSpecs": {"maxOutputTokens": {"max": 32000}, "reasoningLevel": {"values": ["disabled"]}}}},
        {"modelMatch": ".*glm-5\\.3(?:-flash)?(?:[.\\-:/\\[].*)?", "config": {"properties": {"contextWindow": 1000000, "inputFormat": {"supportsImage": false}}, "optionSpecs": {"maxOutputTokens": {"max": 128000}, "reasoningLevel": {"values": ["low", "high", "max"]}}}},
        {"modelMatch": ".*glm-5\\.3-flash(?:[.\\-:/\\[].*)?", "config": {"properties": {"inputFormat": {"supportsImage": true}}}}
      ],
      "builtinProviderModelRules": [
        {"providerId": "account:zai-individual-coding-plan", "modelId": "GLM-5.3", "config": {"enabled": true}},
        {"providerId": "account:zai-individual-coding-plan", "modelId": "GLM-5.3-Flash", "config": {"enabled": true}},
        {"providerId": "account:zai-individual-coding-plan", "modelId": "GLM-5.9-Ghost", "config": {"enabled": false}},
        {"providerId": "account:zai-start-plan", "modelId": "GLM-5.3-Flash", "config": {"enabled": true}},
        {"providerId": "account:zai-start-plan", "modelId": "GLM-5.3", "config": {"enabled": false}}
      ]
    }
  }
}`

// ---------- P0-1 端点 ----------

func TestUpstreamBaseBigmodel(t *testing.T) {
	if got := upstreamBase("bigmodel"); got != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("upstreamBase(bigmodel) = %q, want https://open.bigmodel.cn/api/anthropic（P0-1：上游两分支同值错指 api.z.ai）", got)
	}
	if got := upstreamBase("zai"); got != "https://api.z.ai/api/anthropic" {
		t.Fatalf("upstreamBase(zai) = %q, want https://api.z.ai/api/anthropic", got)
	}
	p := newTestPlugin()
	ep, _ := p.buildUpstream(context.Background(), &credential{Plan: "coding-plan", Provider: "bigmodel", APIKey: "k"}, &pb.ChatRequest{}, nil, "UA")
	if ep != "https://open.bigmodel.cn/api/anthropic/v1/messages" {
		t.Fatalf("bigmodel endpoint = %q", ep)
	}
}

// ---------- P1-8 buildUpstream 头集合 ----------

func TestBuildUpstreamHeaders(t *testing.T) {
	p := newTestPlugin()
	// start-plan：X-Device-Mid + x-zcode-query-source（P1-7/P1-8）
	_, h := p.buildUpstream(context.Background(),
		&credential{Plan: "start-plan", Provider: "zai", JWT: "j", DeviceMid: "dev-1"}, &pb.ChatRequest{}, nil, "UA")
	if h["Authorization"] != "Bearer j" || h["anthropic-version"] != anthropicVersion {
		t.Fatal("start-plan auth 头缺失")
	}
	if h["X-Device-Mid"] != "dev-1" {
		t.Fatalf("start-plan 缺 X-Device-Mid（P1-7，magpie #282）: %v", h)
	}
	if h["x-zcode-query-source"] != "main_turn" {
		t.Fatalf("start-plan 缺 x-zcode-query-source（P1-8，3.16 attribution 头）: %v", h)
	}
	// 旧凭据无 DeviceMid：省略而不发空值
	_, h2 := p.buildUpstream(context.Background(),
		&credential{Plan: "start-plan", Provider: "zai", JWT: "j"}, &pb.ChatRequest{}, nil, "UA")
	if _, ok := h2["X-Device-Mid"]; ok {
		t.Fatal("无 device_mid 不应携带空 X-Device-Mid")
	}
	// coding-plan：双 auth 头 + 业务 API 绝不带 X-Device-Mid（#282）
	_, h3 := p.buildUpstream(context.Background(),
		&credential{Plan: "coding-plan", Provider: "zai", APIKey: "id.secret", DeviceMid: "dev-2"}, &pb.ChatRequest{}, nil, "UA")
	if h3["x-api-key"] != "id.secret" || h3["Authorization"] != "Bearer id.secret" {
		t.Fatalf("coding-plan 双 auth 头异常: %v", h3)
	}
	if _, ok := h3["X-Device-Mid"]; ok {
		t.Fatal("业务 API（api.z.ai）不应携带 X-Device-Mid（magpie #282 规则）")
	}
	if _, ok := h3["x-zcode-query-source"]; ok {
		t.Fatal("coding-plan 不加 x-zcode-query-source（magpie 仅 startHeaders 携带）")
	}
}

// ---------- P0-2 start-plan 恒 3 块 ----------

func TestApplyStartPlanBodyThreeBlocks(t *testing.T) {
	p := newTestPlugin()
	for _, tc := range []struct {
		name   string
		system interface{}
	}{
		{"string 形态", "你是一个代码助手"},
		{"块数组形态", []interface{}{
			map[string]interface{}{"type": "text", "text": "块一"},
			map[string]interface{}{"type": "text", "text": "块二", "cache_control": map[string]string{"type": "ephemeral"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]interface{}{
				"model":    "GLM-5.3",
				"system":   tc.system,
				"messages": []interface{}{map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}}}},
				"tools":    []interface{}{map[string]interface{}{"name": "bash", "cache_control": map[string]string{"type": "ephemeral"}}},
			}
			cred := &credential{Plan: "start-plan", Provider: "zai", JWT: "j", DeviceMid: "dev-1"}
			if err := p.applyStartPlanBody(body, cred, &pb.ChatRequest{Model: "GLM-5.3"}, "sess-1"); err != nil {
				t.Fatal(err)
			}
			sys, ok := body["system"].([]map[string]interface{})
			if !ok {
				t.Fatalf("system 类型异常: %T", body["system"])
			}
			if len(sys) != 3 {
				t.Fatalf("system 块数 = %d, want 恒 3（P0-2：第四块被 zcode.z.ai 405/3012 拒载）", len(sys))
			}
			third, _ := sys[2]["text"].(string)
			if !strings.Contains(third, "你是一个代码助手") && !strings.Contains(third, "块一") {
				t.Fatalf("客户端 system 文本未并入第三块（P0-2）: %q", third)
			}
			if !strings.HasPrefix(third, "\n\n") {
				t.Fatalf("第三块失去上游 \\n\\n 前缀形态: %q", third)
			}
			for i, b := range sys {
				if _, ok := b["cache_control"].(map[string]string); !ok {
					t.Fatalf("block %d 缺 ephemeral cache_control", i)
				}
			}
			// 前缀 user 消息（currentDate <system-reminder>）
			msgs := body["messages"].([]interface{})
			if len(msgs) != 2 {
				t.Fatalf("messages = %d, want 2（前缀 + 原文）", len(msgs))
			}
			prefix := msgs[0].(map[string]interface{})
			if prefix["role"] != "user" {
				t.Fatal("前缀消息角色应为 user")
			}
			// tools cache_control 剥离
			tool := body["tools"].([]interface{})[0].(map[string]interface{})
			if _, ok := tool["cache_control"]; ok {
				t.Fatal("tools cache_control 未剥离")
			}
			// metadata.user_id（P1-6，dress 覆写语义）
			uid := assertUserID(t, body)
			if uid.DeviceID != "dev-1" || uid.AccountUUID != "" || uid.SessionID != "sess-1" {
				t.Fatalf("metadata.user_id = %+v, want {dev-1, '', sess-1}", uid)
			}
		})
	}
}

type userIDShape struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

func assertUserID(t *testing.T, body map[string]interface{}) userIDShape {
	t.Helper()
	meta, ok := body["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("缺 metadata: %v", body)
	}
	uid, _ := meta["user_id"].(string)
	var parsed userIDShape
	if json.Unmarshal([]byte(uid), &parsed) != nil {
		t.Fatalf("metadata.user_id 非 JSON 对象形态（magpie #387）: %q", uid)
	}
	return parsed
}

// TestApplyStartPlanBodyChatBodyMessagesShape ChatBody 的 messages 实际为
// []map[string]interface{}——上游原实现断言 []interface{} 恒失败 → currentDate
// 前缀被静默跳过（e2e 发现的隐性缺陷回归钉）。
func TestApplyStartPlanBodyChatBodyMessagesShape(t *testing.T) {
	p := newTestPlugin()
	body := map[string]interface{}{
		"model": "GLM-5.3",
		"messages": []map[string]interface{}{
			{"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}}},
		},
	}
	cred := &credential{Plan: "start-plan", Provider: "zai", JWT: "j"}
	if err := p.applyStartPlanBody(body, cred, &pb.ChatRequest{Model: "GLM-5.3"}, "s"); err != nil {
		t.Fatal(err)
	}
	msgs, ok := body["messages"].([]interface{})
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %T %v, want 2（前缀 + 原文）", body["messages"], body["messages"])
	}
	if prefix := msgs[0].(map[string]interface{}); prefix["role"] != "user" {
		t.Fatalf("前缀角色异常: %v", prefix)
	}
}

// ---------- P1-5 会话稳定 ----------

func TestSessionIDStable(t *testing.T) {
	if got := sessionIDFor(&credential{SessionID: "fixed"}); got != "fixed" {
		t.Fatalf("blob 持久 session_id 未被优先采用: %q", got)
	}
	a := sessionIDFor(&credential{DeviceMid: "dev", APIKey: "k"})
	b := sessionIDFor(&credential{DeviceMid: "dev", APIKey: "k"})
	if a == "" || a != b {
		t.Fatalf("派生会话 id 不稳定: %q vs %q", a, b)
	}
	if c := sessionIDFor(&credential{DeviceMid: "other"}); c == a {
		t.Fatal("不同凭据派生出同值会话 id")
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(a) {
		t.Fatalf("uuidOf 非 v4 形 UUID: %q", a)
	}
	if sessionIDFor(nil) == "" {
		t.Fatal("nil 凭据应仍派生出 id")
	}
}

// ---------- P1-6 metadata（marked 不覆盖客户端自带） ----------

func TestApplyMetadataUserID(t *testing.T) {
	body := map[string]interface{}{}
	applyMetadataUserID(body, "dev-1", "sess-1")
	if uid := assertUserID(t, body); uid.DeviceID != "dev-1" || uid.AccountUUID != "" || uid.SessionID != "sess-1" {
		t.Fatalf("user_id = %+v", uid)
	}
	// 客户端自带 user_id：保留（magpie marked 同型）
	body2 := map[string]interface{}{"metadata": map[string]interface{}{"user_id": "client-uid", "custom": "x"}}
	applyMetadataUserID(body2, "dev", "sess")
	m2 := body2["metadata"].(map[string]interface{})
	if m2["user_id"] != "client-uid" || m2["custom"] != "x" {
		t.Fatalf("客户端自带 metadata 被破坏: %v", m2)
	}
}

// ---------- P1-9 错误语义映射 ----------

func TestMapUpstreamFailure(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int32
		wantIn   string
	}{
		{"405 裸风控", 405, `{"msg":"blocked"}`, 403, "风控"},
		{"code 3012", 400, `{"code":3012,"msg":"request has been blocked due to unusual activity"}`, 403, "风控"},
		{"unusual activity", 403, `{"error":{"message":"unusual activity detected"}}`, 403, "风控"},
		{"unusual activity 大小写变体", 403, `{"error":{"message":"Unusual Activity detected"}}`, 403, "风控"},
		{"aliyun 边缘页", 403, `<html><script src="https://errors.aliyun.com/x.js"></script></html>`, 502, "防火墙"},
		{"aliyun 页大写标签", 403, `<HTML><script src="https://ERRORS.aliyun.com/x.js"></script></HTML>`, 502, "防火墙"},
		{"1113 耗尽", 400, `{"code":"1113","msg":"no resource package"}`, 429, "耗尽"},
		{"1005 耗尽 error.code", 400, `{"error":{"code":"1005","message":"exceed quota limit"}}`, 429, "耗尽"},
		{"1302 限速", 429, `{"code":1302}`, 429, "限速"},
		{"1303 限速", 503, `{"code":1303}`, 429, "限速"},
		{"401 透传", 401, `{"error":{"message":"Authentication Failed type 1000"}}`, 401, "HTTP 401"},
		{"429 透传", 429, `plain busy`, 429, "HTTP 429"},
		{"500 兜底", 500, `boom`, 502, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := mapUpstreamFailure(tc.status, tc.body)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d（msg=%s）", code, tc.wantCode, msg)
			}
			if !strings.Contains(msg, tc.wantIn) {
				t.Fatalf("msg %q 不含 %q", msg, tc.wantIn)
			}
		})
	}
}

// ---------- P0-3 签名键契约 + fail-open ----------

func TestSigningSessionKeyContract(t *testing.T) {
	if headerSessionID != "x-session-id" {
		t.Fatalf("headerSessionID = %q, want 小写 x-session-id（P0-3：Go map 键大小写敏感，写/读/skip/out 四处同形）", headerSessionID)
	}
	p := newTestPlugin()
	headers := p.llmIdentityHeaders(&credential{Plan: "coding-plan", APIKey: "id.secret"})
	headers[headerSessionID] = "sess-abc" // chat.go 的写入形态（headerSessionID 常量）
	if got := signingSessionID(headers); got != "sess-abc" {
		t.Fatalf("signingSessionID 读不到 chat.go 写入的会话 id（P0-3 键失配回归）: %q", got)
	}
	if got := signingSessionID(map[string]string{"X-Session-Id": "x"}); got != "" {
		t.Fatalf("大写键不应可读（键统一后恒以小写为准）: %q", got)
	}
}

func TestSignHeadersFailOpen(t *testing.T) {
	p := newTestPlugin()
	// origin 指向不可达端口：门探测快速失败，不触真实网络
	s := newSigner(p, "https://127.0.0.1:1")
	if out, signed := s.signHeaders(&signCtx{url: "http://127.0.0.1:1/v1/messages", headers: map[string]string{}, cred: "id.secret", appVersion: "v"}); signed {
		t.Fatal("http 端点不应签名")
	} else if out == nil {
		t.Fatal("fail-open 应原样返回头（非 nil）")
	}
	if _, signed := s.signHeaders(&signCtx{url: startPlanMessagesURL, headers: map[string]string{}, cred: "x", appVersion: "v"}); signed {
		t.Fatal("start-plan 路径不应签名")
	}
	if _, signed := s.signHeaders(&signCtx{url: "https://api.z.ai/api/anthropic/v1/messages", headers: map[string]string{}, cred: "bare-key", appVersion: "v"}); signed {
		t.Fatal("非双密钥不应签名")
	}
	// 双密钥 + 会话在 + 门不可达 → fail-open，头原样
	h := map[string]string{headerSessionID: "sess-1"}
	out, signed := s.signHeaders(&signCtx{ctx: context.Background(), url: "https://api.z.ai/api/anthropic/v1/messages", headers: h, cred: "id.secret", appVersion: "v"})
	if signed {
		t.Fatal("门不可达应 fail-open（probeGate 失败冷却后返回未签名）")
	}
	if out[headerSessionID] != "sess-1" {
		t.Fatalf("fail-open 应原样返回头: %v", out)
	}
}

func TestParseSigningCredential(t *testing.T) {
	if id, secret, ok := parseSigningCredential("keyid.secretpart"); !ok || id != "keyid" || secret != "secretpart" {
		t.Fatalf("双密钥解析异常: %q %q %v", id, secret, ok)
	}
	for _, bad := range []string{"nodot", "a.b.c", ".secret", "id.", "."} {
		if _, _, ok := parseSigningCredential(bad); ok {
			t.Fatalf("%q 不应被认作双密钥", bad)
		}
	}
}

func TestDecryptSigningPrivateKeyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	secret := "test-secret"
	block, err := aes.NewCipher(hkdfSHA256(secret, kdfInfoEd25519))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := aescipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 12)
	if _, err := cryptorand.Read(iv); err != nil {
		t.Fatal(err)
	}
	cipher := append(append([]byte{}, iv...), gcm.Seal(nil, iv, pkcs8, []byte("keyid"))...)
	got, err := decryptSigningPrivateKey("keyid", secret, base64.StdEncoding.EncodeToString(cipher))
	if err != nil {
		t.Fatalf("解密握手私钥失败: %v", err)
	}
	msg := []byte("id\nts\nv\nsess\nnonce")
	if !ed25519.Verify(pub, msg, ed25519.Sign(got, msg)) {
		t.Fatal("解密出的私钥签名不可验（握手解密链路损坏）")
	}
}

func TestSolvePoW(t *testing.T) {
	const ts = "1700000000000"
	pow, err := solvePoW("keyid", "sess", ts)
	if err != nil {
		t.Fatal(err)
	}
	seedHash := sha256.Sum256([]byte(strings.Join([]string{"keyid", signingAppID, "sess", ts}, "\n")))
	seed := hex.EncodeToString(seedHash[:])[:32]
	d := sha256.Sum256([]byte(seed + "\n" + pow))
	if !leadingZeroBits(d[:], powBits) {
		t.Fatalf("PoW 候选难度不足: %q", pow)
	}
	if len(pow) != powNonceBytes*2+8 {
		t.Fatalf("candidate 形态异常: %q", pow)
	}
}

// ---------- P0-4a 静态表换代 ----------

func TestStaticModelsCurrentGeneration(t *testing.T) {
	ids := map[string]glmModel{}
	for _, m := range glmModels {
		ids[m.ID] = m
	}
	m, ok := ids["GLM-5.3"]
	if !ok || m.CtxWindow != 1000000 || m.MaxOut != 128000 || m.Label != "GLM-5.3" {
		t.Fatalf("静态表缺现行 GLM-5.3（ctx 1M）: %+v", m)
	}
	f, ok := ids["GLM-5.3-Flash"]
	if !ok || !f.Vision || f.CtxWindow != 1000000 {
		t.Fatalf("静态表缺 GLM-5.3-Flash（vision）: %+v", f)
	}
	for _, dead := range []string{"glm-4.6", "glm-4.5-air", "glm-4.5v", "GLM-4.6", "GLM-4.5-Air", "GLM-4.5V"} {
		if _, ok := ids[dead]; ok {
			t.Fatalf("过期模型仍在静态表: %s", dead)
		}
	}
	for _, m := range glmModels {
		if !modelAllowed(m, "coding-plan") || !modelAllowed(m, "start-plan") {
			t.Fatalf("%s 应双 plan 开放（live 内联 builtinProviders 口径）", m.ID)
		}
	}
	if got := staticEntries("start-plan"); len(got) != len(glmModels) {
		t.Fatalf("staticEntries(start-plan) = %d, want %d", len(got), len(glmModels))
	}
}

// ---------- P0-4b 动态目录解析 ----------

func TestPlanIDOf(t *testing.T) {
	cases := map[[2]string]string{
		{"zai", "coding-plan"}:      "account:zai-individual-coding-plan",
		{"zai", "start-plan"}:       "account:zai-start-plan",
		{"bigmodel", "coding-plan"}: "account:bigmodel-individual-coding-plan",
		{"bigmodel", "start-plan"}:  "account:bigmodel-start-plan",
	}
	for in, want := range cases {
		if got := planIDOf(in[0], in[1]); got != want {
			t.Fatalf("planIDOf(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestModelsOfRules(t *testing.T) {
	var rules providerRulesFile
	if err := json.Unmarshal([]byte(testRulesJSON), &rules); err != nil {
		t.Fatal(err)
	}
	got := modelsOf(&rules, "account:zai-individual-coding-plan")
	if len(got) != 2 {
		t.Fatalf("models = %d, want 2（enabled=false 剔除）: %+v", len(got), got)
	}
	if got[0].ID != "GLM-5.3" || got[0].CtxWindow != 1000000 || got[0].MaxOut != 128000 || got[0].Vision || !got[0].Reasoning {
		t.Fatalf("GLM-5.3 解析异常: %+v（modelRules 后者覆盖前者）", got[0])
	}
	if got[1].ID != "GLM-5.3-Flash" || !got[1].Vision || !got[1].Reasoning || got[1].CtxWindow != 1000000 {
		t.Fatalf("GLM-5.3-Flash 解析异常: %+v", got[1])
	}
	// plan 维度：start-plan 仅 Flash（GLM-5.3 enabled=false；现行规则文件 rev 23 同形态）
	sp := modelsOf(&rules, "account:zai-start-plan")
	if len(sp) != 1 || sp[0].ID != "GLM-5.3-Flash" {
		t.Fatalf("start-plan 目录异常: %+v（存疑⑥：动态目录 start-plan 不含 GLM-5.3，静态兜底含）", sp)
	}
	if modelsOf(nil, "x") != nil {
		t.Fatal("nil 规则应返回空")
	}
}

// ---------- 登录全链 e2e（P1-7 + P0-4b + P1-5 建档） ----------

func TestLoginFlowEndToEnd(t *testing.T) {
	resetCatalogCache()
	t.Cleanup(resetCatalogCache)
	oldScheme := providerConfigURLScheme
	providerConfigURLScheme = "http" // httptest 上游为 http；生产恒 https
	t.Cleanup(func() { providerConfigURLScheme = oldScheme })

	var initMid, pollMid, configsMid, configsAppVer, configsPlatformQ, zloginMid string
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/oauth/cli/init":
			initMid = r.Header.Get("X-Device-Mid")
			if r.Header.Get("Authorization") == "" {
				t.Error("init 缺 Bearer pollToken")
			}
			_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"flow_id":"flow-1","authorize_url":"https://zcode.z.ai/app/oauth/login?x=1","expires_at":1,"poll_interval_sec":1}}`)
		case r.URL.Path == "/api/v1/oauth/cli/poll/flow-1":
			pollMid = r.Header.Get("X-Device-Mid")
			_, _ = io.WriteString(w, `{"code":0,"data":{"status":"ready","token":"site-jwt","user":{"user_id":"u1"},"zai":{"access_token":"oauth-tok"}}}`)
		case r.URL.Path == "/api/auth/z/login":
			zloginMid = r.Header.Get("X-Device-Mid")
			_, _ = io.WriteString(w, `{"code":200,"data":{"access_token":"biz-tok"}}`)
		case r.URL.Path == "/api/biz/customer/getCustomerInfo":
			_, _ = io.WriteString(w, `{"code":0,"data":{"organizations":[{"organizationId":"org1","organizationName":"默认机构","projects":[{"projectId":"proj1","projectName":"默认项目"}]}]}}`)
		case r.URL.Path == "/api/biz/v1/organization/org1/projects/proj1/api_keys":
			if r.Method == http.MethodPost {
				_, _ = io.WriteString(w, `{"code":0,"data":{"apiKey":"keyid"}}`)
			} else {
				_, _ = io.WriteString(w, `{"code":0,"data":[]}`)
			}
		case r.URL.Path == "/api/biz/v1/organization/org1/projects/proj1/api_keys/copy/keyid":
			_, _ = io.WriteString(w, `{"code":0,"data":{"secretKey":"secret"}}`)
		case r.URL.Path == "/api/v1/client/configs":
			configsMid = r.Header.Get("X-Device-Mid")
			configsAppVer = r.URL.Query().Get("app_version")
			configsPlatformQ = r.URL.Query().Get("platform")
			_, _ = io.WriteString(w, `{"code":0,"data":{"configs":{"builtin_provider_config_json":"`+srvURL+`/zcode-builtin.json"}}}`)
		case r.URL.Path == "/zcode-builtin.json":
			_, _ = io.WriteString(w, testRulesJSON)
		default:
			t.Errorf("意外上游路径 %s %s", r.Method, r.URL.Path)
			_, _ = io.WriteString(w, `{"code":1,"msg":"no"}`)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	// 注入点：登录流 + 业务 API + 目录拉取全部指向 fake 上游（默认值=生产值）
	oldBase, oldZai, oldOrigin := zcodeAPIBase, zaiHost, zcodeOrigin
	zcodeOrigin = srv.URL
	zcodeAPIBase = srv.URL + "/api/v1"
	zaiHost = srv.URL
	t.Cleanup(func() { zcodeAPIBase, zaiHost, zcodeOrigin = oldBase, oldZai, oldOrigin })

	p := newTestPlugin()
	ctx := context.Background()

	// ① init：X-Device-Mid 必带且入 state（P1-7）
	r1, err := p.Login(ctx, &pb.LoginRequest{MethodId: "oauth_zai"})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Error != nil {
		t.Fatalf("init: %s", r1.Error.Message)
	}
	if initMid == "" {
		t.Fatal("loginInit 未携带 X-Device-Mid（P1-7）")
	}
	var state map[string]string
	if json.Unmarshal(r1.Next.State, &state) != nil || state["device_mid"] != initMid {
		t.Fatalf("state 未持久 device_mid: %s", r1.Next.State)
	}

	// ② poll ready → 解析 API Key → 目录校验
	r2, err := p.Login(ctx, &pb.LoginRequest{MethodId: "oauth_zai", State: r1.Next.State})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Error != nil {
		t.Fatalf("poll: %s", r2.Error.Message)
	}
	if pollMid != initMid {
		t.Fatalf("poll 的 X-Device-Mid 与 init 不一致: %q vs %q", pollMid, initMid)
	}
	if zloginMid != "" {
		t.Fatal("业务 API（z/login）不应携带 X-Device-Mid（magpie #282）")
	}
	// 目录两步拉取：X-Device-Mid 沿用建档 id、platform 用实测可行词表、版本 3.16.3
	if configsMid != initMid {
		t.Fatalf("client/configs X-Device-Mid = %q, want 建档 id", configsMid)
	}
	if configsPlatformQ != configsPlatform {
		t.Fatalf("client/configs platform = %q, want %q（qa22 词表实测）", configsPlatformQ, configsPlatform)
	}
	if configsAppVer != defaultAppVersion {
		t.Fatalf("client/configs app_version = %q, want %q（P1-8）", configsAppVer, defaultAppVersion)
	}
	// ③ blob 建档：双密钥 + 稳定设备/会话 id
	var c credential
	if json.Unmarshal(r2.Blob, &c) != nil {
		t.Fatalf("blob 非法: %s", r2.Blob)
	}
	if c.APIKey != "keyid.secret" {
		t.Fatalf("APIKey = %q, want keyid.secret（resolveCodingPlanKey 主链）", c.APIKey)
	}
	if c.DeviceMid != initMid {
		t.Fatalf("DeviceMid = %q, want 登录流程稳定 id", c.DeviceMid)
	}
	if c.SessionID == "" {
		t.Fatal("SessionID 未建档（P1-5 持久化落点）")
	}
	if c.Plan != "coding-plan" || c.Provider != "zai" {
		t.Fatalf("plan/provider = %s/%s", c.Plan, c.Provider)
	}
	if r2.Profile == nil || !r2.Profile.Healthy {
		t.Fatal("登录档案应 healthy")
	}
	// ④ 会话 id 消费端一致：sessionIDFor 应取 blob 持久值
	if got := sessionIDFor(&c); got != c.SessionID {
		t.Fatalf("sessionIDFor 未优先 blob 持久值: %q vs %q", got, c.SessionID)
	}
}

// ---------- 对话 e2e：start-plan（P0-2 + P1-5/6/7/8） ----------

func TestChatStartPlanEndToEnd(t *testing.T) {
	var hdr http.Header
	var body map[string]interface{}
	srv := sseUpstream(t, &hdr, &body, 200, sseOK)
	defer srv.Close()
	oldURL := startPlanMessagesURL
	startPlanMessagesURL = srv.URL + "/api/v1/zcode-plan/anthropic/v1/messages"
	t.Cleanup(func() { startPlanMessagesURL = oldURL })

	p := newTestPlugin()
	blob, _ := json.Marshal(&credential{Plan: "start-plan", Provider: "zai", JWT: "jwt-1", DeviceMid: "dev-e2e", SessionID: "sess-e2e"})
	stream := &fakeChatStream{}
	req := &pb.ChatRequest{
		Model: "GLM-5.3",
		Messages: []*pb.EnvelopeMessage{
			{Role: "system", Text: "你是一个代码助手"},
			{Role: "user", Text: "hello"},
		},
		Credential: &pb.CredentialBlob{Blob: blob},
	}
	if err := p.Chat(req, stream); err != nil {
		t.Fatal(err)
	}
	// 头：Bearer JWT + 设备 id + 3.16 attribution + 稳定会话 id + 版本代差
	if hdr.Get("Authorization") != "Bearer jwt-1" {
		t.Fatalf("Authorization = %q", hdr.Get("Authorization"))
	}
	if hdr.Get("X-Device-Mid") != "dev-e2e" {
		t.Fatalf("start-plan 对话缺 X-Device-Mid（P1-7）: %v", hdr)
	}
	if hdr.Get("x-zcode-query-source") != "main_turn" {
		t.Fatal("start-plan 对话缺 x-zcode-query-source（P1-8）")
	}
	if hdr.Get("x-session-id") != "sess-e2e" {
		t.Fatalf("x-session-id = %q, want blob 持久值（P1-5）", hdr.Get("x-session-id"))
	}
	if hdr.Get("X-Zcode-App-Version") != "3.16.3" {
		t.Fatalf("X-ZCode-App-Version = %q, want 3.16.3（P1-8）", hdr.Get("X-Zcode-App-Version"))
	}
	if hdr.Get("Anthropic-Version") != anthropicVersion {
		t.Fatal("anthropic-version 缺失")
	}
	// 体：恒 3 块 + 客户端文本入第三块 + metadata.user_id + 前缀消息（P0-2/P1-6）
	sys, ok := body["system"].([]interface{})
	if !ok || len(sys) != 3 {
		t.Fatalf("system 块数 = %d, want 3（P0-2）: %v", len(sys), body["system"])
	}
	third := sys[2].(map[string]interface{})["text"].(string)
	if !strings.Contains(third, "你是一个代码助手") {
		t.Fatalf("客户端 system 未并入第三块: %q", third)
	}
	if uid := assertUserID(t, body); uid.DeviceID != "dev-e2e" || uid.SessionID != "sess-e2e" {
		t.Fatalf("metadata.user_id = %+v", uid)
	}
	msgs := body["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2（前缀 user + 原 user；客户端 system 已并入块）", len(msgs))
	}
	// 流：首事件 MessageStart + 解析器吐 ContentDelta
	if len(stream.events) < 2 {
		t.Fatalf("事件数 = %d", len(stream.events))
	}
	if ev := stream.events[0].GetMessageStart(); ev == nil || ev.Model != "GLM-5.3" {
		t.Fatalf("首事件应为 MessageStart(GLM-5.3): %v", stream.events[0])
	}
	var hasDelta bool
	for _, ev := range stream.events {
		if d := ev.GetContentDelta(); d != nil && d.Text == "hi" {
			hasDelta = true
		}
	}
	if !hasDelta {
		t.Fatalf("缺 ContentDelta(hi): %+v", stream.events)
	}
}

// ---------- 对话 e2e：上游拒载 → 错误语义（P1-9 走全链） ----------

func TestChatStartPlanRefusedMapsTo403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(405)
		_, _ = io.WriteString(w, `{"code":3012,"msg":"request has been blocked due to unusual activity"}`)
	}))
	defer srv.Close()
	oldURL := startPlanMessagesURL
	startPlanMessagesURL = srv.URL + "/api/v1/zcode-plan/anthropic/v1/messages"
	t.Cleanup(func() { startPlanMessagesURL = oldURL })

	p := newTestPlugin()
	blob, _ := json.Marshal(&credential{Plan: "start-plan", Provider: "zai", JWT: "jwt-1", DeviceMid: "dev"})
	stream := &fakeChatStream{}
	req := &pb.ChatRequest{
		Model:      "GLM-5.3",
		Messages:   []*pb.EnvelopeMessage{{Role: "user", Text: "hi"}},
		Credential: &pb.CredentialBlob{Blob: blob},
	}
	if err := p.Chat(req, stream); err != nil {
		t.Fatal(err)
	}
	evs := stream.events
	if len(evs) != 1 || evs[0].GetTaskFailed() == nil {
		t.Fatalf("应恰好一个 TaskFailed 事件: %+v", evs)
	}
	if code := evs[0].GetTaskFailed().GetError().GetCode(); code != 403 {
		t.Fatalf("405/3012 应映射 403 风控, got %d", code)
	}
	if msg := evs[0].GetTaskFailed().GetError().GetMessage(); !strings.Contains(msg, "风控") {
		t.Fatalf("风控文案缺失: %q", msg)
	}
}

// ---------- 对话 e2e：coding-plan（双 auth + 会话稳定派生 + marked + #282） ----------

func TestChatCodingPlanEndToEnd(t *testing.T) {
	var hdr http.Header
	var body map[string]interface{}
	srv := sseUpstream(t, &hdr, &body, 200, sseOK)
	defer srv.Close()
	oldBase := zaiAnthropicBase
	zaiAnthropicBase = srv.URL // http 端点 → V4 签名按 scheme 门跳过（fail-open 形态之一）
	t.Cleanup(func() { zaiAnthropicBase = oldBase })

	p := newTestPlugin()
	cred := &credential{Plan: "coding-plan", Provider: "zai", APIKey: "keyid.secret", DeviceMid: "dev-c"}
	blob, _ := json.Marshal(cred)
	wantSid := uuidOf("zcode-session-device\x00dev-c")
	runChat := func() {
		t.Helper()
		stream := &fakeChatStream{}
		req := &pb.ChatRequest{
			Model:      "GLM-5.3",
			Messages:   []*pb.EnvelopeMessage{{Role: "user", Text: "hello"}},
			Credential: &pb.CredentialBlob{Blob: blob},
		}
		if err := p.Chat(req, stream); err != nil {
			t.Fatal(err)
		}
		if len(stream.events) == 0 || stream.events[0].GetMessageStart() == nil {
			t.Fatalf("缺 MessageStart: %+v", stream.events)
		}
	}
	runChat()
	if hdr.Get("x-api-key") != "keyid.secret" || hdr.Get("Authorization") != "Bearer keyid.secret" {
		t.Fatalf("coding-plan 双 auth 头异常: %v", hdr)
	}
	// 会话稳定（P1-5）：SessionID 为空 → 凭据派生，两次请求恒同值
	if sid := hdr.Get("x-session-id"); sid == "" || sid != wantSid {
		t.Fatalf("x-session-id = %q, want 派生值 %q", sid, wantSid)
	}
	if hdr.Get("X-Device-Mid") != "" {
		t.Fatal("业务 API 不应携带 X-Device-Mid（magpie #282）")
	}
	if hdr.Get("x-request-id") == "" || hdr.Get("x-zcode-session-type") != "main" || hdr.Get("x-zcode-trace-id") == "" {
		t.Fatalf("trace 头缺失: %v", hdr)
	}
	if uid := assertUserID(t, body); uid.DeviceID != "dev-c" || uid.SessionID != wantSid {
		t.Fatalf("coding-plan metadata.user_id = %+v（P1-6 marked）", uid)
	}
	firstReqID := hdr.Get("x-request-id")
	hdr = nil
	body = nil
	runChat()
	if hdr.Get("x-session-id") != wantSid {
		t.Fatalf("第二次请求会话 id 漂移: %q vs %q", hdr.Get("x-session-id"), wantSid)
	}
	if hdr.Get("x-request-id") == "" || hdr.Get("x-request-id") == firstReqID {
		t.Fatal("第二次请求 x-request-id 应重新生成且请求确实到达上游")
	}
}

// ---------- P1-8 默认版本 ----------

func TestAppVersionDefault(t *testing.T) {
	p := newTestPlugin()
	if got := p.appVersion(); got != "3.16.3" {
		t.Fatalf("appVersion = %q, want 3.16.3（P1-8：3.14.0 头代差）", got)
	}
	hs, err := p.Handshake(context.Background(), &pb.HandshakeRequest{ProtocolVersion: sdk.ProtocolVersion})
	if err != nil || hs.Error != nil {
		t.Fatalf("handshake: %v %v", err, hs.GetError())
	}
	if !strings.Contains(hs.Manifest.SettingsSchema, `"default": "3.16.3"`) {
		t.Fatalf("settings schema default 未同步 3.16.3: %s", hs.Manifest.SettingsSchema)
	}
}

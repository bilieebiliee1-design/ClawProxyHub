// Package main — zcode 对话：统一信封 → Anthropic 上游（双计划）→ StreamEvent。
// coding-plan：直连 Anthropic 端点 + V4 签名；start-plan：zcode.z.ai JWT 网关 + system 块注入。
package zcode

// 对上游 d4090ee 的功能性偏离（登记于 tools/build-plugins.sh 头注 4)，重同步上游时
// 逐项保留；bug 修复类应按 doubao PR #1 先例反哺上游）：
//   - P0-1 bigmodel 对话端点错指：upstreamBase("bigmodel") 改 open.bigmodel.cn
//     （上游两分支同值错指 api.z.ai；live 目录 bigmodel.baseUrl、magpie BIGMODEL_BASE、
//     account.go bigmodelHost 三方一致）；
//   - P0-2 start-plan 第四 system 块 405/3012 拒载形态：客户端 system 文本并入第三块
//     dynamic 尾部、恒 3 块（magpie dress #343 同型）；
//   - P1-5 x-session-id 会话稳定化（magpie #388 sessionOf 同型，双计划均外发）；
//   - P1-6 metadata.user_id 注入（magpie #387 userIdOf 同型）；
//   - P1-7 start-plan 出站携带 X-Device-Mid（magpie #282：凡 zcode.z.ai 请求必带）；
//   - P1-8 x-zcode-query-source: main_turn（3.16 attribution 头）；
//   - P1-9 上游错误语义映射（405/3012 风控、1113/1005 耗尽、1302/1303 限速、
//     Aliyun 边缘页网络提示）；
//   - 上游端点常量改 var（httptest 单测注入点，默认值=生产值）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ShadowSmallBaby/ClawProxyHub/sdk"
	"github.com/ShadowSmallBaby/ClawProxyHub/sdk/anthropicup"
	pb "github.com/ShadowSmallBaby/ClawProxyHub/sdk/proto/cphv1"
	shared "github.com/ShadowSmallBaby/ClawProxyHubPlugins/shared"
)

// signer 插件级 V4 签名管理器（懒建，origin 固定 zcode.z.ai）。
func (p *plugin) signerFor() *signer {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sg == nil {
		p.sg = newSigner(p, "https://zcode.z.ai")
	}
	return p.sg
}

// 上游 Anthropic 端点基座（var 为 httptest 单测注入点；默认值即生产值）。
var (
	zaiAnthropicBase      = "https://api.z.ai/api/anthropic"
	bigmodelAnthropicBase = "https://open.bigmodel.cn/api/anthropic"
	startPlanMessagesURL  = "https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages"
)

// upstreamBase 上游 Anthropic 端点。
// P0-1 修复：原实现两分支同返 api.z.ai（bigmodel 分支死代码），oauth_bigmodel 账号
// 对话必错端点。现行目录 bigmodel 的 baseUrl 为 open.bigmodel.cn/api/anthropic
// （qa22/client_configs.json live 实测；magpie index.mjs:30 BIGMODEL_BASE 同值）。
// account.go bigmodelHost（bigmodel.cn）仅业务面 API，不在此列。
func upstreamBase(provider string) string {
	if provider == "bigmodel" {
		return bigmodelAnthropicBase
	}
	return zaiAnthropicBase
}

// Chat 统一信封 → Anthropic SSE。
func (p *plugin) Chat(req *pb.ChatRequest, stream pb.ClawPlugin_ChatServer) error {
	ctx := stream.Context()
	cred, err := credFrom(req.GetCredential())
	if err != nil {
		return stream.Send(shared.Failed(401, err.Error()))
	}

	body := anthropicup.ChatBody(req)
	body["model"] = req.Model
	body["stream"] = true

	// UA：核心注入优先，否则 ZCode 桌面客户端形态（SDK 后缀只加 LLM 平面）
	ua := "ZCode/" + p.appVersion() + " " + anthropicSDKSuffix
	if v := req.Extra[sdk.ExtraClientUserAgent]; v != "" {
		ua = v
	}

	endpoint, headers := p.buildUpstream(ctx, cred, req, body, ua)

	// 会话稳定 id（P1-5，magpie #388 sessionOf）：双计划均随请求外发，
	// 签名与 metadata.user_id 共用
	sid := sessionIDFor(cred)

	// start-plan 网关注入 system 块 + context_prefix + metadata（3012 拒载防线）
	if cred.Plan == "start-plan" {
		if err := p.applyStartPlanBody(body, cred, req, sid); err != nil {
			return stream.Send(shared.Failed(502, err.Error()))
		}
	} else {
		// coding-plan：客户端未自带 metadata.user_id 时补齐（P1-6，magpie marked 同型）
		applyMetadataUserID(body, cred.DeviceMid, sid)
	}
	raw, _ := json.Marshal(body)

	// V4 签名（coding-plan 双密钥 + 门开才签；fail-open）
	sc := &signCtx{ctx: ctx, url: endpoint, headers: headers, cred: cred.APIKey, appVersion: p.appVersion()}
	sc.headers[headerSessionID] = sid
	if cred.Plan == "coding-plan" {
		if signed, _ := p.signerFor().signHeaders(sc); signed != nil {
			headers = signed
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return stream.Send(shared.Failed(502, err.Error()))
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.hc(cred).Do(httpReq)
	if err != nil {
		return stream.Send(shared.Failed(502, err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		code, msg := mapUpstreamFailure(resp.StatusCode, string(errBody))
		return stream.Send(shared.Failed(code, msg))
	}

	if err := stream.Send(&pb.StreamEvent{Event: &pb.StreamEvent_MessageStart{
		MessageStart: &pb.MessageStart{Model: req.Model},
	}}); err != nil {
		return err
	}

	// 上游固定 Anthropic 协议，按入口方言选解析器回吐
	parser := anthropicup.NewParser(func(ev *pb.StreamEvent) { _ = stream.Send(ev) })
	return sdk.ScanSSE(resp.Body, parser)
}

// buildUpstream 端点 + identity/trace 头（coding-plan 双 auth 头；start-plan Bearer JWT）。
func (p *plugin) buildUpstream(ctx context.Context, cred *credential, req *pb.ChatRequest, body map[string]interface{}, ua string) (string, map[string]string) {
	headers := p.llmIdentityHeaders(cred)
	headers["User-Agent"] = ua

	if cred.Plan == "start-plan" && cred.JWT != "" {
		headers["Authorization"] = "Bearer " + cred.JWT
		headers["anthropic-version"] = anthropicVersion
		// X-Device-Mid（P1-7）：zcode.z.ai 请求平面按 magpie #282 规则携带设备 id
		//（LLM 平面 api.z.ai/open.bigmodel.cn 绝不带）；缺失（旧凭据）时省略
		if cred.DeviceMid != "" {
			headers["X-Device-Mid"] = cred.DeviceMid
		}
		// 3.16 attribution 头（magpie startHeaders：what asked the model）
		headers["x-zcode-query-source"] = "main_turn"
		return startPlanMessagesURL, headers
	}
	// coding-plan：bundle ebo 双 auth 头（x-api-key + Authorization 同值）
	headers["x-api-key"] = cred.APIKey
	headers["Authorization"] = "Bearer " + cred.APIKey
	headers["anthropic-version"] = anthropicVersion
	// trace/attribution 头（bundle Bdt 形态）
	headers["x-request-id"] = shared.RandUUID()
	headers["x-zcode-session-type"] = "main"
	headers["x-zcode-trace-id"] = shared.RandUUID()
	headers["x-query-id"] = shared.RandUUID()
	return upstreamBase(cred.Provider) + "/v1/messages", headers
}

// sessionIDFor 对话会话 id（P1-5，magpie #388 sessionOf 同型：会话内恒定）。
// 信封无会话标识字段（ChatRequest 仅 Extra map；核心现行只注入 client_user_agent /
// fingerprint_headers，无会话键——sdk.go Extra 常量已核实），回退账号级稳定 UUID：
// 登录时随机生成持久进 blob（credential.SessionID，DeviceMid 同型）；旧 blob 无该
// 字段时由凭据材料确定性派生（uuidOf，同账号恒同值，无需重登录）。
func sessionIDFor(cred *credential) string {
	if cred != nil && cred.SessionID != "" {
		return cred.SessionID
	}
	seed := "zcode-session-device\x00"
	if cred != nil {
		seed += shared.OrDefault(cred.DeviceMid, shared.OrDefault(cred.APIKey, shared.OrDefault(cred.JWT, cred.UserID)))
	}
	return uuidOf(seed)
}

// uuidOf 确定性 v4 形 UUID（magpie uuidOf 同形）：同 seed 恒同值。
func uuidOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:])
	variant, _ := strconv.ParseUint(h[16:17], 16, 8)
	variant = (variant & 3) | 8
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + strconv.FormatUint(variant, 16) + h[17:20] + "-" + h[20:32]
}

// applyMetadataUserID metadata.user_id 注入（P1-6，magpie marked 同型）：客户端未自带
// 时补 {"device_id":…,"account_uuid":"","session_id":…}；网关 prompt cache 以
// device_id 定位机器，缺省时 cache 大部落空（#387）。
func applyMetadataUserID(body map[string]interface{}, deviceMid, sessionID string) {
	meta, _ := body["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	if cur, ok := meta["user_id"].(string); ok && strings.TrimSpace(cur) != "" {
		return // 客户端自带（magpie marked：不覆盖）
	}
	meta["user_id"] = userIdOf(deviceMid, sessionID)
	body["metadata"] = meta
}

// userIdOf metadata.user_id 的 ZCode 客户端形态（magpie userIdOf 同型）。
func userIdOf(deviceMid, sessionID string) string {
	b, _ := json.Marshal(map[string]string{"device_id": deviceMid, "account_uuid": "", "session_id": sessionID})
	return string(b)
}

// applyStartPlanBody start-plan 网关身份块：system 恒三块（ephemeral）+ currentDate
// 前缀 user 消息 + metadata.user_id。
// P0-2 修复（magpie dress #343 同型）：客户端 system 文本并入第三块 dynamic 尾部——
// ZCode 客户端把每个 dynamic 段以 "\n\n" join 进同一块，从不发第四块；zcode.z.ai
// 对第四块以 405/3012 拒载。原实现（上游形态）把客户端块追加为第四+块。
func (p *plugin) applyStartPlanBody(body map[string]interface{}, cred *credential, req *pb.ChatRequest, sid string) error {
	data, err := p.systemBlocks()
	if err != nil {
		return err
	}
	var cliPrefix, dynBefore, dynAfter string
	var envMap, cpMap, srMap map[string]string
	json.Unmarshal(data["cliPrefix"], &cliPrefix)
	var stableArr []string
	json.Unmarshal(data["stableSections"], &stableArr)
	json.Unmarshal(data["dynamicSections"], &struct {
		BeforeEnvironment *string
		AfterEnvironment  *string
	}{&dynBefore, &dynAfter})
	json.Unmarshal(data["environment"], &envMap)
	json.Unmarshal(data["contextPrefix"], &cpMap)
	json.Unmarshal(data["systemReminder"], &srMap)

	// Environment 段（真实运行时值 + powered-by）
	env := strings.Join([]string{
		envMap["heading"],
		envMap["invokedLine"],
		"- " + envMap["cwdLabel"] + ": " + cwdOrUnknown(),
		"- " + envMap["gitLabel"] + ": " + envMap["gitNo"],
		"- " + envMap["platformLabel"] + ": " + Platform(),
		"- " + envMap["shellLabel"] + ": " + shellName(),
		"- " + envMap["osVersionLabel"] + ": " + osVersionLine(),
		"- " + strings.Replace(strings.Replace(envMap["poweredByLine"], "{provider}", providerID(cred.Provider), 1), "{model}", req.Model, 1),
	}, "\n")

	stableText := strings.Join(stableArr, "\n\n")

	// 客户端 system 文本归一化后并入第三块 dynamic 尾部（P0-2：恒 3 块）
	var own []string
	if existing, ok := body["system"]; ok {
		for _, b := range normalizeUserSystem(existing) {
			if s, ok := b["text"].(string); ok {
				own = append(own, s)
			}
		}
	}
	dynamicParts := append([]string{dynBefore, env, dynAfter}, own...)
	dynamic := strings.Join(dynamicParts, "\n\n")

	system := []map[string]interface{}{
		{"type": "text", "text": cliPrefix, "cache_control": map[string]string{"type": "ephemeral"}},
		{"type": "text", "text": stableText, "cache_control": map[string]string{"type": "ephemeral"}},
		{"type": "text", "text": "\n\n" + dynamic, "cache_control": map[string]string{"type": "ephemeral"}},
	}
	body["system"] = system

	// metadata.user_id（P1-6）：网关按机器定位 prompt cache（magpie dress 覆写语义）
	meta, _ := body["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["user_id"] = userIdOf(cred.DeviceMid, sid)
	body["metadata"] = meta

	// context_prefix：currentDate <system-reminder> 前缀 user 消息。
	// 注：anthropicup.ChatBody 的 messages 为 []map[string]interface{}，上游原实现
	// 的 []interface{} 断言恒失败 → 前缀被静默跳过（本轮 e2e 单测发现的隐性缺陷，
	// 登记于 tools/build-plugins.sh 头注 4)②）；两种形态均归一化处理。
	var msgs []interface{}
	switch v := body["messages"].(type) {
	case []interface{}:
		msgs = v
	case []map[string]interface{}:
		for _, m := range v {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) > 0 {
		date := time.Now().Format("2006-01-02")
		currentDate := cpMap["currentDateHeading"] + "\n" + strings.Replace(cpMap["currentDateLine"], "{date}", date, 1)
		text := srMap["open"] + strings.Join([]string{cpMap["intro"], currentDate, "", cpMap["outro"]}, "\n") + srMap["close"]
		prefix := map[string]interface{}{
			"role":    "user",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
		}
		body["messages"] = append([]interface{}{prefix}, msgs...)
	}
	// tools 的 cache_control 剥离（官方 3 块 + 末消息标记已占满 4 断点预算）
	if tools, ok := body["tools"].([]interface{}); ok {
		for _, t := range tools {
			if m, ok := t.(map[string]interface{}); ok {
				delete(m, "cache_control")
			}
		}
	}
	return nil
}

// normalizeUserSystem 客户端 system 块归一化（string / 块数组；剥 cache_control）。
func normalizeUserSystem(v interface{}) []map[string]interface{} {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []map[string]interface{}{{"type": "text", "text": s}}
		}
	case []interface{}:
		var out []map[string]interface{}
		for _, item := range t {
			if m, ok := item.(map[string]interface{}); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, map[string]interface{}{"type": "text", "text": s})
				}
			} else if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, map[string]interface{}{"type": "text", "text": s})
			}
		}
		return out
	}
	return nil
}

// ---------- 上游错误语义（P1-9，magpie index.mjs ≈1546-1600 / 267-277 同型） ----------

var (
	// 上游风控拒载标记：HTTP 405 裸形态、unusual activity、JSON code 3012
	//（#343 请求形态 / #348 账号风控均此错；大小写不敏感对齐 magpie
	// START_REFUSED 的 /i 形态，上游文案大小写变体不漏配）
	startRefusedRe = regexp.MustCompile(`(?i)unusual activity|request has been blocked|"code"\s*:\s*"?3012\b`)
	// Aliyun 边缘防火墙拦截页（网络/IP 维度，非账号；/i 对齐 magpie EDGE_PAGE）
	edgePageRe = regexp.MustCompile(`(?i)errors\.aliyun\.com|<html`)
)

// mapUpstreamFailure 上游错误 → 统一信封 (code, message)：
//   - Aliyun 边缘页 → 502 网络提示（换网络/代理，非账号问题）；
//   - 405 / 3012 / unusual activity → 403 账号风控（ZCode 官方客户端同账号同错，
//     只能等待或联系官方）；
//   - code 1113 / 1005 → 429 套餐耗尽；1302 / 1303 → 429 上游限速；
//   - 其余按 HTTP 状态（401/403→401、429→429、余 502）。
func mapUpstreamFailure(status int, body string) (int32, string) {
	if status >= 400 && edgePageRe.MatchString(body) {
		return 502, fmt.Sprintf("上游边缘防火墙拦截了本网络出口的请求：请稍后再试或更换网络/代理（HTTP %d）", status)
	}
	if status >= 400 && (status == 405 || startRefusedRe.MatchString(body)) {
		return 403, fmt.Sprintf("上游风控已限制该账号（unusual activity / code 3012）：账号被暂时休息，请稍后再试或更换账号；若 ZCode 官方客户端同账号同样失败，需联系官方解封（HTTP %d）", status)
	}
	var env struct {
		Code  json.Number `json:"code"`
		Msg   string      `json:"msg"`
		Error *struct {
			Code    json.Number `json:"code"`
			Message string      `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &env) == nil {
		c1, c2 := env.Code.String(), ""
		if env.Error != nil {
			c2 = env.Error.Code.String()
		}
		switch {
		case c1 == "1113" || c1 == "1005" || c2 == "1113" || c2 == "1005":
			return 429, "套餐额度已耗尽（code 1113/1005）：请等待额度重置或更换账号"
		case c1 == "1302" || c1 == "1303" || c2 == "1302" || c2 == "1303":
			return 429, "上游限速（code 1302/1303）：请求过于频繁，请稍后再试"
		}
	}
	code := int32(502)
	switch {
	case status == 401 || status == 403:
		code = 401
	case status == 429:
		code = 429
	}
	return code, fmt.Sprintf("HTTP %d: %s", status, shared.Truncate(body, 300))
}

func cwdOrUnknown() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "unknown"
}

func shellName() string {
	if v := os.Getenv("SHELL"); v != "" {
		return filepath.Base(v)
	}
	if v := os.Getenv("ComSpec"); v != "" {
		return filepath.Base(v)
	}
	return "unknown"
}

func osVersionLine() string {
	if r := Release(); r != "" {
		return Platform() + " " + r
	}
	return Platform()
}

func providerID(provider string) string {
	if provider == "bigmodel" {
		return "bigmodel-api"
	}
	return "zai-api"
}

// ---------- SSE 扫描 ----------

// idCounter 同毫秒内的单调计数。
var idCounter uint64

var (
	_ = atomic.AddUint64
	_ = rand.Read
)

// Package main — zcode 模型目录：静态兜底表 + 动态两步拉取 + gRPC ListModels。
//
// P0-4 对上游 d4090ee 的偏离（登记于 tools/build-plugins.sh 头注 4)）：
//   - 静态表换代：glm-4.6/glm-4.5-air/glm-4.5v 世代过期（现行目录已无此三者），
//     换为 GLM-5.3 / GLM-5.3-Flash（ctx 1,000,000 / maxOut 128,000，Flash 带 vision，
//     双 plan 开放）——据 qa22/client_configs.json live 实测：builtinProviders 全部
//     defaultModel=GLM-5.3、models=[GLM-5.3,GLM-5.3-Flash]；
//   - 新增动态目录两步拉取（magpie remoteConfig/modelsOf 同型）：
//     ① GET zcode.z.ai/api/v1/client/configs?app_version=&platform= →
//       data.configs.builtin_provider_config_json；
//     ② GET 该 URL → zcode-builtin-*.json（revision + 规则叠加层）→ modelsOf 解析。
//     10min 缓存 + 失败 60s 冷却 + last-good 回退 + 静态兜底。
//
// 存疑（如实标注）：动态目录下 start-plan 依现行规则文件（zcode-builtin-23 的
// builtinProviderModelRules，rev 23 实测）不含 GLM-5.3（仅 Flash/GLM-5.2/GLM-5-Turbo；
// magpie 静态注释 "the Start Plan has all but GLM-5.3" 同），而静态兜底按 live 内联
// builtinProviders 双 plan 全含 GLM-5.3（方案指示以 live 内联为准）——动态拉取成功时
// 以规则文件为准，失败回退静态表时 start-plan 仍含 GLM-5.3。
package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/ShadowSmallBaby/ClawProxyHub/sdk/proto/cphv1"
	shared "github.com/ShadowSmallBaby/ClawProxyHubPlugins/shared"
)

// glmModel 目录条目（静态表与动态目录共用；Label 恒为模型 id 本身——现行目录
// 模型 id 即大写形态 GLM-5.3，与旧表小写 id/大写 Label 分离的形态不同）。
type glmModel struct {
	ID        string
	Label     string
	CtxWindow int32
	MaxOut    int32
	Reasoning bool
	Vision    bool
	// Plans 该模型可用的套餐；空 = 全部套餐可用。
	Plans []string
}

// glmModels 静态兜底目录（P0-4 换代：ZCode 现行目录世代；旧表
// glm-4.6/glm-4.5-air/glm-4.5v 已从现行目录消失，glm-4.5-air 仅剩官方
// ANTHROPIC_DEFAULT_HAIKU_MODEL 映射位）。双 plan 全开放据 live 内联 builtinProviders。
var glmModels = []glmModel{
	{ID: "GLM-5.3", Label: "GLM-5.3", CtxWindow: 1000000, MaxOut: 128000, Reasoning: true, Plans: []string{"coding-plan", "start-plan"}},
	{ID: "GLM-5.3-Flash", Label: "GLM-5.3-Flash", CtxWindow: 1000000, MaxOut: 128000, Reasoning: true, Vision: true, Plans: []string{"coding-plan", "start-plan"}},
}

// modelAllowed 该模型是否对当前套餐开放（Plans 空 = 全开放）。
func modelAllowed(m glmModel, plan string) bool {
	if len(m.Plans) == 0 {
		return true
	}
	return slices.Contains(m.Plans, plan)
}

// staticEntries 静态兜底目录按套餐过滤。
func staticEntries(plan string) []glmModel {
	var out []glmModel
	for _, m := range glmModels {
		if modelAllowed(m, plan) {
			out = append(out, m)
		}
	}
	return out
}

// ---------- 动态目录（P0-4b，magpie remoteConfig/modelsOf 同型） ----------

const (
	// catalogTTL 动态目录缓存时长（magpie 同款 10min）。
	catalogTTL = 10 * time.Minute
	// catalogNegCool 拉取失败冷却（避免每请求重试打上游）。
	catalogNegCool = time.Minute
	// catalogTimeout 两步拉取整体超时（magpie AbortSignal.timeout(8000) 同值）。
	catalogTimeout = 8 * time.Second
	// configsPlatform client/configs 的 platform 查询参数词表。
	// 伪装决策点（存疑，如实标注）：qa22 诊断实测该端点只认 windows-x86_64 形
	// （windows-amd64 / android-* / linux-amd64 均 3001 parameter error；magpie 自身
	// 发 win32-x64 形——三实现三形态，messages 端点头的接受度未测）。目录为全局性、
	// 内容与平台无关，安卓设备直查借用实测可行的桌面词表。
	configsPlatform = "windows-x86_64"
)

// planIDOf (provider, plan) → ZCode provider 配置的 plan 维度 id
//（magpie planID 同型；team/offpeak 形态本插件不建档，不解析）。
func planIDOf(provider, plan string) string {
	site := "zai"
	if provider == "bigmodel" {
		site = "bigmodel"
	}
	if plan == "start-plan" {
		return "account:" + site + "-start-plan"
	}
	return "account:" + site + "-individual-coding-plan"
}

// providerRulesFile zcode-builtin-*.json（provider 配置规则叠加层，revision 23 实测
// schemaVersion 1）。
type providerRulesFile struct {
	Revision int `json:"revision"`
	Config   struct {
		ProviderConfigRules struct {
			ProviderRules []struct {
				ProviderID      string   `json:"providerId"`
				BuiltinModelIDs []string `json:"builtinModelIds"`
			} `json:"providerRules"`
		} `json:"providerConfigRules"`
		ModelConfigRules struct {
			ModelRules []struct {
				ModelMatch string `json:"modelMatch"`
				Config     struct {
					Properties struct {
						ContextWindow int `json:"contextWindow"`
						InputFormat   struct {
							SupportsImage *bool `json:"supportsImage"`
						} `json:"inputFormat"`
					} `json:"properties"`
					OptionSpecs struct {
						MaxOutputTokens struct {
							Max int `json:"max"`
						} `json:"maxOutputTokens"`
						ReasoningLevel struct {
							Values []string `json:"values"`
						} `json:"reasoningLevel"`
					} `json:"optionSpecs"`
				} `json:"config"`
			} `json:"modelRules"`
			BuiltinProviderModelRules []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
				Config     *struct {
					Enabled *bool `json:"enabled"`
				} `json:"config"`
			} `json:"builtinProviderModelRules"`
		} `json:"modelConfigRules"`
	} `json:"config"`
}

// modelsOf 规则叠加层 → plan 维度模型目录（magpie modelsOf 同型）：plan 的
// providerRules.builtinModelIds + builtinProviderModelRules（enabled=false 剔除）
// 取模型 id（保声明序、去重不分大小写），modelRules 正则（不区分大小写，后匹配者
// 覆盖先匹配者）填 ctx/maxOut/vision/reasoning。产出为空 = 该 plan 无目录（调用方
// 回退）。
func modelsOf(cfg *providerRulesFile, planID string) []glmModel {
	if cfg == nil {
		return nil
	}
	c := cfg.Config
	var ids []string
	on := map[string]bool{}
	add := func(id string) {
		k := strings.ToLower(id)
		if id != "" && !on[k] {
			on[k] = true
			ids = append(ids, id)
		}
	}
	for _, r := range c.ProviderConfigRules.ProviderRules {
		if r.ProviderID == planID {
			for _, id := range r.BuiltinModelIDs {
				add(id)
			}
		}
	}
	off := map[string]bool{}
	for _, r := range c.ModelConfigRules.BuiltinProviderModelRules {
		if r.ProviderID != planID {
			continue
		}
		if r.Config != nil && r.Config.Enabled != nil && !*r.Config.Enabled {
			off[strings.ToLower(r.ModelID)] = true
		} else {
			add(r.ModelID)
		}
	}
	var out []glmModel
	for _, id := range ids {
		if off[strings.ToLower(id)] {
			continue
		}
		m := glmModel{ID: id, Label: id}
		for _, r := range c.ModelConfigRules.ModelRules {
			re, err := regexp.Compile(`(?i)^(?:` + r.ModelMatch + `)$`)
			if err != nil || !re.MatchString(id) {
				continue
			}
			x := r.Config
			if x.Properties.ContextWindow > 0 {
				m.CtxWindow = clampInt32(x.Properties.ContextWindow)
			}
			if x.OptionSpecs.MaxOutputTokens.Max > 0 {
				m.MaxOut = clampInt32(x.OptionSpecs.MaxOutputTokens.Max)
			}
			if x.Properties.InputFormat.SupportsImage != nil {
				m.Vision = *x.Properties.InputFormat.SupportsImage
			}
			if len(x.OptionSpecs.ReasoningLevel.Values) > 0 {
				m.Reasoning = slices.ContainsFunc(x.OptionSpecs.ReasoningLevel.Values, func(v string) bool {
					return v != "none" && v != "disabled"
				})
			}
		}
		out = append(out, m)
	}
	return out
}

func clampInt32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// catalogSlot 动态目录缓存槽（plan 维度各存一份，兼作 last-good）。
type catalogSlot struct {
	entries []glmModel
	at      time.Time
}

var (
	catalogMu    sync.Mutex
	catalogCache = map[string]catalogSlot{} // planID → 缓存/last-good
	catalogNegAt time.Time                  // 拉取失败冷却起点
	anyDeviceMID string                     // 匿名拉取（无凭据）的设备 id（进程级，懒生成）
)

// resetCatalogCache 测试注入点：清空动态目录缓存。
func resetCatalogCache() {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalogCache = map[string]catalogSlot{}
	catalogNegAt = time.Time{}
}

// catalogFor 动态目录：缓存新鲜直接用；过期两步拉取（成功刷新缓存，失败 60s 冷却）；
// 拉取失败/空产出回退 last-good；无 last-good 返回 nil（调用方回退静态表）。
func (p *plugin) catalogFor(ctx context.Context, c *credential, planID string) []glmModel {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	slot, ok := catalogCache[planID]
	now := time.Now()
	fresh := ok && now.Sub(slot.at) < catalogTTL
	cooled := !catalogNegAt.IsZero() && now.Sub(catalogNegAt) < catalogNegCool
	if !fresh && !cooled {
		entries, err := p.fetchCatalog(ctx, c, planID)
		if err == nil && len(entries) > 0 {
			catalogCache[planID] = catalogSlot{entries: entries, at: now}
			return entries
		}
		catalogNegAt = now
	}
	if ok {
		return slot.entries
	}
	return nil
}

// fetchCatalog 两步拉取：
// ① GET {ZCODE}/api/v1/client/configs?app_version=<appVersion>&platform=<桌面词表>
//   → data.configs.builtin_provider_config_json（https URL）；
// ② GET 该 URL → zcode-builtin-*.json → modelsOf(planID) 解析。
// 请求平面：zcode.z.ai 域名按 magpie #282 规则携带 X-Device-Mid（有凭据用
// credential.DeviceMid，匿名用进程级随机 UUID）；CDN 域名只带 UA（magpie 同型）。
func (p *plugin) fetchCatalog(ctx context.Context, c *credential, planID string) ([]glmModel, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	device := ""
	if c != nil {
		device = c.DeviceMid
	}
	if device == "" {
		if anyDeviceMID == "" {
			anyDeviceMID = shared.RandUUID()
		}
		device = anyDeviceMID
	}
	cfgURL, err := p.fetchProviderConfigURL(ctx, device)
	if err != nil {
		return nil, err
	}
	raw, err := p.httpGet(ctx, cfgURL, "")
	if err != nil {
		return nil, fmt.Errorf("provider config fetch: %w", err)
	}
	var rules providerRulesFile
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("provider config parse: %w", err)
	}
	return modelsOf(&rules, planID), nil
}

// providerConfigURLScheme builtin_provider_config_json 的 scheme 门（单测注入点：
// httptest 上游为 http；生产恒 https——magpie 同款 startsWith("https://") 门）。
var providerConfigURLScheme = "https"

// fetchProviderConfigURL 第一步：client/configs → builtin_provider_config_json。
func (p *plugin) fetchProviderConfigURL(ctx context.Context, device string) (string, error) {
	q := url.Values{"app_version": {p.appVersion()}, "platform": {configsPlatform}}
	raw, err := p.httpGet(ctx, zcodeAPIBase+"/client/configs?"+q.Encode(), device)
	if err != nil {
		return "", fmt.Errorf("client/configs: %w", err)
	}
	var env struct {
		Code json.Number `json:"code"`
		Msg  string      `json:"msg"`
		Data *struct {
			Configs *struct {
				BuiltinProviderConfigJSON string `json:"builtin_provider_config_json"`
			} `json:"configs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("client/configs decode: %w", err)
	}
	if c := env.Code.String(); c != "" && c != "0" && c != "200" {
		return "", fmt.Errorf("client/configs: code=%s msg=%s", c, env.Msg)
	}
	if env.Data == nil || env.Data.Configs == nil {
		return "", fmt.Errorf("client/configs: 缺 configs")
	}
	u := env.Data.Configs.BuiltinProviderConfigJSON
	if !strings.HasPrefix(u, providerConfigURLScheme+"://") {
		return "", fmt.Errorf("client/configs: provider config URL 非 %s", providerConfigURLScheme)
	}
	return u, nil
}

// httpGet 匿名 GET（UA = ZCode/<appVersion>；device 非空且目标是 zcode.z.ai 时带
// X-Device-Mid，magpie call() #282 规则同型）。
func (p *plugin) httpGet(ctx context.Context, rawURL, device string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZCode/"+p.appVersion())
	req.Header.Set("Accept", "application/json")
	if device != "" && strings.HasPrefix(rawURL, zcodeOrigin+"/") {
		req.Header.Set("X-Device-Mid", device)
	}
	resp, err := p.hc(nil).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: HTTP %d", shared.Truncate(rawURL, 120), resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// listModels 动态目录优先（按 plan 维度），last-good 次之，静态兜底——网络失败不
// 致命（verifyCredential 依赖本函数不硬失败，登录链路不新增失败点）。
func (p *plugin) listModels(ctx context.Context, c *credential) ([]*pb.ModelInfo, error) {
	provider, plan := "zai", "coding-plan"
	if c != nil {
		if c.Provider == "bigmodel" {
			provider = "bigmodel"
		}
		if c.Plan != "" {
			plan = c.Plan
		}
	}
	entries := p.catalogFor(ctx, c, planIDOf(provider, plan))
	if len(entries) == 0 {
		entries = staticEntries(plan)
	}
	var out []*pb.ModelInfo
	for _, m := range entries {
		out = append(out, &pb.ModelInfo{
			Id:             m.ID,
			Label:          map[string]string{"en": m.Label},
			ContextWindow:  m.CtxWindow,
			SupportsTools:  true,
			SupportsStream: true,
		})
	}
	return out, nil
}

// ListModels gRPC：SyncModels / RefreshCatalog 拉取目录（凭据 blob 决定套餐维度）。
func (p *plugin) ListModels(ctx context.Context, credBlob *pb.CredentialBlob) (*pb.ModelList, error) {
	c, err := credFrom(credBlob)
	if err != nil {
		return nil, err
	}
	models, err := p.listModels(ctx, c)
	if err != nil {
		return nil, err
	}
	return &pb.ModelList{Models: models}, nil
}

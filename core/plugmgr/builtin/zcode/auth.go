// Package main — zcode 认证：OAuth 设备码登录（init/poll）+ 凭据校验 + gRPC Refresh。
package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	pb "github.com/ShadowSmallBaby/ClawProxyHub/sdk/proto/cphv1"
	shared "github.com/ShadowSmallBaby/ClawProxyHubPlugins/shared"
)

func (p *plugin) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResult, error) {
	provider := "zai"
	if req.MethodId == "oauth_bigmodel" {
		provider = "bigmodel"
	} else if req.MethodId != "oauth_zai" {
		return nil, fmt.Errorf("unknown auth method: %s", req.MethodId)
	}
	if len(req.State) == 0 {
		return p.loginInit(ctx, provider)
	}
	return p.loginPoll(ctx, provider, req.State)
}

func (p *plugin) loginInit(ctx context.Context, provider string) (*pb.LoginResult, error) {
	pollToken := shared.RandHex(32)
	// 流程级设备 id（P1-7，magpie #282：凡 zcode.z.ai 请求必带）：init 生成，
	// 经 state 传递到 poll 并建档 credential.DeviceMid，保证同账号设备 id 稳定
	deviceMid := shared.RandUUID()
	body, _ := json.Marshal(map[string]string{"provider": provider})
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", zcodeAPIBase+"/oauth/cli/init", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer "+pollToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Device-Mid", deviceMid)
	resp, err := p.hc(nil).Do(httpReq)
	if err != nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 502, Message: "login init: " + err.Error()}}, nil
	}
	defer resp.Body.Close()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			FlowID          string `json:"flow_id"`
			AuthorizeURL    string `json:"authorize_url"`
			ExpiresAt       int64  `json:"expires_at"`
			PollIntervalSec int    `json:"poll_interval_sec"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil || env.Code != 0 || env.Data == nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 502, Message: fmt.Sprintf("login init failed: HTTP %d code=%d msg=%s", resp.StatusCode, env.Code, env.Msg)}}, nil
	}
	state, _ := json.Marshal(map[string]string{
		"provider": provider, "flow_id": env.Data.FlowID, "poll_token": pollToken,
		"device_mid": deviceMid,
	})
	return &pb.LoginResult{Next: &pb.LoginNextStep{
		Action: "open_url", Url: env.Data.AuthorizeURL,
		Prompt: map[string]string{
			"zh": "已打开授权页，请在浏览器完成登录授权，然后点击「我已完成授权」",
			"en": "Auth page opened; complete sign-in in the browser, then confirm below",
		},
		State: state,
		Wait:  false,
		Fields: []*pb.AuthField{{
			Name: "confirm", Label: map[string]string{"zh": "确认授权", "en": "Confirm"},
			Type: "confirm", Placeholder: "",
		}},
	}}, nil
}

// loginPoll 轮询授权结果：ready → 解析上游 API Key（coding-plan 双密钥）+ JWT 建档。
func (p *plugin) loginPoll(ctx context.Context, provider string, state []byte) (*pb.LoginResult, error) {
	var s struct {
		FlowID    string `json:"flow_id"`
		PollToken string `json:"poll_token"`
		DeviceMid string `json:"device_mid"`
	}
	if json.Unmarshal(state, &s) != nil || s.FlowID == "" || s.PollToken == "" {
		return &pb.LoginResult{Error: &pb.Error{Code: 400, Message: "state 已失效，请重新发起登录"}}, nil
	}
	// 旧版本 state 无 device_mid：补生成并回写 state，保证跨轮询/建档稳定（P1-7）
	if s.DeviceMid == "" {
		s.DeviceMid = shared.RandUUID()
		state, _ = json.Marshal(map[string]string{
			"provider": provider, "flow_id": s.FlowID, "poll_token": s.PollToken, "device_mid": s.DeviceMid,
		})
	}
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", zcodeAPIBase+"/oauth/cli/poll/"+url.PathEscape(s.FlowID), nil)
	httpReq.Header.Set("Authorization", "Bearer "+s.PollToken)
	httpReq.Header.Set("X-Device-Mid", s.DeviceMid)
	resp, err := p.hc(nil).Do(httpReq)
	if err != nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 502, Message: "login poll: " + err.Error()}}, nil
	}
	defer resp.Body.Close()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			User   struct {
				UserID string `json:"user_id"`
			} `json:"user"`
			Zai *struct {
				AccessToken string `json:"access_token"`
			} `json:"zai"`
			Bigmodel *struct {
				AccessToken string `json:"access_token"`
			} `json:"bigmodel"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 502, Message: "login poll decode: " + err.Error()}}, nil
	}
	if env.Code != 0 {
		return &pb.LoginResult{Error: &pb.Error{Code: 401, Message: "授权失败: " + env.Msg}}, nil
	}
	switch env.Data.Status {
	case "pending":
		return &pb.LoginResult{Next: &pb.LoginNextStep{
			Action: "wait",
			Prompt: map[string]string{
				"zh": "尚未完成授权，请先在浏览器完成登录后重试",
				"en": "Authorization pending; complete sign-in in the browser and retry",
			},
			State: state,
			Wait:  false,
			Fields: []*pb.AuthField{{
				Name: "confirm", Label: map[string]string{"zh": "确认授权", "en": "Confirm"},
				Type: "confirm", Placeholder: "",
			}},
		}}, nil
	case "failed":
		return &pb.LoginResult{Error: &pb.Error{Code: 401, Message: "授权失败，请重试"}}, nil
	}

	var accessToken string
	if provider == "zai" && env.Data.Zai != nil {
		accessToken = strings.TrimSpace(env.Data.Zai.AccessToken)
	} else if provider == "bigmodel" && env.Data.Bigmodel != nil {
		accessToken = strings.TrimSpace(env.Data.Bigmodel.AccessToken)
	}
	if accessToken == "" {
		return &pb.LoginResult{Error: &pb.Error{Code: 502, Message: "授权响应缺少 access_token"}}, nil
	}

	c := &credential{Plan: "coding-plan", Provider: provider, JWT: env.Data.Token, UserID: env.Data.User.UserID}
	if err := p.resolveCodingPlanKey(ctx, c, accessToken); err != nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 401, Message: err.Error()}}, nil
	}
	// 设备/会话 id 建档（P1-7/P1-5）：device_mid 沿登录流程 state 传递（同账号稳定，
	// 后续 zcode.z.ai 请求平面必带）；session_id 登录时随机生成持久进 blob
	//（sessionIDFor 消费，旧 blob 缺失时按凭据材料确定性派生）
	c.DeviceMid = s.DeviceMid
	c.SessionID = shared.RandUUID()
	if err := p.verifyCredential(ctx, c); err != nil {
		return &pb.LoginResult{Error: &pb.Error{Code: 401, Message: "凭据校验失败: " + err.Error()}}, nil
	}
	blob, _ := json.Marshal(c)
	name := "zcode-" + provider
	return &pb.LoginResult{
		Blob:    blob,
		Profile: &pb.AccountProfile{DisplayName: name, Healthy: true, Quota: map[string]string{}},
	}, nil
}

// verifyCredential 能列模型即有效。P0-4 动态目录后本函数随目录拉取部分真实化：
// 登录账号的 client/configs → provider config 两步真联网（真实凭据+目录解析成功
// 即完成一次真实上游校验）；拉取失败回退静态表 → 校验软通过，不新增登录失败点
//（P1-11「与动态目录绑定做」的落地形态；全量实化待真实密钥自测后另议）。
func (p *plugin) verifyCredential(ctx context.Context, c *credential) error {
	_, err := p.listModels(ctx, c)
	return err
}

// Refresh 凭据续期：coding-plan 双密钥长期有效，重校验后原样返回；
// start-plan JWT 续期需登录流程捕获 refresh_token（当前未实现），亦重校验后原样返回。
func (p *plugin) Refresh(ctx context.Context, credBlob *pb.CredentialBlob) (*pb.RefreshResult, error) {
	c, err := credFrom(credBlob)
	if err != nil {
		return &pb.RefreshResult{Error: &pb.Error{Code: 400, Message: err.Error()}}, nil
	}
	if err := p.verifyCredential(ctx, c); err != nil {
		return &pb.RefreshResult{Error: &pb.Error{Code: 401, Message: "凭据校验失败: " + err.Error()}}, nil
	}
	blob, _ := json.Marshal(c)
	return &pb.RefreshResult{Blob: blob}, nil
}

// GetProfile 账号档案：凭据有效性由 verifyCredential 判定，固定展示名 + 健康（上游无独立档案接口）。
func (p *plugin) GetProfile(ctx context.Context, credBlob *pb.CredentialBlob) (*pb.AccountProfile, error) {
	c, err := credFrom(credBlob)
	if err != nil {
		return nil, err
	}
	name := "zcode-" + c.Provider
	return &pb.AccountProfile{DisplayName: name, Healthy: true, Quota: map[string]string{}}, nil
}

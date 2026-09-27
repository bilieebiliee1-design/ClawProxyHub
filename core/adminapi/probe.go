// probe.go — 一键测活（NexPort v1.3.0 方案 ③）。
//
// POST /admin/probe/run?concurrency=4&account_id= ：并行探测所有渠道/模型，NDJSON
// 进度流实时输出过程与结果（与一键签到同一通道形态）：
//
//	{"total":N,"concurrency":4}                       首事件：目标数与并行度
//	{"target":{...},"phase":"start"}                  目标开始探测
//	{"target":{...},"phase":"retry","attempt":2,
//	 "error":"..."}                                   失败退避重试
//	{"target":{...},"phase":"result","ok":true,
//	 "connect_ms":..,"first_token_ms":..,"total_ms":..,
//	 "tokens_per_sec":..,"output_tokens":..,"attempts":1}  单目标结果
//	{"done":true,"ok":M,"failed":K,"total":N}         收尾汇总
//
// 控制变量法（指标横向可比的关键）：所有探测使用完全相同的请求形态——固定提示词
// "ping"、max_tokens=16、temperature=0、stream=true、同一首帧超时（settings 全局值）；
// 唯一变量是渠道（账号凭据）与模型本身。探测直连插件 Chat（不经路由/网关 mux），
// 不落 request_logs——避免影响仪表盘统计口径。
//
// 指标定义：
//   - connect_ms 连接时间：请求发出 → 收到第一个事件（含插件进程 + 上游拨号/TLS）；
//   - first_token_ms 首字时间：请求发出 → 首个内容/推理增量；
//   - total_ms 总耗时；tokens_per_sec 出字速度 = 输出 token 数 ÷（total−first_token）；
//     上游未回 usage 时按字符数 / 3.5 估算（与网关 context_bytes_per_token 默认一致）；
//   - 失败最多 3 次退避重试（1s/2s/4s），结果计最后一次错误的 attempts 次数。
package adminapi

import (
	"context"
	"fmt"
	"net/http"

	"sync"
	"time"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/model"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// ChatClient 测活对插件聊天能力的最小依赖（*plugmgr.Manager 实现；测试用假件注入）。
type ChatClient interface {
	Chat(req *pb.ChatRequest, pluginName string, cred *pb.CredentialBlob) (chan *pb.StreamEvent, error)
}

// probeTarget 一个探测目标（渠道 × 模型）。
type probeTarget struct {
	AccountID   int64  `json:"account_id"`
	Account     string `json:"account"`
	PluginID    int64  `json:"plugin_id"`
	Plugin      string `json:"plugin"`
	PluginLabel string `json:"plugin_label"`
	Instance    string `json:"instance"`
	Model       string `json:"model"`
}

// probeResult 单目标探测结果（NDJSON result 事件体）。
type probeResult struct {
	OK           bool    `json:"ok"`
	ConnectMs    int64   `json:"connect_ms"`
	FirstTokenMs int64   `json:"first_token_ms"`
	TotalMs      int64   `json:"total_ms"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	OutputTokens int     `json:"output_tokens"`
	Attempts     int     `json:"attempts"`
	Error        string  `json:"error,omitempty"`
}

// 控制变量：探测请求统一参数（见文件头）。
const (
	probePrompt    = "ping"
	probeMaxTokens = 16
	// probeCharsPerToken 出字速度估算系数（上游未回 usage 时；与网关 token 粗估同源）。
	probeCharsPerToken = 3.5
	// probeMaxRetries 失败后退避重试上限（总尝试 = 1 + probeMaxRetries）。
	probeMaxRetries = 3
)

// probeBackoff 退避序列（指数：1s/2s/4s）。
func probeBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && i < probeMaxRetries; i++ {
		d *= 2
	}
	return d
}

// probeMu 一键测活互斥（同一时刻至多一轮探测）。
var probeMu sync.Mutex

// 可注入测活参数（生产值：probeBackoff 1s/2s/4s + settings 首帧超时；测试注入零退避 /
// 亚秒超时）。
var (
	probeBackoffFn          = probeBackoff
	probeFirstEventTimeoutF = func(s *Server) time.Duration { return s.settings.FirstEventTimeout() }
)

// chatClient 测活用的插件聊天客户端（probeChat 测试注入优先；生产走 plugmgr.Manager）。
func (s *Server) chatClient() ChatClient {
	if s.probeChat != nil {
		return s.probeChat
	}
	return s.plugins
}

// runProbe POST /admin/probe/run — 一键测活（NDJSON 进度流）。
func (s *Server) runProbe(w http.ResponseWriter, r *http.Request) {
	if !probeMu.TryLock() {
		http.Error(w, `{"error":"已有测活任务在执行，请稍后再试"}`, http.StatusConflict)
		return
	}
	defer probeMu.Unlock()

	conc := 4
	if n := int(parseInt(r.URL.Query().Get("concurrency"))); n >= 1 && n <= 8 {
		conc = n
	}
	var acctFilter int64
	if v := parseInt(r.URL.Query().Get("account_id")); v > 0 {
		acctFilter = v
	}

	targets := s.collectProbeTargets(r.Context(), acctFilter)
	pw := newProgressWriter(w)
	pw.send(map[string]interface{}{"total": len(targets), "concurrency": conc})

	events := make(chan map[string]interface{}, 64)
	done := make(chan struct{})
	go func() { // 单写者：progressWriter 非并发安全，探测结果经 channel 汇聚后顺序写出
		defer close(done)
		for ev := range events {
			pw.send(ev)
		}
	}()

	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	var cntMu sync.Mutex
	okCount, failCount := 0, 0
	for i := range targets {
		t := targets[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Context().Err() != nil {
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			events <- map[string]interface{}{"target": t, "phase": "start"}
			res := s.probeOne(r.Context(), t, func(attempt int, err string) {
				events <- map[string]interface{}{"target": t, "phase": "retry",
					"attempt": attempt + 1, "error": err}
			})
			payload := map[string]interface{}{"target": t, "phase": "result"}
			for k, v := range map[string]interface{}{
				"ok": res.OK, "connect_ms": res.ConnectMs, "first_token_ms": res.FirstTokenMs,
				"total_ms": res.TotalMs, "tokens_per_sec": res.TokensPerSec,
				"output_tokens": res.OutputTokens, "attempts": res.Attempts,
			} {
				payload[k] = v
			}
			if res.Error != "" {
				payload["error"] = res.Error
			}
			events <- payload
			cntMu.Lock()
			if res.OK {
				okCount++
			} else {
				failCount++
			}
			cntMu.Unlock()
		}()
	}
	wg.Wait()
	close(events)
	<-done // 进度流写完后才发收尾（progressWriter 单写者约定）
	pw.send(map[string]interface{}{"done": true, "total": len(targets), "ok": okCount, "failed": failCount})
	logsink.Printf("[probe] 一键测活完成: %d 目标，存活 %d，失败 %d", len(targets), okCount, failCount)
}

// collectProbeTargets 收集探测目标：全部可调度账号（active+disabled；expired 凭据已死
// 不测）× 其模型目录。模型目录缺失的账号发一个空模型目标，探测时回退插件聚合目录。
func (s *Server) collectProbeTargets(ctx context.Context, acctFilter int64) []probeTarget {
	var accts []model.Account
	q := s.db.Where("status IN ?", []string{"active", "disabled"}).Order("id")
	if acctFilter > 0 {
		q = q.Where("id = ?", acctFilter)
	}
	if err := q.Find(&accts).Error; err != nil {
		return nil
	}
	var out []probeTarget
	for _, a := range accts {
		pluginName := pluginNameByID(s.db, a.PluginID)
		if pluginName == "" {
			continue
		}
		t := probeTarget{AccountID: a.ID, Account: a.DisplayName, PluginID: a.PluginID,
			Plugin: pluginName, PluginLabel: s.pluginBrandByID(a.PluginID),
			Instance: instanceNameByID(s.db, a.InstanceID)}
		models := s.accounts.StoredModels(a.ID)
		if len(models) == 0 {
			out = append(out, t) // 空模型目录：worker 内回退插件聚合目录
			continue
		}
		for _, m := range models {
			if id := m.GetId(); id != "" {
				tm := t
				tm.Model = id
				out = append(out, tm)
			}
		}
	}
	return out
}

// probeOne 单目标探测：至多 1+probeMaxRetries 次尝试，指数退避；onRetry 播报重试。
func (s *Server) probeOne(ctx context.Context, t probeTarget, onRetry func(attempt int, err string)) probeResult {
	var last probeResult
	last.Error = "not attempted"
	for attempt := 0; attempt <= probeMaxRetries; attempt++ {
		if ctx.Err() != nil {
			last.Error = "已取消"
			last.Attempts = attempt
			return last
		}
		if attempt > 0 {
			if onRetry != nil {
				onRetry(attempt, last.Error)
			}
			select {
			case <-time.After(probeBackoffFn(attempt)):
			case <-ctx.Done():
				last.Error = "已取消"
				last.Attempts = attempt
				return last
			}
		}
		res, err := s.probeAttempt(ctx, t)
		if err == nil {
			res.Attempts = attempt + 1
			return res
		}
		last = res
		last.OK = false
		last.Error = err.Error()
		last.Attempts = attempt + 1
	}
	return last
}

// probeAttempt 一次探测尝试（控制变量请求形态 + 三项指标测量）。
func (s *Server) probeAttempt(ctx context.Context, t probeTarget) (probeResult, error) {
	var acct model.Account
	if err := s.db.First(&acct, t.AccountID).Error; err != nil {
		return probeResult{}, fmt.Errorf("账号不存在: %d", t.AccountID)
	}
	modelID := t.Model
	if modelID == "" {
		// 模型目录缺失回退：插件聚合目录里该插件的第一个模型
		for mid, pn := range s.plugins.Models() {
			if pn == t.Plugin && (modelID == "" || mid < modelID) {
				modelID = mid
			}
		}
		if modelID == "" {
			return probeResult{}, fmt.Errorf("账号无模型目录（请在账号页同步模型后重试）")
		}
	}
	cred := account.BuildCred(s.db, s.dataDir, &acct, 0)
	req := &pb.ChatRequest{
		Model: modelID, Stream: true, Source: "chat_completions",
		Temperature: 0, MaxTokens: probeMaxTokens,
		Messages:   []*pb.EnvelopeMessage{{Role: "user", Text: probePrompt}},
		Credential: cred,
	}
	start := time.Now()
	events, err := s.chatClient().Chat(req, t.Plugin, cred)
	if err != nil {
		return probeResult{}, err
	}

	res := probeResult{}
	var chars int64
	firstTokenAt := time.Time{}
	feTimeout := probeFirstEventTimeoutF(s)
	// speedAt 生成段耗时用原始时钟差计算（毫秒整数截断会把亚毫秒生成窗算成 0）
	speedAt := func(tokens int, at time.Time) float64 {
		if tokens <= 0 || firstTokenAt.IsZero() {
			return 0
		}
		gen := at.Sub(firstTokenAt)
		if gen <= 0 {
			return 0
		}
		return float64(tokens) / gen.Seconds()
	}
	for {
		var ev *pb.StreamEvent
		select {
		case e, ok := <-events:
			if !ok {
				// 通道关闭：有内容且无失败即视为成功（部分插件不发 MessageFinish）
				fin := time.Now()
				res.TotalMs = fin.Sub(start).Milliseconds()
				res.OutputTokens = int(tokenCount(chars, res.OutputTokens))
				if !res.OK {
					res.OK = chars > 0 || res.OutputTokens > 0
				}
				if res.ConnectMs == 0 {
					return probeResult{}, fmt.Errorf("上游无响应")
				}
				if res.FirstTokenMs == 0 {
					res.FirstTokenMs = res.TotalMs
				}
				res.TokensPerSec = speedAt(res.OutputTokens, fin)
				return res, nil
			}
			ev = e
		case <-time.After(feTimeout):
			if res.ConnectMs > 0 {
				res.ConnectMs = time.Since(start).Milliseconds()
			}
			return probeResult{ConnectMs: res.ConnectMs}, fmt.Errorf("等待上游事件超时（>%s）", feTimeout)
		case <-ctx.Done():
			return probeResult{ConnectMs: res.ConnectMs}, fmt.Errorf("已取消")
		}
		if res.ConnectMs == 0 {
			res.ConnectMs = time.Since(start).Milliseconds() // 连接时间：首个事件（任意帧）
		}
		switch e := ev.Event.(type) {
		case *pb.StreamEvent_ContentDelta:
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
				res.FirstTokenMs = firstTokenAt.Sub(start).Milliseconds()
			}
			chars += int64(len(e.ContentDelta.Text))
		case *pb.StreamEvent_ReasoningDelta:
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
				res.FirstTokenMs = firstTokenAt.Sub(start).Milliseconds()
			}
			chars += int64(len(e.ReasoningDelta.Text))
		case *pb.StreamEvent_MessageFinish:
			fin := time.Now()
			res.TotalMs = fin.Sub(start).Milliseconds()
			if u := e.MessageFinish.Usage; u != nil && u.OutputTokens > 0 {
				res.OutputTokens = int(u.OutputTokens)
			} else {
				res.OutputTokens = int(tokenCount(chars, 0))
			}
			res.OK = true
			res.TokensPerSec = speedAt(res.OutputTokens, fin)
			return res, nil
		case *pb.StreamEvent_TaskFailed:
			msg := ""
			if e.TaskFailed != nil && e.TaskFailed.Error != nil {
				msg = e.TaskFailed.Error.Message
			}
			return probeResult{ConnectMs: res.ConnectMs, FirstTokenMs: res.FirstTokenMs}, fmt.Errorf("%s", msg)
		}
	}
}

// tokenCount 上游未回 usage 时按字符数估算（与网关 bytes/token 粗估同系数）。
func tokenCount(chars int64, reported int) int64 {
	if reported > 0 {
		return int64(reported)
	}
	return int64(float64(chars) / probeCharsPerToken)
}

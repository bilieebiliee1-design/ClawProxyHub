// probe_test.go — 一键测活回归（v1.3.0 方案 ③）：指标测量（连接/首字/出字速度）、
// 3 次退避重试、超时判定、互斥。
package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// fakeChat 假插件聊天客户端：按模型名配置失败次数 / 无响应次数，成功路径回放
// MessageStart → ContentDelta → MessageFinish(usage) 的最小事件流。
type fakeChat struct {
	mu      sync.Mutex
	failFor map[string]int // 剩余 TaskFailed 次数
	hangFor map[string]int // 剩余「挂死不发事件」次数（channel 保持打开 → 触发首帧超时）
	connect time.Duration  // 成功路径的首事件延迟
	gen     time.Duration  // 首事件 → 首内容延迟
}

func (f *fakeChat) Chat(req *pb.ChatRequest, pluginName string, cred *pb.CredentialBlob) (chan *pb.StreamEvent, error) {
	ch := make(chan *pb.StreamEvent, 8)
	m := req.Model
	f.mu.Lock()
	if f.hangFor[m] > 0 {
		f.hangFor[m]--
		f.mu.Unlock()
		go func() { time.Sleep(2 * time.Second); close(ch) }() // 挂死：不发任何事件
		return ch, nil
	}
	if f.failFor[m] > 0 {
		f.failFor[m]--
		f.mu.Unlock()
		ch <- &pb.StreamEvent{Event: &pb.StreamEvent_TaskFailed{TaskFailed: &pb.TaskFailed{
			Error: &pb.Error{Code: 502, Message: "boom"}}}}
		close(ch)
		return ch, nil
	}
	f.mu.Unlock()
	connect, gen := f.connect, f.gen
	go func() {
		defer close(ch)
		time.Sleep(connect)
		ch <- &pb.StreamEvent{Event: &pb.StreamEvent_MessageStart{MessageStart: &pb.MessageStart{}}}
		time.Sleep(gen)
		ch <- &pb.StreamEvent{Event: &pb.StreamEvent_ContentDelta{ContentDelta: &pb.ContentDelta{Text: "hello你好，测活"}}}
		time.Sleep(12 * time.Millisecond) // 生成段窗口（Windows 时钟粒度 ~15ms，需可测量）
		ch <- &pb.StreamEvent{Event: &pb.StreamEvent_MessageFinish{MessageFinish: &pb.MessageFinish{
			FinishReason: "stop", Usage: &pb.Usage{OutputTokens: 8}}}}
	}()
	return ch, nil
}

// seedProbe 测活夹具：注入假客户端 + 亚秒超时 / 零退避（结束还原）。
func seedProbe(t *testing.T) (*Server, context.Context) {
	t.Helper()
	s, _ := seedAutocfg(t)
	oldBackoff, oldTimeout := probeBackoffFn, probeFirstEventTimeoutF
	probeBackoffFn = func(int) time.Duration { return time.Millisecond }
	probeFirstEventTimeoutF = func(*Server) time.Duration { return 150 * time.Millisecond }
	t.Cleanup(func() { probeBackoffFn, probeFirstEventTimeoutF = oldBackoff, oldTimeout })
	return s, context.Background()
}

// TestProbeMetrics 成功路径三项指标：连接时间 / 首字时间 / 出字速度。
func TestProbeMetrics(t *testing.T) {
	s, ctx := seedProbe(t)
	fc := &fakeChat{failFor: map[string]int{}, hangFor: map[string]int{},
		connect: 12 * time.Millisecond, gen: 25 * time.Millisecond}
	s.probeChat = fc
	addAccount(t, s, "pok", `[{"id":"ok-model"}]`)

	targets := s.collectProbeTargets(ctx, 0)
	if len(targets) != 1 || targets[0].Model != "ok-model" {
		t.Fatalf("targets = %+v", targets)
	}
	res := s.probeOne(ctx, targets[0], nil)
	if !res.OK || res.Attempts != 1 {
		t.Fatalf("result = %+v, want ok attempts=1", res)
	}
	if res.ConnectMs < 8 {
		t.Fatalf("connect_ms = %d, want >=8 (12ms 首事件延迟)", res.ConnectMs)
	}
	if res.FirstTokenMs < res.ConnectMs+15 {
		t.Fatalf("first_token_ms = %d, want >= connect+15", res.FirstTokenMs)
	}
	if res.OutputTokens != 8 {
		t.Fatalf("output_tokens = %d, want 8（usage 上报优先）", res.OutputTokens)
	}
	if res.TokensPerSec <= 0 {
		t.Fatalf("tokens_per_sec = %v, want > 0", res.TokensPerSec)
	}
}

// TestProbeRetryBackoff 失败退避重试：第 3 次成功（attempts=3，重试事件 2 次）；
// 持续失败至多 3 次重试（总尝试 4）后判死。
func TestProbeRetryBackoff(t *testing.T) {
	s, ctx := seedProbe(t)
	fc := &fakeChat{failFor: map[string]int{"r-model": 2, "f-model": 99}, hangFor: map[string]int{}}
	s.probeChat = fc
	addAccount(t, s, "pr", `[{"id":"r-model"},{"id":"f-model"}]`)
	targets := s.collectProbeTargets(ctx, 0)

	var retries int
	for _, tgt := range targets {
		res := s.probeOne(ctx, tgt, func(attempt int, err string) { retries++ })
		switch tgt.Model {
		case "r-model":
			if !res.OK || res.Attempts != 3 {
				t.Fatalf("r-model = %+v, want ok attempts=3", res)
			}
		case "f-model":
			if res.OK || res.Attempts != 4 || !strings.Contains(res.Error, "boom") {
				t.Fatalf("f-model = %+v, want fail attempts=4 error=boom", res)
			}
		}
	}
	if retries != 2+3 {
		t.Fatalf("retry events = %d, want 5（r-model 2 次 + f-model 3 次）", retries)
	}
}

// TestProbeTimeout 无响应渠道：首帧超时判失败并走满重试。
func TestProbeTimeout(t *testing.T) {
	s, ctx := seedProbe(t)
	fc := &fakeChat{failFor: map[string]int{}, hangFor: map[string]int{"s-model": 99}}
	s.probeChat = fc
	addAccount(t, s, "pt", `[{"id":"s-model"}]`)
	targets := s.collectProbeTargets(ctx, 0)
	res := s.probeOne(ctx, targets[0], nil)
	if res.OK || res.Attempts != 4 || !strings.Contains(res.Error, "超时") {
		t.Fatalf("result = %+v, want timeout-fail attempts=4", res)
	}
}

// TestProbeRunAPI runProbe 端到端：NDJSON 事件流（start→result→done）+ 互斥 409。
func TestProbeRunAPI(t *testing.T) {
	s, _ := seedProbe(t)
	fc := &fakeChat{failFor: map[string]int{}, hangFor: map[string]int{},
		connect: time.Millisecond, gen: time.Millisecond}
	s.probeChat = fc
	addAccount(t, s, "papi", `[{"id":"m1"},{"id":"m2"}]`)

	req := httptest.NewRequest(http.MethodPost, "/admin/probe/run", nil)
	rec := httptest.NewRecorder()
	s.runProbe(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"phase":"start"`, `"phase":"result"`, `"ok":true`, `"done":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("NDJSON missing %s:\n%s", want, body)
		}
	}

	// 互斥：持锁时再触发 → 409
	probeMu.Lock()
	defer probeMu.Unlock()
	req2 := httptest.NewRequest(http.MethodPost, "/admin/probe/run", nil)
	rec2 := httptest.NewRecorder()
	s.runProbe(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second run = %d, want 409", rec2.Code)
	}
}

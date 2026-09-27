// runlog_test.go — 隧道生命周期 → run-logs 埋点回归（方案 ③，白盒）。
package tunnel

import "testing"

// stubRunLogger 捕获 runlog 写入（runlog.Logger 落库依赖 sqlite/CGO，测试用桩）。
type stubRunLogger struct {
	events  []string // "level|action|message"
	modules []string
}

func (s *stubRunLogger) Log(level, module, action, message, detail string, accountID *int64) {
	s.modules = append(s.modules, module)
	s.events = append(s.events, level+"|"+action+"|"+message)
}

// TestSetStateLockedRunLogEvents 状态迁移写 run-logs：start/active/error/stop，
// 且 idle→idle 不重复记（双 Stop 不刷屏）。
func TestSetStateLockedRunLogEvents(t *testing.T) {
	stub := &stubRunLogger{}
	m := New(40001)
	m.SetRunLogger(stub)

	m.mu.Lock()
	m.setStateLocked(StateStarting, "")
	m.setStateLocked(StateActive, "")
	m.mu.Unlock()
	m.mu.Lock()
	m.url = "https://x.trycloudflare.com" // active 事件此前已写（url 为空时的形态）
	m.mu.Unlock()

	m.mu.Lock()
	m.setStateLocked(StateError, "隧道连接断开: signal: killed")
	m.setStateLocked(StateIdle, "")
	m.setStateLocked(StateIdle, "") // 双重 Stop
	m.mu.Unlock()

	want := []string{
		"info|start|隧道启动中（cloudflared 临时隧道）",
		"info|active|隧道已建立: ",
		"error|error|隧道连接断开: signal: killed",
		"info|stop|隧道已停止",
	}
	if len(stub.events) != len(want) {
		t.Fatalf("事件数 = %d (%v), want %d", len(stub.events), stub.events, len(want))
	}
	for i, w := range want {
		if stub.events[i] != w {
			t.Fatalf("事件[%d] = %q, want %q", i, stub.events[i], w)
		}
		if stub.modules[i] != "tunnel" {
			t.Fatalf("事件[%d] module = %q, want tunnel", i, stub.modules[i])
		}
	}
}

// TestSetStateLockedNilRunLogger 未注入 runlog 时状态迁移不 panic（bridge 直用场景）。
func TestSetStateLockedNilRunLogger(t *testing.T) {
	m := New(40002)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setStateLocked(StateStarting, "")
	m.setStateLocked(StateError, "boom")
	if m.state != StateError {
		t.Fatalf("state = %q", m.state)
	}
}

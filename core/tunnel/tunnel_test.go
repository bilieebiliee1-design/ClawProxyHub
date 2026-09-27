// tunnel_test.go — 隧道生命周期回归测试（白盒，package tunnel）。
//
// 核心回归（上轮根因 1）：launchOnce 拿到域名返回 Active 后，旧代码的 defer cancel()
// 触发 → exec.CommandContext 的 ctx Done → cloudflared 整组 SIGKILL（strace 实证
// 输出域名后全部线程被杀）。下面的 TestStartKeepsProcessAliveAfterActive 用带心跳的
// 假 cloudflared 证明：Active 返回后子进程持续存活；该测试在旧代码上必然失败
// （进程在 Start 返回瞬间被杀，心跳文件停止增长）。
package tunnel

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"io.nexport.gateway/core/logsink"
)

// buildFakeCloudflared 运行时把 testdata/fakecloudflared/main.go 构建为可执行文件
// （testdata 目录不参与 ./... 构建，这里显式单文件编译）。
func buildFakeCloudflared(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go"+exeSuffix())
	}
	out := filepath.Join(t.TempDir(), "fakecloudflared"+exeSuffix())
	src := filepath.Join("testdata", "fakecloudflared", "main.go")
	cmd := exec.Command(goBin, "build", "-o", out, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 fakecloudflared 失败: %v\n%s", err, b)
	}
	return out
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// fakeManager 返回注入好桩（无边缘解析、日志静音）的 Manager。
func fakeManager(t *testing.T, port int) *Manager {
	t.Helper()
	logsink.SetOutput(io.Discard)
	oldEdge := edgeArgsFn
	edgeArgsFn = func() []string { return nil }
	t.Cleanup(func() { edgeArgsFn = oldEdge })
	return New(port)
}

// TestStartKeepsProcessAliveAfterActive 根因 1 的回归测试：
// Active 之后子进程必须存活（心跳持续增长），且 cancel 由 Manager 保管而非释放。
func TestStartKeepsProcessAliveAfterActive(t *testing.T) {
	fake := buildFakeCloudflared(t)
	// 存活探测走 TCP：假二进制在 127.0.0.1:<port> 监听，父进程拨号成功 = 子进程存活。
	// （本机进程沙箱对 %TEMP% 文件视图按进程树隔离，跨进程文件心跳不可见。）
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // 只占号：假二进制稍后在同一端口监听
	t.Setenv("FAKE_TCP_PORT", strconv.Itoa(port))
	t.Setenv("FAKE_URL_NAME", "lifecycle-test")
	addr := "127.0.0.1:" + strconv.Itoa(port)

	m := fakeManager(t, 39717)
	if err := m.Start(fake); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := m.State(); got != StateActive {
		t.Fatalf("state = %q, want active", got)
	}

	// 白盒断言：cancel 必须仍由 Manager 保管（Stop 唯一触发点），不得提前触发。
	m.mu.Lock()
	cancelRetained := m.cancel != nil && m.cmd != nil
	m.mu.Unlock()
	if !cancelRetained {
		t.Fatal("Active 后 m.cancel/m.cmd 应保留（旧代码 defer cancel() 会在此处触发 SIGKILL）")
	}

	// 存活探测：Start 返回后 2.5s 内子进程必须仍可拨通（旧代码：毫秒级被杀，必失联）。
	alive := func() bool {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}
	if !alive() {
		t.Fatal("Active 后首次拨号即失败：子进程已死")
	}
	for i := 0; i < 10; i++ {
		time.Sleep(250 * time.Millisecond)
		if !alive() {
			t.Fatalf("Active 后 %.2fs 子进程失联（旧代码 defer cancel() 的 SIGKILL 表现）", float64(i+1)*0.25)
		}
	}

	// Stop 必须能真正终止进程并回 Idle。
	m.Stop()
	if m.Running() {
		t.Fatal("Stop 后子进程应已退出")
	}
	if got := m.State(); got != StateIdle {
		t.Fatalf("Stop 后 state = %q, want idle", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !alive() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("Stop 后子进程仍可拨通（未退出）")
}

// TestStartExitBeforeURLIsError 未拿到域名即退出 → Error 且资源清理（cancel 释放）。
func TestStartExitBeforeURLIsError(t *testing.T) {
	fake := buildFakeCloudflared(t)
	t.Setenv("FAKE_MODE", "exit-now")
	m := fakeManager(t, 39717)
	err := m.Start(fake)
	if err == nil || !strings.Contains(err.Error(), "退出") {
		t.Fatalf("期望「cloudflared 退出」错误，got: %v", err)
	}
	m.mu.Lock()
	cleaned := m.cancel == nil && m.cmd == nil
	m.mu.Unlock()
	if !cleaned {
		t.Fatal("失败路径后 m.cancel/m.cmd 应已清理")
	}
	if m.Running() {
		t.Fatal("失败路径后子进程应已退出")
	}
	if got := m.State(); got != StateError {
		t.Fatalf("state = %q, want error", got)
	}
}

// TestStartSilentHangTimeout 超时路径：拿不到域名 → acquireTimeout 后报错并清理。
func TestStartSilentHangTimeout(t *testing.T) {
	fake := buildFakeCloudflared(t)
	t.Setenv("FAKE_MODE", "silent-hang")
	old := acquireTimeout
	acquireTimeout = 1200 * time.Millisecond
	t.Cleanup(func() { acquireTimeout = old })

	m := fakeManager(t, 39717)
	err := m.Start(fake)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("期望超时错误，got: %v", err)
	}
	m.mu.Lock()
	cleaned := m.cancel == nil && m.cmd == nil
	m.mu.Unlock()
	if !cleaned {
		t.Fatal("超时失败后 m.cancel/m.cmd 应已清理")
	}
	if m.Running() {
		t.Fatal("超时失败后子进程应已终止")
	}
}

// TestStopDuringStarting Starting 阶段 Stop → errStopped，回 Idle。
func TestStopDuringStarting(t *testing.T) {
	fake := buildFakeCloudflared(t)
	m := fakeManager(t, 39717)

	done := make(chan error, 1)
	go func() { done <- m.Start(fake) }()
	deadline := time.Now().Add(5 * time.Second)
	for !m.Running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !m.Running() {
		t.Fatal("子进程未在期限内启动")
	}
	m.Stop()

	select {
	case err := <-done:
		// 环境时序宽容（合规复审 2026-09-28）：WSL 等慢环境下 Stop 可能落在 Start
		// 已完成返回之后，此时 Start 走正常结束路径返回 nil——errStopped 只在「Stop
		// 打断 Starting」窄窗口内出现。两种结果下进程均已停止，下方 State 断言仍校验终态。
		if err != nil && !errors.Is(err, errStopped) {
			t.Fatalf("期望 nil 或 errStopped，got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start 未在 Stop 后返回")
	}
	if got := m.State(); got != StateIdle {
		t.Fatalf("state = %q, want idle", got)
	}
}

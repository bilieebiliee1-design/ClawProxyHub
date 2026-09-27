// tunnel.go — cloudflared 临时隧道生命周期管理（NexPort 移植新增）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。原项目无隧道功能（全仓 grep 无命中），此包为安卓端新增：
//
//   - 目标固定指向核心 fork 的第二监听器（仅 /v1/* + /health，127.0.0.1，拓扑隔离，
//     /admin 与面板不可达）；
//   - TryCloudflare 临时隧道：`cloudflared tunnel --no-autoupdate --url http://127.0.0.1:<port>`，
//     从输出解析 trycloudflare.com 域名回报；
//   - 边缘地址预解析（edge.go）：本进程做 SRV/A 原始 DNS 查询后以 --edge 传给子进程，
//     绕开安卓上 Go 内建 resolver 无 /etc/resolv.conf → [::1]:53 refused 的死路；
//   - QUIC/UDP 被运营商拦截时自动加 --protocol http2 重试一次；
//   - 状态机 Idle → Starting → Active(URL) → Error → Idle；事件以 JSON 字符串经 Sink 回调
//     （规避 gomobile bind 的类型限制，由 bridge 层转发 Kotlin）。
//
// 二进制要求：安卓侧必须使用按 CGO 自建的 libcloudflared.so（动态链接 bionic，
// DNS 走 getaddrinfo）。官方静态发布版在安卓上纯 Go 解析器无法解析任何主机名，不可用。
package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"io.nexport.gateway/core/logsink"
)

// State 隧道状态机取值。
const (
	StateIdle     = "idle"
	StateStarting = "starting"
	StateActive   = "active"
	StateError    = "error"
)

// acquireTimeout 等待临时域名颁发的上限（QUIC 协商失败时进程自行退出，通常远快于此）。
// 用 var 是为测试可注入（假 cloudflared 用短超时验证超时路径）。
var acquireTimeout = 90 * time.Second

// urlRe TryCloudflare 输出中的临时域名。
var urlRe = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// Event 推送给宿主（Kotlin）的隧道事件（JSON 序列化后的字符串）。
type Event struct {
	State   string `json:"state"`             // idle/starting/active/error
	URL     string `json:"url,omitempty"`     // Active 时的公网临时域名
	Message string `json:"message,omitempty"` // 人类可读说明（错误原因等）
}

// Sink 隧道事件回调（bridge 注册；JSON 字符串规避 bind 类型限制）。
type Sink interface {
	OnTunnelEvent(json string)
}

// RunLogger 隧道生命周期运行日志面（*runlog.Logger 实现；接口化便于无 DB 单测）。
type RunLogger interface {
	Log(level, module, action, message, detail string, accountID *int64)
}

// Manager 一个核心实例的隧道管理器（同一时刻至多一条隧道）。
type Manager struct {
	mu     sync.Mutex
	port   int // 第二监听器端口（隧道目标）
	bin    string
	cmd    *exec.Cmd
	cancel context.CancelFunc // 取消 = 请求停止当前子进程
	state  string
	url    string
	errMsg string
	sink   Sink
	runLog RunLogger     // 运行日志（生命周期事件落 run-logs；nil = 不记）
	stopCh chan struct{} // Stop() 通知（关闭式）
}

// New 创建隧道管理器。port 为核心第二监听器（仅 /v1 + /health）端口。
func New(port int) *Manager {
	return &Manager{port: port, state: StateIdle}
}

// SetSink 注册事件回调（nil 清除）。建议在 Start 前调用。
func (m *Manager) SetSink(s Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = s
}

// SetRunLogger 注册运行日志写入器（核心启动时注入；nil 清除）。
// 生命周期事件（启动中 / 已建立 / 断开与错误 / 停止）写入 run-logs，
// 运行日志页因此能看到隧道全生命周期（含 phantom process reaper 杀死子进程的
// 断连记录——awaitExit 的健壮性修复落库可查）。
func (m *Manager) SetRunLogger(l RunLogger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runLog = l
}

// State 当前状态（idle/starting/active/error）。
func (m *Manager) State() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// URL 当前隧道域名（未建立时为空）。
func (m *Manager) URL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.url
}

// ErrMsg 最近一次错误说明（空为无错误）。
func (m *Manager) ErrMsg() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errMsg
}

// Running 隧道子进程是否存活。
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// Start 拉起 cloudflared 临时隧道并阻塞至拿到临时域名（Active）、失败或被 Stop。
// binPath 为可执行文件路径（安卓侧传 nativeLibraryDir/libcloudflared.so）。
// 默认 QUIC；失败时按 --protocol http2 重试一次（运营商拦截 UDP 的常见回退）。
func (m *Manager) Start(binPath string) error {
	m.mu.Lock()
	switch {
	case binPath == "":
		m.mu.Unlock()
		return errors.New("cloudflared 路径为空")
	case m.cmd != nil:
		m.mu.Unlock()
		return errors.New("隧道已在运行")
	}
	m.bin = binPath
	m.url, m.errMsg = "", ""
	stopCh := make(chan struct{})
	m.stopCh = stopCh
	m.setStateLocked(StateStarting, "")
	m.mu.Unlock()

	// 边缘地址预解析一次（两次尝试共用；安卓上 SRV 不能交给 cloudflared 自己查，见 edge.go）
	edge := edgeArgsFn()

	var lastErr error
	for attempt, http2 := 0, false; attempt < 2; attempt, http2 = attempt+1, true {
		err := m.launchOnce(http2, edge, stopCh)
		if err == nil {
			return nil // Active
		}
		if errors.Is(err, errStopped) { // 主动 Stop：不再重试
			return err
		}
		lastErr = err
		if attempt == 0 {
			logsink.Warnf("[tunnel] 启动失败（%v），改用 --protocol http2 重试一次", err)
		}
	}
	m.mu.Lock()
	m.errMsg = lastErr.Error()
	m.setStateLocked(StateError, m.errMsg)
	m.mu.Unlock()
	return lastErr
}

var errStopped = errors.New("tunnel stopped")

// edgeArgsFn 边缘参数注入点：默认 edgeArgs（触网解析）；测试替换为桩以隔离网络。
var edgeArgsFn = edgeArgs

// launchOnce 单次拉起：阻塞至拿到域名（nil）/ 进程退出 / 超时 / Stop。
//
// 生命周期要点（修复上轮根因）：ctx 的 cancel 绝不能在拿到域名后触发——
// exec.CommandContext 的 ctx 一旦 Done 会对子进程整组 SIGKILL，导致隧道 5-7 秒必挂。
// 因此这里不做 defer cancel()：
//   - 成功（Active）：cancel 交给 m.cancel 保管，仅 Stop() 会触发；
//   - 其余路径（启动失败/进程退出/超时/Stop）：本函数显式 cancel 释放资源。
func (m *Manager) launchOnce(http2 bool, edge []string, stopCh chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"tunnel"}
	args = append(args, edge...)
	args = append(args, "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", m.port))
	if http2 {
		args = append(args, "--protocol", "http2")
	}
	cmd := exec.CommandContext(ctx, m.bin, args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	m.mu.Lock()
	m.cmd, m.cancel = cmd, cancel
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		cancel() // 启动失败：立即释放（exec 的 watchCtx 尚未接管也安全）
		m.mu.Lock()
		m.cmd, m.cancel = nil, nil
		m.mu.Unlock()
		pw.Close()
		return fmt.Errorf("启动 cloudflared: %w", err)
	}

	// 输出泵：逐行经 logsink 出口，并抽取 trycloudflare 域名
	urlCh := make(chan string, 1)
	go func() {
		defer pw.Close()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			logsink.Printf("[tunnel] %s", line)
			if u := urlRe.FindString(line); u != "" {
				select {
				case urlCh <- u:
				default:
				}
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var result error
	select {
	case u := <-urlCh:
		m.mu.Lock()
		m.url = u
		m.setStateLocked(StateActive, "")
		m.mu.Unlock()
		logsink.Printf("[tunnel] 临时域名就绪: %s", u)
		// 后台继续吃输出直至进程退出（断连时落 Error）。
		// 注意：此处绝不 cancel——ctx 存活是子进程存活的唯一保障。
		go m.awaitExit(cmd, waitCh, cancel, stopCh)
		return nil
	case err := <-waitCh:
		result = fmt.Errorf("cloudflared 退出（未获取到临时域名）: %v", err)
	case <-time.After(acquireTimeout):
		result = fmt.Errorf("等待临时域名超时（%s）", acquireTimeout)
		_ = cmd.Process.Kill()
		<-waitCh
	case <-stopCh:
		result = errStopped
		_ = cmd.Process.Kill()
		<-waitCh
	}
	// 失败路径收尾：进程已死或已杀，cancel 释放 ctx 与 exec 的 watchCtx 协程
	cancel()
	m.mu.Lock()
	m.cmd, m.cancel = nil, nil
	m.mu.Unlock()
	return result
}

// awaitExit Active 之后的收尾监视：进程退出（非 Stop 触发）时落 Error 并回报。
// 两条出口都必须 cancel（进程已死，cancel 只释放资源，不会再误伤任何进程）。
func (m *Manager) awaitExit(cmd *exec.Cmd, waitCh chan error, cancel context.CancelFunc, stopCh chan struct{}) {
	var err error
	select {
	case err = <-waitCh:
	case <-stopCh:
		_ = cmd.Process.Kill()
		<-waitCh
		cancel()
		return
	}
	cancel()
	select {
	case <-stopCh:
		return // Stop 已接管状态
	default:
	}
	m.mu.Lock()
	m.cmd, m.cancel = nil, nil
	m.url = ""
	m.errMsg = fmt.Sprintf("隧道连接断开: %v", err)
	msg := m.errMsg
	m.setStateLocked(StateError, msg)
	m.mu.Unlock()
	logsink.Warnf("[tunnel] %s", msg)
}

// Stop 停止隧道子进程并回 Idle（幂等）。
func (m *Manager) Stop() {
	m.mu.Lock()
	closeOnce := m.stopCh
	cmd, cancel := m.cmd, m.cancel
	m.cmd, m.cancel = nil, nil
	m.url, m.errMsg = "", ""
	m.setStateLocked(StateIdle, "")
	m.mu.Unlock()
	if closeOnce != nil {
		select {
		case <-closeOnce:
		default:
			close(closeOnce)
		}
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
}

// setStateLocked 变更状态并外发事件（须持 m.mu）。
func (m *Manager) setStateLocked(state, msg string) {
	prev := m.state
	m.state = state
	// 运行日志：生命周期事件落 run-logs（同步写——本地 SQLite 单条插入，
	// 且状态迁移本就低频；保证 run-logs 里事件顺序与状态机一致）。
	if m.runLog != nil {
		switch state {
		case StateStarting:
			m.runLog.Log("info", "tunnel", "start", "隧道启动中（cloudflared 临时隧道）", "", nil)
		case StateActive:
			m.runLog.Log("info", "tunnel", "active", "隧道已建立: "+m.url, "", nil)
		case StateError:
			text := msg
			if text == "" {
				text = "隧道错误"
			}
			m.runLog.Log("error", "tunnel", "error", text, "", nil)
		case StateIdle:
			if prev != StateIdle { // 双重 Stop / 未启动过的 Stop 不刷屏
				m.runLog.Log("info", "tunnel", "stop", "隧道已停止", "", nil)
			}
		}
	}
	sink := m.sink
	if sink == nil {
		return
	}
	ev := Event{State: state, URL: m.url, Message: msg}
	if b, err := json.Marshal(ev); err == nil {
		go sink.OnTunnelEvent(string(b)) // 回调在独立协程触发，不阻塞管理器
	}
}

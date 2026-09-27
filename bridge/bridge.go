// Package bridge — NexPort 安卓桥接层（gomobile bind 入口）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。本包是 gomobile bind 的目标包，API 面受
// gobind 类型系统约束：仅 string/int/bool/error + interface 回调；结构体以指针传递。
//
// 线程约束（对接文档，Kotlin 侧必须遵守）：
//   - gobind 生成的 Java→Go 调用是同步阻塞调用：Start（含建库/迁移/插件自启，秒级）
//     必须在工作线程调用，主线程直调必 ANR；
//   - Go→Kotlin 回调（OnLog/OnTunnelEvent）在 Go 侧协程触发：Kotlin 收到后须 post
//     到主线程再更新 UI；
//   - Start 前 TMPDIR 由本层设为 CacheDir（app.Start 内亦兜底），否则 os.CreateTemp
//     在安卓落到不可写的 /data/local/tmp。
package bridge

import (
	"os"
	"sync"
	"time"

	"io.nexport.gateway/core/app"
	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/tunnel"
)

// Config 启动配置（gomobile 结构体指针传参；Kotlin 侧 new 后逐项 set）。
type Config struct {
	// DataDir 数据目录（必填；filesDir 下专用子目录，如 filesDir/core）
	DataDir string
	// CacheDir 缓存目录（cacheDir；Start 前设为 TMPDIR，临时文件基目录）
	CacheDir string
	// LogLevel 日志级别 debug/info/warn/error（空 = info）
	LogLevel string
	// Locale 界面语言（核心暂不消费；保留透传）
	Locale string
	// NativeLibDir applicationInfo.nativeLibraryDir（libluahost.so /
	// libplugin_<name>.so / libcloudflared.so 查找路径）
	NativeLibDir string
	// SecretKeyHex Keystore 解封后的 32 字节密钥（64 个 hex 字符，可空）。
	// 见 core/account/crypto.go 双格式说明：secret.key 为信封格式时必须注入。
	SecretKeyHex string
	// MarketplaceURL 插件市场索引（可空；面板设置优先）
	MarketplaceURL string
	// GatewayPort 网关固定端口（设置页「网关固定端口」预留项；0 = 自动端口，
	// 向后兼容；1-65535 = 127.0.0.1 固定监听，被占用时 Start 报错）。
	// 自动端口（0）v1.3.0 起持久化复用：首启随机并落盘，之后每次启动复用同一端口，
	// 仅端口被占用等绑定失败时重新随机——变更经 PortChangeNotice() 查询后提示用户。
	GatewayPort int
	// TZOffsetSeconds 设备时区偏移（秒，东八区 = 28800；0 = 不干预）。安卓上 Go 运行时
	// 加载不到本地时区（time.Local 回退 UTC），不传则任务执行历史等时间与设备差 8 小时
	//（v1.3.0 方案 ⑤）。Kotlin 侧传 TimeZone.getDefault().getOffset(now)/1000。
	TZOffsetSeconds int64
	// LanIP 宿主检测的局域网 IPv4（可空）。安卓 targetSdk 34+ 对应用 UID 关闭 netlink
	// RTM_GETLINK（net.Interfaces 报 netlinkrib: permission denied），核心自检拿不到
	// 地址；Kotlin 侧用 java.net.NetworkInterface 枚举站点内 IPv4 传入，核心自检失败时
	// 兜底绑定局域网监听（v1.4.0 首页修复③）。
	LanIP string
}

// LogSink 日志回调（bridge 节流后逐批投递；Kotlin 侧 post 主线程）。
type LogSink interface {
	OnLog(line string)
}

// TunnelSink 隧道事件回调（JSON 字符串规避 bind 类型限制）。
// 事件结构：{"state":"idle|starting|active|error","url":"…","message":"…"}
type TunnelSink interface {
	OnTunnelEvent(json string)
}

var (
	mu          sync.Mutex
	lastCfg     *Config
	logSink     LogSink
	tunnelSink  TunnelSink
	logThrottle *logThrottler
)

// Start 启动核心（同步阻塞：建库 + 迁移 + 插件自启，须在工作线程调用）。
// 成功后 GatewayPort/TunnelPort 可查实际端口（一律 127.0.0.1 随机端口）。
func Start(cfg *Config) error {
	mu.Lock()
	defer mu.Unlock()
	if cfg == nil || cfg.DataDir == "" {
		return errConfig("DataDir 必填")
	}
	if app.Running() {
		return errConfig("核心已在运行")
	}
	// TMPDIR 先行（app.Start 内亦兜底设置）
	if cfg.CacheDir != "" {
		_ = os.Setenv("TMPDIR", cfg.CacheDir)
	}
	lastCfg = cfg
	startLogThrottle()
	_, _, err := app.Start(nil, toOptions(cfg))
	return err
}

// Stop 停止核心（幂等；应用退出/备份恢复重启前调用）。
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	return app.Stop()
}

// Restart 重启核心（同配置）：备份恢复闭环——restore/ 换入后调用，重启时生效。
// 同步阻塞，须在工作线程调用。
func Restart() error {
	mu.Lock()
	defer mu.Unlock()
	return app.Restart()
}

// Running 核心是否在运行。
func Running() bool { return app.Running() }

// GatewayPort 网关端口（未运行 = 0）。
func GatewayPort() int { return app.GatewayPort() }

// PortChangeNotice 本次启动的网关端口变更通知（自动端口模式下持久化端口被占用被迫
// 换口时非空；空串 = 无变更）。Start 成功后查询一次并提示用户（v1.3.0 方案 ①）。
func PortChangeNotice() string { return app.PortChangeNotice() }

// LanEndpoint 当前局域网端点（如 http://192.168.1.5:41234；未开启 / 未监听为空串）。
// 局域网侧仅暴露 /v1（强制 API 密钥）与 /health；面板与管理 API 永不暴露（方案 ④）。
func LanEndpoint() string { return app.LanEndpoint() }

// TunnelPort 隧道目标监听器端口（仅 /v1 + /health；未运行 = 0）。
func TunnelPort() int { return app.TunnelPort() }

// Version 版本串（"1.1.0 (core 1.2.2)" 形态）。
func Version() string { return app.Version() }

// StartTunnel 启动 cloudflared 临时隧道（同步阻塞至拿到 trycloudflare 域名或失败）。
// path 传 nativeLibraryDir + "/libcloudflared.so"（CGO 自建版；官方静态版在安卓
// DNS 不可用，勿用）。隧道默认关；UI 侧须二次确认并展示 FGS 通知（Kotlin 职责）。
func StartTunnel(path string) error {
	t := app.Tunnel()
	if t == nil {
		return errConfig("核心未运行")
	}
	t.SetSink(tunnelEventEmitter{})
	return t.Start(path)
}

// StopTunnel 停止隧道（幂等）。
func StopTunnel() {
	if t := app.Tunnel(); t != nil {
		t.Stop()
	}
}

// TunnelRunning 隧道子进程是否存活。
func TunnelRunning() bool {
	t := app.Tunnel()
	return t != nil && t.Running()
}

// TunnelURL 当前临时域名（未建立为空串）。
func TunnelURL() string {
	t := app.Tunnel()
	if t == nil {
		return ""
	}
	return t.URL()
}

// TunnelState 隧道状态：idle/starting/active/error。
func TunnelState() string {
	t := app.Tunnel()
	if t == nil {
		return tunnel.StateIdle
	}
	return t.State()
}

// SetLogCallback 注册日志回调（nil 取消）。日志按批节流投递（~300ms 或 32 行）。
func SetLogCallback(cb LogSink) {
	mu.Lock()
	defer mu.Unlock()
	logSink = cb
	if logThrottle != nil {
		logThrottle.setSink(cb)
	}
}

// SetTunnelCallback 注册隧道事件回调（nil 取消）。
func SetTunnelCallback(cb TunnelSink) {
	mu.Lock()
	defer mu.Unlock()
	tunnelSink = cb
}

// toOptions bridge Config → app Options。
func toOptions(cfg *Config) app.Options {
	return app.Options{
		DataDir:         cfg.DataDir,
		CacheDir:        cfg.CacheDir,
		LogLevel:        cfg.LogLevel,
		Locale:          cfg.Locale,
		SecretKeyHex:    cfg.SecretKeyHex,
		NativeLibDir:    cfg.NativeLibDir,
		MarketplaceURL:  cfg.MarketplaceURL,
		GatewayPort:     cfg.GatewayPort,
		TZOffsetSeconds: cfg.TZOffsetSeconds,
		LanIPHint:       cfg.LanIP,
	}
}

// tunnelEventEmitter 把 tunnel.Sink 适配为 TunnelSink（JSON 字符串直传）。
type tunnelEventEmitter struct{}

func (tunnelEventEmitter) OnTunnelEvent(json string) {
	mu.Lock()
	cb := tunnelSink
	mu.Unlock()
	if cb != nil {
		cb.OnTunnelEvent(json)
	}
}

// errConfig 构造配置错误（gomobile 只透传 error.Error() 字符串）。
type errConfig string

func (e errConfig) Error() string { return string(e) }

// ---- 日志节流 ----

// logThrottler 行缓冲 + 定时批量投递：减少 JNI 往返；队列有界，溢出丢弃并计数。
type logThrottler struct {
	mu     sync.Mutex
	sink   LogSink
	ch     chan string
	drops  int
	closed bool
}

const (
	logQueueCap     = 256
	logFlushEveryMs = 300
	logMaxBatch     = 32
)

// startLogThrottle 启动（进程级一次）；Start 幂等不重复创建。
func startLogThrottle() {
	if logThrottle != nil {
		return
	}
	t := &logThrottler{ch: make(chan string, logQueueCap)}
	logThrottle = t
	// logsSpec 根因修复（首启一次即全断）：Start 先 SetLogCallback（此时 logThrottle
	// 尚为 nil，回调只存模块变量）后 startLogThrottle——节流器创建后必须把已注册的
	// logSink 接到 t.sink，否则首启会话 sink==nil 全丢（引导第 3 步白框 + 运行日志页
	// 「暂无日志」同源）。注意：本函数由 Start() 在持有 mu 期间调用，此处直接读模块
	// 变量，不得再 mu.Lock()（自锁）；setSink 只取 t.mu——两把不同锁，无死锁。
	t.setSink(logSink)
	logsink.SetOutput(t) // 核心全部日志改道节流器
	go t.loop()
}

func (t *logThrottler) setSink(cb LogSink) {
	t.mu.Lock()
	t.sink = cb
	t.mu.Unlock()
}

// Write 实现 io.Writer：逐行入队（无 sink 时丢弃，避免无谓积压）。
func (t *logThrottler) Write(p []byte) (int, error) {
	t.mu.Lock()
	sink := t.sink
	t.mu.Unlock()
	if sink == nil {
		return len(p), nil
	}
	// 按行拆分（logsink 输出已带换行）
	for len(p) > 0 {
		i := 0
		for i < len(p) && p[i] != '\n' {
			i++
		}
		line := string(p[:i])
		if line != "" {
			select {
			case t.ch <- line:
			default:
				t.mu.Lock()
				t.drops++
				t.mu.Unlock()
			}
		}
		if i >= len(p) {
			break
		}
		p = p[i+1:]
	}
	return len(p), nil
}

// loop 批量投递循环。
func (t *logThrottler) loop() {
	ticker := time.NewTicker(logFlushEveryMs * time.Millisecond)
	defer ticker.Stop()
	batch := make([]string, 0, logMaxBatch)
	for range ticker.C {
		drain := true
		for drain && len(batch) < logMaxBatch {
			select {
			case line := <-t.ch:
				batch = append(batch, line)
			default:
				drain = false
			}
		}
		if len(batch) == 0 {
			continue
		}
		t.mu.Lock()
		sink, drops := t.sink, t.drops
		t.drops = 0
		t.mu.Unlock()
		if sink != nil {
			out := ""
			for i, line := range batch {
				if i > 0 {
					out += "\n"
				}
				out += line
			}
			if drops > 0 {
				out += "\n[bridge] 日志过载丢弃 " + itoa(drops) + " 行"
			}
			sink.OnLog(out)
		}
		batch = batch[:0]
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

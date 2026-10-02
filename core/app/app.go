// Package app — NexPort 核心生命周期（NexPort 移植新增）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。桌面版 main.run()（cmd/cph/main.go）拆为库形态：
//
//   - Start(ctx, Options) (gatewayPort, tunnelPort, err)：两个监听器一律 127.0.0.1 随机端口，
//     先 net.Listen 再回报实际端口；去 signal.NotifyContext / os.Exit（进程生命周期由宿主掌控）；
//   - 第二监听器（隧道目标）挂 /v1/* + /health + 品牌化落地页（tunnelpage.go）；
//     设置 tunnel.expose_admin（默认关）开启后额外可达 /admin 与 /panel（登录 + JWT 保护）；
//   - Stop/Restart 幂等：Restart 供备份恢复闭环（restore/ 换入后重启核心生效）。
//
// 线程约定（对接文档）：Java→Go 调用同步阻塞，Start（含建库/迁移/插件自启）必须在工作线程调用；
// 本包与 gomobile 无耦合，bridge 层负责类型适配与日志节流回调。
package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/adminapi"
	"io.nexport.gateway/core/conf"
	"io.nexport.gateway/core/database"
	"io.nexport.gateway/core/event"
	"io.nexport.gateway/core/gateway"
	"io.nexport.gateway/core/janitor"
	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/plugmgr"
	"io.nexport.gateway/core/router"
	"io.nexport.gateway/core/runlog"
	"io.nexport.gateway/core/setting"
	"io.nexport.gateway/core/task"
	"io.nexport.gateway/core/tunnel"
	"io.nexport.gateway/core/version"
	"io.nexport.gateway/core/web"
)

// Options 库形态启动参数（bridge 层从 Android Config 构造）。
type Options struct {
	// DataDir 数据目录（必填：SQLite、插件、secret.key 都在其下）
	DataDir string
	// CacheDir 缓存目录：启动前设为 TMPDIR（否则安卓上 os.CreateTemp("") 落到
	// 不可写的 /data/local/tmp，市场下载/离线上传/备份导出全部报错）
	CacheDir string
	// LogLevel 日志级别（debug/info/warn/error；空 = info）
	LogLevel string
	// Locale 界面语言（核心暂不消费；保留给宿主透传，见对接文档）
	Locale string
	// SecretKeyHex Keystore 解封后的 32 字节密钥（hex，可空）。见 account/crypto.go 双格式说明。
	SecretKeyHex string
	// NativeLibDir 安卓 nativeLibraryDir（luahost 与预打包插件 libplugin_<name>.so 的查找路径；
	// 桌面/测试留空走原逻辑）
	NativeLibDir string
	// MarketplaceURL 插件市场索引（空 = 面板设置优先，再退内置官方源）
	MarketplaceURL string
	// GatewayPort 网关固定端口（设置页「网关固定端口」预留项）。
	// 0（默认）= 自动端口：复用上次持久化端口（<DataDir>/gateway.port，首启随机），
	//   端口被占用等绑定失败时重新随机并把「端口变更（旧→新）」暴露给应用 UI
	//   （app.PortChangeNotice / run-logs / 站内通知三通道）；
	// 1-65535 = 127.0.0.1:<port> 固定监听（固定端口语义：绑定失败 Start 直接失败，
	//   不静默回退——外部配置与实际监听不得错位）。仅作用于对外 API 网关；
	// 隧道目标第二监听器始终随机（纯内部端口，无需固定）。
	GatewayPort int
	// TZOffsetSeconds 应用时区偏移（秒，东八区 = 28800；0 = 不干预，桌面形态用系统本地）。
	// 背景：安卓上 Go 运行时加载不到 /etc/localtime，time.Local 回退 UTC，任务执行历史
	// 等落库/返回的时间与设备本地差 8 小时（v1.3.0 方案 ⑤）。应用在 Start 前把设备时区
	// 偏移经 bridge Config 传入，这里设为进程级 time.Local（FixedZone），使 time.Now()
	// 落库、JSON 序列化均带正确偏移；存量 UTC 记录由 adminapi 返回前统一转 Local 修复。
	TZOffsetSeconds int64
	// LanIPHint 宿主检测的局域网 IPv4（可空）。安卓 targetSdk 34+ 对应用 UID 关闭
	// netlink RTM_GETLINK（net.Interfaces 报 netlinkrib: permission denied，已在
	// Android 14 x86_64 模拟器以 run-as 同 UID 复现），核心自检拿不到地址，局域网
	// 监听永不建立。Kotlin 侧用 java.net.NetworkInterface 枚举站点内 IPv4 后经
	// bridge Config.LanIP 注入，这里在自检失败时兜底采用（v1.4.0 首页修复③）。
	LanIPHint string
}

// App 一个运行中的核心实例。
type App struct {
	mu         sync.Mutex
	opts       Options
	prevTMPDIR string // Start 前的 TMPDIR（Stop 时恢复——进程级 env 不得跨启动泄漏，测试卫生）
	cancel     context.CancelFunc
	gwSrv      *http.Server
	tunnelSrv  *http.Server
	gwPort     int
	tunnelLn   net.Listener
	plugins    *plugmgr.Manager
	engine     *task.Engine
	tunnel     *tunnel.Manager
	db         *gorm.DB
	runLog     *runlog.Logger // 核心生命周期事件（start/stop）落 run-logs
	settings   *setting.Store
	// portChange 自动端口模式下本次启动发生的端口变更描述（旧→新 + 原因；
	// 空串 = 无变更）。启动期写入、运行期只读，无需加锁。
	portChange string
	// lanSrv/lanAddr/gwHandler 局域网监听（方案 ④）：lanSrv 非 nil 表示在听；
	// gwHandler 留存网关 handler 供设置热切换重建监听。状态变更持 a.mu。
	lanSrv    *http.Server
	lanAddr   string       // host:port（如 192.168.1.5:41234）
	gwHandler http.Handler // 网关 handler（reloadLan 重建用；启动期写入）
}

var (
	globalMu sync.Mutex
	global   *App
)

// Start 启动核心（进程级单实例；重复启动返回错误）。
// 返回 (gatewayPort, tunnelPort)：两监听器固定 127.0.0.1:0，先 bind 后上报实际端口。
// ctx 仅约束启动阶段；运行期生命周期由 Stop/Restart 管理。
func Start(ctx context.Context, opts Options) (gatewayPort, tunnelPort int, err error) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global != nil {
		return 0, 0, errors.New("核心已在运行（先 Stop 再 Start）")
	}
	a, gw, tp, err := start(ctx, opts)
	if err != nil {
		return 0, 0, err
	}
	global = a
	return gw, tp, nil
}

// Stop 停止当前实例（HTTP 服务、隧道、插件、任务引擎；幂等）。
func Stop() error {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return nil
	}
	err := global.stop()
	global = nil
	return err
}

// Restart 重启核心（同 Options）：备份恢复闭环用（restore/ 换入须重启生效）。
func Restart() error {
	globalMu.Lock()
	cur := global
	globalMu.Unlock()
	if cur == nil {
		return errors.New("核心未运行")
	}
	opts := cur.opts
	if err := Stop(); err != nil {
		return fmt.Errorf("停止旧实例: %w", err)
	}
	// 备份换入发生在 start 内部（打开库之前 ApplyPendingRestore）
	_, _, err := Start(context.Background(), opts)
	return err
}

// Running 核心是否在运行。
func Running() bool {
	globalMu.Lock()
	defer globalMu.Unlock()
	return global != nil
}

// GatewayPort 网关监听端口（未运行返回 0）。
func GatewayPort() int {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return 0
	}
	return global.gwPort
}

// TunnelPort 隧道目标监听器端口（仅 /v1 + /health；未运行返回 0）。
func TunnelPort() int {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return 0
	}
	return global.tunnelLnAddrPort()
}

// Tunnel 取隧道管理器（未运行返回 nil）。
func Tunnel() *tunnel.Manager {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return nil
	}
	return global.tunnel
}

// Version 版本串。
func Version() string { return version.String() }

// PortChangeNotice 本次启动的网关端口变更通知（自动端口模式下持久化端口绑定失败
// 被迫换口时非空，形如「网关端口 41234 被占用，已自动更换为 52344」；空串 = 无变更）。
// 应用在 Start 成功后查询并提示用户；面板侧同值见 /admin/system/info 的 port_change。
func PortChangeNotice() string {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return ""
	}
	return global.portChange
}

// LanEndpoint 当前局域网端点（如 http://192.168.1.5:41234；未开启 / 未监听为空串）。
// 局域网侧仅暴露 /v1（强制 API 密钥）与 /health，面板与管理 API 永不暴露（方案 ④）。
func LanEndpoint() string {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		return ""
	}
	return global.lanEndpoint()
}

// lanEndpoint 实例方法版（调用方按需持 a.mu）。
func (a *App) lanEndpoint() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lanSrv == nil || a.lanAddr == "" {
		return ""
	}
	return "http://" + a.lanAddr
}

// start 实际启动序列（globalMu 已持）。
func start(ctx context.Context, opts Options) (app *App, gwPort, tunPort int, err error) {
	prevTMPDIR := "" // Start 前的 TMPDIR（Stop 恢复；测试卫生，见下）
	if opts.DataDir == "" {
		return nil, 0, 0, errors.New("DataDir 必填")
	}
	// TMPDIR 先行：os.CreateTemp("") / os.TempDir() 全部改道应用缓存目录。
	// 进程级 env 修改记录旧值，Stop 时恢复——否则包级连跑的测试里 t.TempDir() 会拿到
	// 上一测已被清理的目录而全数报错（合规复审 2026-09-28 测试卫生项）。
	if opts.CacheDir != "" {
		prevTMPDIR = os.Getenv("TMPDIR")
		_ = os.Setenv("TMPDIR", opts.CacheDir)
		_ = os.MkdirAll(opts.CacheDir, 0o700)
	}
	// 失败路径同样恢复（perf 修复轮复跑测试发现）：App 实例未建成即出错返回
	//（固定端口被占用 / TZOffset 非法 / SecretKeyHex 解封失败等）时，进程级 TMPDIR
	// 停留在本次 CacheDir——后续成功 Start 会覆盖且生产自愈，但包级测试里该目录
	// 已随上一测清理，后续用例 t.TempDir() 全数 Fatal（port_test TestGatewayPort-
	// FixedMode 占用端口用例 → TestLanListener 等 TempDir stat 报错）。与 Stop 恢复
	// 同口径：实例未建成 → 立即复原；建成 → 交由 Stop 恢复。
	defer func() {
		if app == nil && opts.CacheDir != "" {
			_ = os.Setenv("TMPDIR", prevTMPDIR)
		}
	}()

	// 日志出口与级别（未注册 sink 时落 stderr，桌面行为不变）
	logsink.SetMinLevel(logsink.ParseLevel(opts.LogLevel))

	// 应用时区（v1.3.0 方案 ⑤）：安卓 Go 运行时无 /etc/localtime，time.Local 回退 UTC，
	// 任务执行历史等时间与设备本地差 8 小时。应用经 bridge 传入设备偏移后在此设为
	// 进程级 Local；0 = 不干预（桌面形态保持系统本地）。
	if opts.TZOffsetSeconds != 0 {
		off := int(opts.TZOffsetSeconds)
		if off < -24*3600 || off > 24*3600 {
			return nil, 0, 0, fmt.Errorf("TZOffsetSeconds %d 非法（±86400 秒内）", off)
		}
		time.Local = time.FixedZone(timezoneName(off), off)
	}

	// 密钥注入（Keystore 信封解封，须早于一切加解密）
	if opts.SecretKeyHex != "" {
		key, err := decodeHex32(opts.SecretKeyHex)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("SecretKeyHex: %w", err)
		}
		account.SetInjectedKey(key)
	}

	// 监听器先行：先拿端口，后续初始化失败也不占用固定资源。
	// 网关地址：GatewayPort>0 固定端口（设置页预留项）；==0 自动端口（v1.3.0 ①）——
	// 优先复用上次持久化端口，绑定失败（被占用等）重新随机并记录端口变更。
	persistedPort := readPersistedPort(opts.DataDir) // 落盘前先读：换口判定要用旧值
	gwAddr := conf.Loopback()
	switch {
	case opts.GatewayPort < 0 || opts.GatewayPort > 65535:
		return nil, 0, 0, fmt.Errorf("GatewayPort %d 非法（0=自动，1-65535=固定）", opts.GatewayPort)
	case opts.GatewayPort > 0:
		gwAddr = conf.LoopbackAt(opts.GatewayPort)
	default:
		if persistedPort > 0 {
			gwAddr = conf.LoopbackAt(persistedPort)
		}
	}
	gwLn, err := net.Listen("tcp", gwAddr)
	if err != nil && opts.GatewayPort == 0 {
		// 自动端口：持久化端口绑定失败（被占用 / 保留端口 / 权限）→ 重新随机。
		// 端口变更在拿到新端口后组装（应用 UI / run-logs / 站内通知三通道提示）。
		gwLn, err = net.Listen("tcp", conf.Loopback())
	}
	if err != nil {
		if opts.GatewayPort > 0 {
			return nil, 0, 0, fmt.Errorf("listen gateway: 网关固定端口 %d 监听失败（可能被占用）: %w", opts.GatewayPort, err)
		}
		return nil, 0, 0, fmt.Errorf("listen gateway: %w", err)
	}
	tunLn, err := net.Listen("tcp", conf.Loopback())
	if err != nil {
		gwLn.Close()
		return nil, 0, 0, fmt.Errorf("listen tunnel: %w", err)
	}
	gwPort = gwLn.Addr().(*net.TCPAddr).Port
	tunPort = tunLn.Addr().(*net.TCPAddr).Port
	// 端口持久化（三种模式统一落最近一次生效端口；切回自动模式时从它继续）
	persistPort(opts.DataDir, gwPort)
	portChange := ""
	if opts.GatewayPort == 0 && persistedPort > 0 && persistedPort != gwPort {
		portChange = fmt.Sprintf("网关端口 %d 被占用，已自动更换为 %d（依赖旧端口的外部客户端需改用新端口）", persistedPort, gwPort)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	a := &App{opts: opts, gwPort: gwPort, tunnelLn: tunLn, portChange: portChange, prevTMPDIR: prevTMPDIR}
	a.tunnel = tunnel.New(tunPort)

	fail := func(step string, err error) (*App, int, int, error) {
		gwLn.Close()
		tunLn.Close()
		return nil, 0, 0, fmt.Errorf("%s: %w", step, err)
	}

	// 插件目录 + 安卓 nativeLibraryDir 注入（luahost / 预打包插件查找）
	if err := os.MkdirAll(pluginDirOf(opts), 0o755); err != nil {
		return fail("create plugin dir", err)
	}

	// 密钥可用性预检（信封缺注入等情况快速失败，而不是静默明文）
	if err := account.EnsureKey(opts.DataDir); err != nil {
		return fail("secret key", err)
	}

	// 管理界面上传的备份在此换入（打开库之前）
	dsn := filepath.Join(opts.DataDir, "cph.db")
	dbPath := database.DSNToFilepath(dsn)
	if err := database.ApplyPendingRestore(dbPath, opts.DataDir); err != nil {
		return fail("apply pending restore", err)
	}
	db, err := database.Open(ctx, dsn)
	if err != nil {
		return fail("open database", err)
	}
	a.db = db
	// 运行日志（核心 / 隧道生命周期埋点；级别走设置实时读取）
	settings := setting.New(db)
	rl := runlog.New(db, settings.RunLevel)
	a.runLog = rl
	a.settings = settings
	// 隧道生命周期事件写 run-logs（starting/active/error/stop 全程可查）
	a.tunnel.SetRunLogger(rl)

	if key := os.Getenv("CPH_SEED_API_KEY"); key != "" {
		if err := seedAPIKey(db, key, opts.DataDir); err != nil {
			rl.Error("core", "start", "种子 API Key 写入失败", err.Error(), nil)
			return fail("seed api key", err)
		}
	}

	plugins := plugmgr.NewManager(pluginDirOf(opts), db)
	plugins.SetNativeLibDir(opts.NativeLibDir)
	// 安卓内置 Go 插件落盘（nativeLibraryDir 有 libplugin_<name>.so 而目录缺失时补
	// manifest/icon；桌面为空操作）——使 Scan / 自启 / 市场安装状态走既有链路
	plugins.EnsureBuiltinPlugins()
	// 开机自启：plugmgr.AutoStarts 双重过滤（①持久化停止 enabled=0 跳过；②仅拉起
	// 已配置账号的插件——无账号插件不承载任何请求，常驻纯耗内存，经管理页「启动」
	// 显式拉起）。
	// 分批限流 spawn（perf 修复轮）：每批 4 个并发、批间让出 CPU——顺序全量拉起 25 个
	// go-plugin 子进程实测构成 spawn 风暴（资源竞争轮核心启动 +5.2s、CPU 饱和），是
	// 升级首启 ANR 的环境放大器。Start 并发安全（实例/客户端各自独立，仅落表短暂持锁）。
	if bins, err := plugins.AutoStarts(); err == nil {
		const batchSize = 4
		for i := 0; i < len(bins); i += batchSize {
			end := i + batchSize
			if end > len(bins) {
				end = len(bins)
			}
			var wg sync.WaitGroup
			for _, bin := range bins[i:end] {
				wg.Add(1)
				go func(bin string) {
					defer wg.Done()
					if _, err := plugins.Start(ctx, bin); err != nil {
						logsink.Printf("[plugin] start failed: %v", err)
						rl.Warn("plugin", "start", "插件启动失败: "+filepath.Base(bin), err.Error(), nil)
					}
				}(bin)
			}
			wg.Wait()
			runtime.Gosched() // 批间让出一拍，缓解 spawn 竞争峰值
		}
	}
	plugins.RefreshCatalog(ctx)
	a.plugins = plugins

	bus := event.New()
	accounts := account.New(db, opts.DataDir, plugins)
	accounts.SubscribeRefresh(ctx, bus)

	engine := task.NewEngine(db, opts.DataDir, task.NewPluginRunner(plugins), bus)
	engine.Start(ctx)
	a.engine = engine

	janitor.StartLogRetention(ctx, db, settings)
	gw := gateway.New(db, opts.DataDir, plugins, router.New(db), accounts, settings)

	adminSrv := adminapi.New(db, accounts, plugins, engine, settings,
		marketplaceURLOf(opts), opts.DataDir, dbPath)
	adminSrv.SetTempDir(tempDirOf(opts))
	// 运行时信息（网关端口 / 端口变更 / 局域网端点）注入 system/info，面板与桥接层
	// 消费同一份事实；LAN 开关设置变化回调热切换监听（方案 ④）
	adminSrv.SetRuntimeInfo(func() map[string]interface{} {
		return map[string]interface{}{
			"gateway_port": a.gwPort,
			"port_change":  a.portChange,
			"lan_enabled":  settings.LanEnabled(),
			"lan_endpoint": a.lanEndpoint(),
		}
	})
	adminSrv.SetLanReloader(a.reloadLan)

	// 主 mux：/v1、/health、/admin、图标、面板（桌面版 main.go 拓扑原样保留）
	mux := http.NewServeMux()
	mux.Handle("/v1/", gw.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/admin/", adminSrv.Handler())
	// 插件图标（img 标签带不了 Authorization，走免鉴权只读静态服务）
	mux.HandleFunc("GET /assets/plugins/{name}/icon", func(w http.ResponseWriter, r *http.Request) {
		path, ok := plugins.IconFile(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeFile(w, r, path)
	})
	mux.Handle("/", web.Handler())

	// 隧道第二监听器：/v1/* + /health + / 品牌化落地页（tunnelpage.go）。
	// 设置 tunnel.expose_admin（默认关）开启后公网额外可达 /admin（登录 + JWT 保护）
	// 与 /panel 面板入口；关闭时 /admin 无注册 → 404，不暴露存在性。
	tunMux := newTunnelMux(gw.Handler(), adminSrv.Handler(), settings)

	// cancel 关停信号：ctx 本体无需持有（Done 由 stop 触发关闭）
	_, a.cancel = context.WithCancel(context.Background())
	a.gwSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	a.tunnelSrv = &http.Server{Handler: tunMux, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = a.gwSrv.Serve(gwLn) }()
	go func() { _ = a.tunnelSrv.Serve(tunLn) }()

	// 局域网监听（方案 ④，默认开）：绑定检测到的局域网 IPv4:网关端口，仅 /v1+/health。
	// 失败（无局域网地址 / 端口被占）只降级为不监听，回环服务不受影响。
	a.startLan(gw.Handler(), rl)

	logsink.Printf("[app] core listening: gateway=127.0.0.1:%d tunnel-target=127.0.0.1:%d (v1+landing%s)%s, version=%s",
		gwPort, tunPort, exposeTag(settings), lanTag(a), version.String())
	// 运行日志：核心启动事件（v1.1.0 遗留「运行日志页全程暂无日志」的主埋点之一）
	rl.Info("core", "start", fmt.Sprintf("核心启动（网关 127.0.0.1:%d，插件 %d 个）", gwPort, len(plugins.Names())),
		fmt.Sprintf("隧道目标监听 127.0.0.1:%d；版本 %s；数据目录 %s", tunPort, version.String(), opts.DataDir), nil)
	// 端口变更（自动模式下持久化端口被占用被迫换口）：运行日志 + 站内通知，应用 UI
	// 经 PortChangeNotice() 弹提示——三通道保证用户一定看到（方案 ①）
	if portChange != "" {
		rl.Warn("core", "port-change", portChange, fmt.Sprintf("127.0.0.1:%d", gwPort), nil)
		db.Create(&model.Notification{Title: "网关端口已变更", Content: portChange, Level: "warning"})
	}
	return a, gwPort, tunPort, nil
}

// exposeTag 启动日志里标注面板暴露开关状态（开=经隧道暴露 admin/panel）。
func exposeTag(settings *setting.Store) string {
	if settings.TunnelExposeAdmin() {
		return "+admin"
	}
	return ""
}

// timezoneName 时区名（UTC±HH:MM 形态；展示用途，偏移值才是语义载体）。
func timezoneName(off int) string {
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, off/3600, (off%3600)/60)
}

// stop 停止序列：HTTP 优雅关停 → 隧道 → 插件 → 任务引擎 → cancel。
func (a *App) stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var first error
	if a.gwSrv != nil {
		first = a.gwSrv.Shutdown(shutdownCtx)
	}
	if a.tunnelSrv != nil {
		if err := a.tunnelSrv.Shutdown(shutdownCtx); err != nil && first == nil {
			first = err
		}
	}
	a.stopLanLocked() // 局域网监听（方案 ④）随核心关停（已持 a.mu）
	if a.tunnel != nil {
		a.tunnel.Stop()
	}
	if a.plugins != nil {
		a.plugins.StopAll()
	}
	if a.engine != nil {
		a.engine.Stop()
	}
	if a.cancel != nil {
		a.cancel()
	}
	// 恢复 Start 前的 TMPDIR（进程级 env 污染回收；安卓宿主下次 Start 会再设）
	if a.prevTMPDIR != "" || os.Getenv("TMPDIR") != "" {
		_ = os.Setenv("TMPDIR", a.prevTMPDIR)
	}
	// 运行日志先落库（stop 事件），再释放 sqlite 句柄
	if a.runLog != nil {
		a.runLog.Info("core", "stop", "核心已停止", "", nil)
	}
	// 释放 sqlite 句柄：全部组件已停、stop 事件已落库；Windows 下不关会一直锁住
	// cph.db（备份换库 / 测试 TempDir 清理都会踩到）
	if a.db != nil {
		if sqlDB, err := a.db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	logsink.Printf("[app] core stopped")
	return first
}

// tunnelLnAddrPort 隧道目标端口（a.tunnelLn 保存的监听器）。
func (a *App) tunnelLnAddrPort() int {
	if a.tunnelLn == nil {
		return 0
	}
	return a.tunnelLn.Addr().(*net.TCPAddr).Port
}

// pluginDirOf 插件目录（<DataDir>/plugins）。
func pluginDirOf(opts Options) string {
	return filepath.Join(opts.DataDir, "plugins")
}

// tempDirOf 临时目录：CacheDir 优先，退 os.TempDir()。
func tempDirOf(opts Options) string {
	if opts.CacheDir != "" {
		return opts.CacheDir
	}
	return os.TempDir()
}

// marketplaceURLOf 市场地址：显式指定 > 面板设置内置默认。
func marketplaceURLOf(opts Options) string {
	if opts.MarketplaceURL != "" {
		return opts.MarketplaceURL
	}
	return setting.DefaultMarketplaceURL
}

// seedAPIKey 首次部署引导：环境变量指定 key，不存在则入库（加密存储）。
func seedAPIKey(db *gorm.DB, raw string, dataDir string) error {
	var count int64
	db.Model(&model.Key{}).Where("key_cipher = ?", string(account.EncryptCredential(dataDir, []byte(raw)))).Count(&count)
	if count > 0 {
		return nil
	}
	return db.Create(&model.Key{KeyCipher: string(account.EncryptCredential(dataDir, []byte(raw))), Name: "seed"}).Error
}

// decodeHex32 解析 32 字节 hex 密钥。
func decodeHex32(s string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("须为 64 个 hex 字符: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("须为 32 字节（64 个 hex 字符），实际 %d 字节", len(key))
	}
	return key, nil
}

// lan.go — 局域网监听（NexPort v1.3.0 方案 ④）。
//
// 需求：新增可设置开关（默认开，setting.KeyLanEnabled）；开启后网关额外监听局域网
// 地址，但仅暴露 /v1 与 /health 且强制 API 密钥鉴权，/admin 与面板永不暴露到局域网。
//
// 安全设计（从严）：
//   - 绝不绑定 0.0.0.0 通配：只绑定检测到的本机局域网 IPv4（无可用地址则不监听，
//     回环服务不受影响）；绑定的端口号与回环网关相同（不同地址，可共存）；
//   - 专用 mux 只注册 GET /health 与 /v1/*（复用网关 handler，/v1 逐请求 API 密钥
//     鉴权——gateway.Server.authorize 对 models/messages/chat/completions/responses
//     全部强制）；其余路径无注册 → 404，不暴露 /admin 与面板的存在性；
//   - /health 只回 {"status":"ok"}，无版本、无拓扑、无密钥信息；
//   - 开关关闭/开启经 adminapi 设置回调热切换（stop 旧监听器 → 按需重建）。
package app

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/runlog"
)

// newLanMux 局域网专用 mux：仅 /v1/*（网关，强制 API 密钥）+ /health；其余一律 404。
func newLanMux(gw http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", gw)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	// 根兜底 404：局域网不提供落地页/面板/管理 API（不暴露存在性）
	return mux
}

// lanListenIP 检测本机可对外服务的局域网 IPv4。
// 选择策略（确定性）：跳过回环 / 未启用 / 点对点接口与 link-local（169.254/16）；
// 接口名优先级 wlan* > eth* > 其余，同优先级按接口序号；地址优先 RFC1918 私网。
// 返回空串表示无可用局域网地址（飞行模式 / 仅回环等），调用方跳过监听。
func lanListenIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	type cand struct {
		ifIndex int
		name    string
		ip      net.IP
	}
	var cands []cand
	prio := func(name string) int {
		switch {
		case strings.HasPrefix(name, "wlan"):
			return 0
		case strings.HasPrefix(name, "eth"):
			return 1
		default:
			return 2
		}
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			cands = append(cands, cand{ifIndex: ifc.Index, name: ifc.Name, ip: ip4})
		}
	}
	if len(cands) == 0 {
		return ""
	}
	isPrivate := func(ip net.IP) bool {
		return ip.IsPrivate() // RFC1918 + RFC4193（IPv4 下即 10/8、172.16/12、192.168/16）
	}
	sort.SliceStable(cands, func(i, j int) bool {
		pi, pj := prio(cands[i].name), prio(cands[j].name)
		if pi != pj {
			return pi < pj
		}
		if cands[i].ifIndex != cands[j].ifIndex {
			return cands[i].ifIndex < cands[j].ifIndex
		}
		return isPrivate(cands[j].ip) && !isPrivate(cands[i].ip) // 私网优先
	})
	// 名字优先级最高者里再挑私网地址（同接口多地址时）
	top := prio(cands[0].name)
	for _, c := range cands {
		if prio(c.name) == top && isPrivate(c.ip) {
			return c.ip.String()
		}
	}
	return cands[0].ip.String()
}

// lanTag 启动日志里的局域网监听标注（开=地址，关=空串）。
func lanTag(a *App) string {
	if ep := a.lanEndpoint(); ep != "" {
		return " lan=" + ep
	}
	return " lan=off"
}

// startLan 按当前设置开启局域网监听（默认开）：绑定 <局域网IP>:<网关端口>，
// 仅暴露 /v1（复用网关 handler，强制 API 密钥）与 /health。失败降级为不监听
// （无局域网地址 / 端口被占 / 已在听），不影响回环服务；结果落运行日志。
func (a *App) startLan(gw http.Handler, rl *runlog.Logger) {
	a.mu.Lock()
	a.gwHandler = gw // 留存网关 handler：设置页热切换时重建监听用
	if a.lanSrv != nil {
		a.mu.Unlock()
		return
	}
	if !a.settings.LanEnabled() {
		a.mu.Unlock()
		logsink.Printf("[app] lan listening disabled by settings")
		return
	}
	gwPort := a.gwPort
	a.mu.Unlock()

	ip := lanListenIP()
	source := "detect"
	if ip == "" {
		// 安卓 targetSdk 34+ 对应用 UID 关闭 netlink RTM_GETLINK（net.Interfaces 报
		// netlinkrib: permission denied，Android 14 模拟器 run-as 同 UID 复现）——
		// 宿主经 Config.LanIP 注入 NetworkInterface 枚举的站点内 IPv4 兜底（修复③）。
		if hint := strings.TrimSpace(a.opts.LanIPHint); hint != "" && net.ParseIP(hint) != nil {
			ip, source = hint, "host-hint"
		}
	}
	if ip == "" {
		logsink.Printf("[app] lan listening skipped: no usable LAN IPv4")
		return
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(gwPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		msg := "局域网监听失败（端口被占或地址不可用），已跳过: " + err.Error()
		logsink.Printf("[app] %s", msg)
		if rl != nil {
			rl.Warn("core", "lan", msg, addr, nil)
		}
		return
	}
	srv := &http.Server{Handler: newLanMux(gw), ReadHeaderTimeout: 30 * time.Second}
	a.mu.Lock()
	if a.lanSrv != nil { // 并发保护：已在听则不重复起（理论上仅测试会触发）
		a.mu.Unlock()
		ln.Close()
		return
	}
	a.lanSrv, a.lanAddr = srv, addr
	a.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	logsink.Printf("[app] lan listening: %s (source=%s；仅 /v1 强制密钥 + /health；面板/管理 API 不暴露)", addr, source)
	if rl != nil {
		rl.Info("core", "lan", "局域网监听已开启: "+addr, "仅暴露 /v1（强制 API 密钥）与 /health", nil)
	}
}

// stopLan 关闭局域网监听（幂等；自行持锁）。
func (a *App) stopLan() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopLanLocked()
}

// stopLanLocked 关闭实现（调用方须已持 a.mu；stop() 序列内复用避免重入死锁）。
func (a *App) stopLanLocked() {
	srv := a.lanSrv
	a.lanSrv, a.lanAddr = nil, ""
	if srv == nil {
		return
	}
	go func() { // Shutdown 阻塞数秒，不持锁执行（仅终止 goroutine，无共享态）
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		logsink.Printf("[app] lan listening stopped")
	}()
}

// reloadLan 局域网开关设置变化后的热切换（adminapi 回调）：先停旧监听，再按当前
// 设置决定是否重建。启动早期 gw handler 未就绪时安全跳过（启动序列稍后 startLan）。
func (a *App) reloadLan() {
	a.stopLan()
	if a.settings == nil || !a.settings.LanEnabled() {
		return
	}
	a.mu.Lock()
	gw := a.gwHandler
	a.mu.Unlock()
	if gw != nil {
		a.startLan(gw, a.runLog)
	}
}

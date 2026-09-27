package io.nexport.gateway.app

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import io.nexport.gateway.bridge.Bridge
import io.nexport.gateway.bridge.Config
import io.nexport.gateway.bridge.LogSink
import io.nexport.gateway.bridge.TunnelSink
import org.json.JSONObject
import java.io.File
import java.util.Locale
import java.util.concurrent.CopyOnWriteArrayList

/**
 * CoreController — Go 核心（gomobile bridge）的线程安全封装。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 *
 * 线程约束（对接文档，必须遵守）：
 *  - gobind Java→Go 调用是同步阻塞调用：Start（建库/迁移/插件自启）一律在工作线程调用，
 *    主线程直调必 ANR；
 *  - Go→Kotlin 回调（onLog/onTunnelEvent）在 Go 协程触发，这里统一 post 回主线程再分发，
 *    UI 只在主线程更新；
 *  - secret.key 双格式：启动前 KeyEnvelope.prepareInjection 规整并注入（见该类注释）。
 *
 * v1.1.0 接线：
 *  - start() 按「网络·网关端口」设置调用 Config.setGatewayPort(long)（0 = 随机，核心默认）；
 *  - tunnelSink 解析出的 message 持有到 lastTunnelMessage（进程生命周期内有效），
 *    首页隧道卡 error 态消费；跨核心重启的持久错误痕迹以日志环形缓冲为准；
 *  - 隧道自动重连：error 且非用户主动断开时按 30s/1m/2m/5m 退避至多 5 次（tunnelUxSpec）；
 *    每次重连前校验核心 RUNNING 且 libcloudflared.so 存在（startTunnel 既有检查）。
 */
object CoreController {
    enum class State { NOT_STARTED, STARTING, RUNNING, FAILED }

    interface Listener {
        fun onCoreStateChanged(state: State, error: String?) {}
        fun onCoreLog(line: String) {}
        fun onTunnelEvent(state: String, url: String, message: String) {}
    }

    @Volatile var state = State.NOT_STARTED; private set
    @Volatile var lastError: String? = null; private set
    @Volatile var gatewayPort = 0; private set
    @Volatile var tunnelPort = 0; private set
    /** 局域网端点（如 http://192.168.1.5:41234；未开启/未监听为空串。仅 /v1+密钥与 /health）。 */
    @Volatile var lanEndpoint = ""; private set
    @Volatile var tunnelState = "idle"; private set
    @Volatile var tunnelUrl = ""; private set

    /**
     * 本次启动的网关端口变更通知（v1.4.0 ②）：核心自动端口模式下持久化端口被占用被迫
     * 换口时核心给出描述文本（空串 = 无变更）；first/second = 旧端口→新端口（核心
     * 文本解析兜底 + 壳侧跨重启记录双源）。UI 据此在网关卡显著提示「旧 → 新」。
     */
    @Volatile var portNotice: String = ""; private set
    @Volatile var portChangedFrom = 0; private set
    @Volatile var portChangedTo = 0; private set

    /** 最近一次隧道错误消息（tunnelSink 解析；进程内有效，核心停止不清除以便 UI 收尾展示）。 */
    @Volatile var lastTunnelMessage: String = ""; private set

    /** 自动重连状态（首页展示「重连中（第 n/5 次，xx 秒后）」用）。 */
    @Volatile var reconnectAttempt = 0; private set
    @Volatile var reconnectNextAtMs = 0L; private set
    @Volatile var reconnectGaveUp = false; private set
    /** 自动重连退避序列（tunnelUxSpec：30s/1m/2m/5m，至多 5 次）。 */
    val reconnectBackoffMs = longArrayOf(30_000L, 60_000L, 120_000L, 300_000L)
    const val RECONNECT_MAX_ATTEMPTS = 5

    private lateinit var appContext: Context
    private val main = Handler(Looper.getMainLooper())
    private val listeners = CopyOnWriteArrayList<Listener>()
    private val logBuf = ArrayDeque<String>()

    /** 环形缓冲容量（logsSpec ③：400 → 2000 行）。 */
    private const val LOG_BUF_MAX = 2000

    private val lock = Any()
    private var reconnectScheduled = false

    fun init(ctx: Context) { appContext = ctx.applicationContext }

    fun dataDir(): File = File(appContext.filesDir, "core")
    fun nativeLibDir(): String = appContext.applicationInfo.nativeLibraryDir
    fun cloudflaredPath(): String = nativeLibDir() + File.separator + "libcloudflared.so"

    fun addListener(l: Listener) { listeners.add(l) }
    fun removeListener(l: Listener) { listeners.remove(l) }

    /**
     * 应用侧状态入流（logsSpec ①②）：核心/隧道状态迁移追加进日志环形缓冲，行首统一
     * HH:mm:ss（Go logsink/emit.go 只加 [error]/[warn] 级别前缀、无时间戳）。注意：本
     * 方法可能在工作线程被调，与 logSink.onLog 同用 synchronized(logBuf) 保护；监听器
     * 通知仍经 main.post。日志行文案为终端语言直写（日志不是 UI 资源）。
     */
    private fun appendLog(level: String, text: String) {
        val ts = java.text.SimpleDateFormat("HH:mm:ss", Locale.US).format(java.util.Date())
        synchronized(logBuf) {
            logBuf.addLast("$ts $level $text")
            while (logBuf.size > LOG_BUF_MAX) logBuf.removeFirst()
        }
    }

    // ---- Go→Kotlin 回调（Go 线程触发，post 主线程） ----

    private val logSink = object : LogSink {
        override fun onLog(line: String) {
            main.post {
                for (l in line.split('\n')) {
                    if (l.isBlank()) continue
                    synchronized(logBuf) {
                        logBuf.addLast(l)
                        while (logBuf.size > LOG_BUF_MAX) logBuf.removeFirst()
                    }
                }
                listeners.forEach { it.onCoreLog(line) }
            }
        }
    }

    private val tunnelSink = object : TunnelSink {
        override fun onTunnelEvent(json: String) {
            var st = tunnelState; var url = tunnelUrl; var msg = ""
            try {
                val o = JSONObject(json)
                st = o.optString("state", st)
                url = o.optString("url", "")
                msg = o.optString("message", "")
            } catch (_: Exception) { /* 非法 JSON 忽略 */ }
            tunnelState = st; tunnelUrl = url
            if (msg.isNotEmpty()) lastTunnelMessage = msg
            // 隧道状态迁移入流（logsSpec ①）：[tunnel] 过滤片从此有真数据
            appendLog("[tunnel]", when (st) {
                "active" -> "状态: active（$url）"
                "error" -> "状态: error（${msg.ifEmpty { "unknown" }}）"
                else -> "状态: $st"
            })
            main.post {
                listeners.forEach { it.onTunnelEvent(st, url, msg) }
                GatewayService.refresh(appContext)
                handleTunnelEventForReconnect(st)
            }
        }
    }

    fun snapshotLogs(): String = synchronized(logBuf) { logBuf.joinToString("\n") }

    /** 「运行日志·清屏」：仅清空环形缓冲（核心侧日志流继续）。 */
    fun clearLogs() = synchronized(logBuf) { logBuf.clear() }

    private fun setState(s: State, err: String?) {
        state = s; lastError = err
        // 状态迁移入流（logsSpec ①）：工作线程被调，appendLog 内部 synchronized(logBuf)
        when (s) {
            State.RUNNING -> appendLog("[app]", "核心已启动 · 网关 127.0.0.1:${gatewayPort}")
            State.FAILED -> appendLog("[error]", "核心启动失败: ${err ?: "unknown"}")
            else -> {}
        }
        main.post {
            listeners.forEach { it.onCoreStateChanged(s, err) }
            GatewayService.refresh(appContext)
        }
    }

    // ---- 生命周期（全部工作线程执行同步阻塞的 bridge 调用） ----

    /** 启动核心（幂等；onDone 在主线程回调）。网关端口按设置注入（自动随机 / 固定）。 */
    fun start(onDone: ((ok: Boolean, err: String?) -> Unit)? = null) {
        synchronized(lock) {
            if (state == State.STARTING || state == State.RUNNING) {
                onDone?.let { main.post { it(state == State.RUNNING, lastError) } }
                return
            }
            setState(State.STARTING, null)
        }
        Thread {
            try {
                val dir = dataDir(); dir.mkdirs()
                val hex = KeyEnvelope.prepareInjection(dir)
                Bridge.setLogCallback(logSink)
                Bridge.setTunnelCallback(tunnelSink)
                val cfg = Config()
                cfg.dataDir = dir.absolutePath
                cfg.cacheDir = appContext.cacheDir.absolutePath
                cfg.logLevel = "info"
                // 核心 Locale 透传改读「通用·语言」设置（原按系统语言）
                cfg.locale = when (Prefs.language(appContext)) {
                    "zh" -> "zh"; "en" -> "en"
                    else -> if (Locale.getDefault().language.startsWith("zh")) "zh" else "en"
                }
                cfg.nativeLibDir = nativeLibDir()
                cfg.secretKeyHex = hex ?: ""
                // 设备时区偏移（秒）：安卓 Go 运行时无 /etc/localtime，不传则任务执行历史
                // 等时间与设备差 8 小时（v1.3.0 时区修复）
                cfg.tzOffsetSeconds =
                    java.util.TimeZone.getDefault().getOffset(System.currentTimeMillis()).toLong() / 1000
                // 局域网 IPv4（java.net.NetworkInterface 枚举）：Android targetSdk 34+ 对应用
                // UID 关闭 netlink RTM_GETLINK，核心侧 net.Interfaces 报 permission denied，
                // 自检永远拿不到地址 → 局域网监听永不建立（v1.4.0 首页修复③，run-as 复现）。
                // 这里把宿主枚举到的站点内 IPv4 注入核心，核心自检失败时兜底采用。
                cfg.lanIP = LanDetect.bestLanIPv4() ?: ""
                // 网关端口（app.go Options.GatewayPort：0=自动[持久化复用]，1-65535=固定，占用明确报错）
                cfg.gatewayPort =
                    if (Prefs.gatewayPortFixed(appContext)) Prefs.gatewayPortValue(appContext).toLong() else 0L
                val prevPort = gatewayPort // 跨重启记忆（v1.4.0 ②：端口变更「旧→新」显著提示）
                Bridge.start(cfg) // 同步阻塞：建库 + 迁移 + 插件自启
                KeyEnvelope.wrapIfRaw(dir) // 首启生成的裸密钥换入 Keystore 信封
                gatewayPort = Bridge.gatewayPort().toInt()
                tunnelPort = Bridge.tunnelPort().toInt()
                lanEndpoint = Bridge.lanEndpoint()
                // 端口变更通知（自动端口被占用换口时非空）：追加进日志入流供 UI 提示 +
                // 结构化旧→新端口（核心描述文本解析，兜底壳侧 prevPort 比对）
                val notice = Bridge.portChangeNotice()
                portNotice = notice
                var from = 0; var to = 0
                Regex("(\\d+)").findAll(notice).map { it.value.toInt() }.toList()
                    .takeIf { it.size >= 2 }?.let { from = it[0]; to = it[1] }
                if (from == 0 && prevPort in 1..65535 && prevPort != gatewayPort) {
                    from = prevPort; to = gatewayPort
                }
                portChangedFrom = from; portChangedTo = to
                if (notice.isNotBlank()) appendLog("[app]", notice)
                AdminApi.reset()
                setState(State.RUNNING, null)
                onDone?.let { main.post { it(true, null) } }
            } catch (t: Throwable) {
                val msg = t.message ?: t.toString()
                setState(State.FAILED, msg)
                onDone?.let { main.post { it(false, msg) } }
            }
        }.start()
    }

    /** 停止核心（幂等；先断隧道再停核心）。 */
    fun stop(onDone: ((ok: Boolean, err: String?) -> Unit)? = null) {
        if (state != State.RUNNING) { onDone?.let { main.post { it(true, null) } }; return }
        setState(State.STARTING, null) // 复用“进行中”态防并发启动
        appendLog("[app]", "核心停止中") // 状态入流（logsSpec ①）
        cancelReconnect(reset = true)
        userTunnelStop = true
        Thread {
            var err: String? = null
            try { Bridge.stopTunnel() } catch (t: Throwable) { /* 幂等 */ }
            try { Bridge.stop() } catch (t: Throwable) { err = t.message ?: t.toString() }
            tunnelState = "idle"; tunnelUrl = ""
            gatewayPort = 0; tunnelPort = 0; lanEndpoint = ""
            portNotice = ""; portChangedFrom = 0; portChangedTo = 0
            AdminApi.reset()
            setState(if (err == null) State.NOT_STARTED else State.FAILED, err)
            onDone?.let { main.post { it(err == null, err) } }
        }.start()
    }

    /**
     * 重启核心（备份恢复闭环：restore/ 换入后调用）。
     *
     * 实现为完整 Stop + Start（而非 bridge.Restart()）：备份换入的新 secret.key 必须重新走
     * KeyEnvelope.prepareInjection——否则核心侧 account.loadOrCreateKey（crypto.go 优先级
     * env > 注入 > 文件）会继续用旧的注入密钥，桌面备份的凭据将解不开（高危⑥场景）。
     * restore/ 换入发生在 app.Start 打开库之前（database.ApplyPendingRestore），语义不变。
     */
    fun restart(onDone: ((ok: Boolean, err: String?) -> Unit)? = null) {
        if (state != State.RUNNING) {
            onDone?.let { main.post { it(false, "核心未运行") } }
            return
        }
        // stop() 自行完成状态迁移（RUNNING→NOT_STARTED），完成后再 start()
        stop { ok, err ->
            if (!ok) {
                onDone?.let { main.post { it(false, err ?: "stop failed") } }
                return@stop
            }
            start(onDone)
        }
    }

    /** 启动临时隧道（同步阻塞至拿到域名或失败，工作线程；事件经 onTunnelEvent 推送）。 */
    fun startTunnel(onDone: ((ok: Boolean, err: String?) -> Unit)? = null) {
        if (state != State.RUNNING) { onDone?.let { main.post { it(false, "核心未运行") } }; return }
        val bin = cloudflaredPath()
        if (!File(bin).isFile) {
            onDone?.let { main.post { it(false, "未找到 libcloudflared.so") } }
            return
        }
        if (tunnelState == "starting" || tunnelState == "active") return
        userTunnelStop = false
        tunnelState = "starting"
        main.post { listeners.forEach { it.onTunnelEvent("starting", "", "") } }
        Thread {
            try {
                Bridge.startTunnel(bin) // 阻塞至 Active/失败；QUIC 失败自动 http2 重试一次
                when (tunnelState) {
                    "active" -> onDone?.let { main.post { it(true, null) } }
                    "starting" -> {
                        // 建立仍在进行（QUIC→http2 重试窗口 / 并发触发的重入返回）：
                        // 不回调失败，避免「操作失败：starting」闪现 toast（QA 回归修复）；
                        // 状态行保持「建立中」，结果由 tunnelSink 事件流推送
                    }
                    else -> onDone?.let { main.post { it(false, tunnelState) } }
                }
            } catch (t: Throwable) {
                val msg = t.message ?: t.toString()
                tunnelState = "error"
                lastTunnelMessage = msg
                main.post { listeners.forEach { it.onTunnelEvent("error", "", msg) } }
                onDone?.let { main.post { it(false, msg) } }
            }
        }.start()
    }

    fun stopTunnel() {
        userTunnelStop = true // 用户手动断开不触发自动重连（tunnelUxSpec）
        cancelReconnect(reset = true)
        Thread { try { Bridge.stopTunnel() } catch (_: Throwable) {} }.start()
    }

    // ---- 隧道自动重连（tunnelUxSpec：error/awaitExit 非主动 Stop 时退避重试） ----

    /** 用户主动断开标记：Stop 路径（Stop()→Idle）不产生 error 事件，此标记兜底手停后残留。 */
    @Volatile private var userTunnelStop = false

    private fun handleTunnelEventForReconnect(st: String) {
        when {
            st == "active" -> {
                // 成功清计数
                cancelReconnect(reset = true)
                userTunnelStop = false
            }
            st == "error" && !userTunnelStop -> scheduleReconnect()
            st == "idle" -> cancelReconnect(reset = false)
        }
    }

    private fun scheduleReconnect() {
        if (!Prefs.tunnelAutoReconnect(appContext)) { reconnectGaveUp = false; return }
        if (reconnectGaveUp) return // 放弃后停留「已停止重试」，等待手动重试
        if (reconnectScheduled) return
        val attempt = reconnectAttempt + 1
        if (attempt > RECONNECT_MAX_ATTEMPTS) {
            reconnectGaveUp = true
            reconnectNextAtMs = 0
            main.post { listeners.forEach { l -> l.onTunnelEvent(tunnelState, tunnelUrl, lastTunnelMessage) } }
            return
        }
        reconnectAttempt = attempt
        val delay = reconnectBackoffMs[(attempt - 1).coerceIn(0, reconnectBackoffMs.size - 1)]
        reconnectNextAtMs = System.currentTimeMillis() + delay
        reconnectScheduled = true
        main.postDelayed(RECONNECT_TOKEN, delay) {
            reconnectScheduled = false
            if (state != State.RUNNING) return@postDelayed
            if (tunnelState != "error") return@postDelayed // 期间用户已手动处理
            if (!File(cloudflaredPath()).isFile) return@postDelayed
            startTunnel(null)
        }
        main.post { listeners.forEach { l -> l.onTunnelEvent(tunnelState, tunnelUrl, lastTunnelMessage) } }
    }

    /** 手动重试：清空退避状态后立即尝试。 */
    fun retryTunnelManually(onDone: ((ok: Boolean, err: String?) -> Unit)? = null) {
        cancelReconnect(reset = true)
        startTunnel(onDone)
    }

    private fun cancelReconnect(reset: Boolean) {
        reconnectScheduled = false
        main.removeCallbacksAndMessages(RECONNECT_TOKEN)
        if (reset) {
            reconnectAttempt = 0
            reconnectNextAtMs = 0
            reconnectGaveUp = false
        } else {
            reconnectNextAtMs = 0
        }
    }

    /** 退避重连专用 token：removeCallbacksAndMessages 只摘除该 token 的回调，不误伤其他 post。 */
    private val RECONNECT_TOKEN = Object()

    private fun Handler.postDelayed(token: Any, delayMs: Long, action: () -> Unit) {
        val r = Runnable { action() }
        postAtTime(r, token, SystemClock.uptimeMillis() + delayMs)
    }
}

/**
 * LanDetect — 宿主侧局域网 IPv4 枚举（v1.4.0 首页修复③）。
 *
 * 根因（Android 14 x86_64 模拟器 run-as 同 UID 复现）：targetSdk 34+ 的应用 UID 被
 * SELinux 拒绝 netlink RTM_GETLINK，Go 的 net.Interfaces() 报
 * 「route ip+net: netlinkrib: permission denied」，核心 lanListenIP() 恒返回空串，
 * 局域网监听永不建立、首页局域网端点恒「未获取」。
 *
 * java.net.NetworkInterface（libcore 实现）不受该限制，枚举规则与核心 lan.go 同源：
 * up + 非回环，IPv4 且非 link-local；接口名优先级 wlan* > eth* > 其余，同优先级按
 * 接口序号，站点内（RFC1918 site-local）优先。无候选返回 null（调用方不注入）。
 */
object LanDetect {

    fun bestLanIPv4(): String? = runCatching {
        data class Cand(val name: String, val index: Int, val ip: String, val siteLocal: Boolean)
        val cands = mutableListOf<Cand>()
        val nifs = java.net.NetworkInterface.getNetworkInterfaces() ?: return null
        for (nif in nifs.asSequence()) {
            if (!nif.isUp || nif.isLoopback) continue
            for (addr in nif.inetAddresses.asSequence()) {
                if (addr !is java.net.Inet4Address) continue
                if (addr.isLoopbackAddress || addr.isLinkLocalAddress) continue
                val host = addr.hostAddress ?: continue
                cands += Cand(nif.name, nif.index, host, addr.isSiteLocalAddress)
            }
        }
        fun prio(name: String): Int = when {
            name.startsWith("wlan") -> 0
            name.startsWith("eth") -> 1
            else -> 2
        }
        cands.sortedWith(
            compareBy<Cand> { prio(it.name) }
                .thenBy { it.index }
                .thenBy { !it.siteLocal } // 站点内优先
        ).firstOrNull()?.ip
    }.getOrNull()
}

package io.nexport.gateway.app

import io.nexport.gateway.R

import android.content.Context
import android.graphics.Typeface
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.widget.NestedScrollView
import com.google.android.material.progressindicator.LinearProgressIndicator

/**
 * Step2 — 启动核心：动画展示 初始化数据库→SQL 迁移→插件目录→面板就绪。
 * app.Start 在工作线程调用（线程约束），核心日志节流上屏（Go 协程回调 → post 主线程）。
 *
 * 核心就绪后：GET /admin/setup-status → 未初始化则 POST /admin/setup 创建账号 →
 * 自动登录缓存 token；已初始化（备份恢复等）则跳过创建。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class StartCoreFragment : StepFragment() {
    override fun stepIndex() = 2
    override fun titleText() = ctx().getString(R.string.onboard_title_start)

    private val ui get() = Ui
    private lateinit var phaseViews: List<TextView>
    private lateinit var progress: LinearProgressIndicator
    private lateinit var status: TextView
    private lateinit var console: TextView
    private lateinit var consoleScroll: NestedScrollView
    private lateinit var btnPrev: Button
    private lateinit var btnCopyLog: Button
    private lateinit var btnRetry: Button
    private lateinit var btnNext: Button
    private var setupDone = false
    private var listenerAttached = false

    private val listener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            if (!isAdded) return
            when (state) {
                CoreController.State.RUNNING -> {
                    // RUNNING 后满格 → 「核心已启动」（onboardingSpec 第3步：200ms 推进动画）
                    progress.setProgressCompat(100, true)
                    main.postDelayed({ if (isAdded) progress.hide() }, 300)
                    waitStartSince = 0L
                    status.text = ctx().getString(R.string.start_ok)
                    markPhases(4)
                    afterCoreStart()
                }
                CoreController.State.FAILED -> {
                    progress.hide()
                    waitStartSince = 0L
                    status.text = (ctx().getString(R.string.start_failed)) + "\n" + (error ?: "")
                    btnCopyLog.visibility = View.VISIBLE
                    btnRetry.visibility = View.VISIBLE
                    btnPrev.visibility = View.VISIBLE
                }
                else -> {}
            }
        }

        override fun onCoreLog(line: String) {
            if (!isAdded) return
            // 空态占位行「等待核心日志…」首行到达后隐藏（onboardingSpec 第3步）
            if (!firstLogShown) {
                firstLogShown = true
                console.text = ""
                console.setTextColor(Ui.color(console, R.attr.nxTextPrimary))
            }
            // 每行 HH:mm:ss 前缀（onboardingSpec 第3步）：bridge 节流后为批量串
            //（~300ms/32 行，内含 \n），逐行拆分加前缀
            val ts = java.text.SimpleDateFormat("HH:mm:ss", java.util.Locale.US)
                .format(java.util.Date())
            line.split('\n').forEach { l ->
                if (l.isNotBlank()) console.append(ts + " " + l + "\n")
            }
            // 恒定自动滚底（既有行为）
            consoleScroll.post { consoleScroll.fullScroll(View.FOCUS_DOWN) }
            val lower = line.lowercase()
            when {
                lower.contains("core listening") -> markPhases(4)
                lower.contains("plugin") -> markPhases(3)
                lower.contains("migrat") -> markPhases(2)
                lower.contains("open database") || lower.contains("sqlite") -> markPhases(1)
            }
        }
    }

    private var phasesDone = 0
    private var firstLogShown = false
    private var waitStartSince = 0L
    private val main = android.os.Handler(android.os.Looper.getMainLooper())

    /** >10s 未 RUNNING 时 status 追加「仍在启动…（已等待 Xs）」节拍（复用 HomeFragment 模式）。 */
    private val waitTick = object : Runnable {
        override fun run() {
            if (!isAdded) return
            if (CoreController.state == CoreController.State.STARTING && waitStartSince > 0L) {
                val waited = ((System.currentTimeMillis() - waitStartSince) / 1000).toInt()
                if (waited >= 10) {
                    status.text = ctx().getString(R.string.start_hint) + "\n" +
                        ctx().getString(R.string.start_still_starting_fmt, waited)
                }
                main.postDelayed(this, 1000)
            }
        }
    }

    /**
     * 相位推进：进度条 indeterminate → determinate，progress = phasesDone/4×100，
     * 相位推进 200ms 动画（onboardingSpec 第3步）。精度声明：相位推进依赖 onCoreLog
     * 关键词匹配（bridge 节流后为 300ms/32 行批量串，单块多关键词时相位跳变）——
     * determinate 进度条沿用同一匹配、接受该粗粒度（既有行为非本轮引入）。
     */
    private fun markPhases(done: Int) {
        if (done > phasesDone) phasesDone = done
        if (progress.isIndeterminate) {
            progress.isIndeterminate = false
            progress.setProgressCompat(phasesDone * 100 / 4, true)
        } else {
            progress.setProgressCompat(phasesDone * 100 / 4, true)
        }
        val c = ctx()
        phaseViews.forEachIndexed { i, tv ->
            val label = tv.tag.toString().removePrefix("label:")
            when {
                i < phasesDone -> {
                    tv.text = "✔ $label"
                    tv.setTextColor(ContextCompat.getColor(c, R.color.nx_state_ok))
                }
                i == phasesDone -> {
                    tv.text = "● $label"
                    tv.setTextColor(ContextCompat.getColor(c, R.color.nx_state_warn))
                }
                else -> {
                    tv.text = "○ $label"
                    tv.setTextColor(Ui.color(tv, R.attr.nxTextPrimary))
                }
            }
        }
    }

    override fun buildContent(c: Context): View {
        val labels = listOf(
            c.getString(R.string.start_phase_init),
            c.getString(R.string.start_phase_migrate),
            c.getString(R.string.start_phase_plugins),
            c.getString(R.string.start_phase_ready)
        )
        phaseViews = labels.map { l ->
            ui.body(c, "○ $l").apply {
                setTypeface(Typeface.MONOSPACE)
                tag = "label:$l"
                setPadding(0, c.dp(4), 0, c.dp(4))
            }
        }
        progress = LinearProgressIndicator(c).apply { isIndeterminate = true }
        status = ui.hint(c, c.getString(R.string.start_hint))
        console = TextView(c).apply {
            setTypeface(Typeface.MONOSPACE)
            textSize = 11f
            // 空态占位行「等待核心日志…」（首行到达后隐藏，onboardingSpec 第3步）
            text = c.getString(R.string.start_console_placeholder)
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
        }
        consoleScroll = NestedScrollView(c).apply {
            addView(console)
            // 控制台底色改 token（替换旧硬编码 0xFF0F172A）
            setBackgroundColor(Ui.color(this, R.attr.nxCardSurface))
            minimumHeight = c.dp(160)
        }
        btnPrev = ui.button(c, getString(R.string.prev)) {
            (activity as? MainActivity)?.replace(AccountFragment(), addToStack = false)
        }
        btnCopyLog = ui.button(c, getString(R.string.start_copy_log)) {
            copyText(c, CoreController.snapshotLogs() + "\n[error] " + (CoreController.lastError ?: ""))
        }.apply { visibility = View.GONE }
        btnRetry = ui.button(c, getString(R.string.retry)) {
            btnRetry.visibility = View.GONE
            btnPrev.visibility = View.GONE
            progress.show()
            startCore()
        }.apply { visibility = View.GONE }
        btnNext = ui.button(c, getString(R.string.next)) {
            (activity as? MainActivity)?.replace(GuideFragment(), addToStack = false)
        }.apply { visibility = View.GONE }

        val col = ui.spaced(
            progress,
            status,
            consoleScroll,
            *phaseViews.toTypedArray(),
            headerButtonRow(btnPrev, btnCopyLog, btnRetry, btnNext)
        )
        return page(col)
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        if (!listenerAttached) {
            CoreController.addListener(listener)
            listenerAttached = true
        }
        when (CoreController.state) {
            CoreController.State.RUNNING -> {
                markPhases(4); progress.setProgressCompat(100, false); progress.hide()
                status.text = ctx().getString(R.string.start_ok)
                afterCoreStart()
            }
            CoreController.State.STARTING -> {
                if (waitStartSince == 0L) waitStartSince = System.currentTimeMillis()
                main.postDelayed(waitTick, 1000)
            }
            else -> startCore()
        }
    }

    private fun startCore() {
        progress.show()
        status.text = ctx().getString(R.string.start_hint)
        waitStartSince = System.currentTimeMillis()
        main.postDelayed(waitTick, 1000)
        CoreController.start { ok, _ -> if (!ok) { /* 状态回调已处理 */ } }
    }

    /** 核心就绪后：创建管理员账号（若未初始化）→ 自动登录 → 放行下一步。 */
    private fun afterCoreStart() {
        if (setupDone) { btnNext.visibility = View.VISIBLE; return }
        Thread {
            val c = ctx()
            val user = OnboardingState.user
            val pass = OnboardingState.pass
            var note = ""
            val initialized = AdminApi.setupInitialized()
            if (initialized == false && user != null && pass != null) {
                val r = AdminApi.setup(user, pass)
                if (r.isFailure) {
                    main { status.text = c.getString(R.string.setup_failed) + "\n" + (r.exceptionOrNull()?.message ?: "") }
                    return@Thread
                }
            } else if (initialized == true) {
                note = c.getString(R.string.setup_exists)
            }
            if (user != null && pass != null) {
                Prefs.saveAdmin(c, user, pass)
                AdminApi.login(user, pass) // 缓存 token 供状态卡使用；失败不影响引导
            }
            OnboardingState.clear()
            setupDone = true
            main {
                status.text = if (note.isEmpty()) c.getString(R.string.start_ok)
                else c.getString(R.string.start_ok) + "\n" + note
                btnNext.visibility = View.VISIBLE
            }
        }.start()
    }

    private fun main(block: () -> Unit) {
        requireActivity().runOnUiThread(block)
    }

    override fun onDestroyView() {
        if (listenerAttached) { CoreController.removeListener(listener); listenerAttached = false }
        super.onDestroyView()
    }
}

/**
 * Step3 — 使用指南（可跳过，v1.4.0 ⑦ 保活授权卡扩容：电池优化忽略、后台运行/自启动
 * （按厂商能力尽力引导）、悬浮窗保活、通知权限；其余说明卡保留）：
 * ①安装首个插件；②「关于 Go 插件」说明卡；③临时隧道；④后台常驻（BatteryOpt.request）；
 * ④b 自启动与后台运行卡（厂商组件逐一尝试 → 应用详情页兜底）；⑤交互卡×2：通知权限卡
 * （POST_NOTIFICATIONS，拒绝行内降级不阻塞）与悬浮窗保活卡（canDrawOverlays → 跳系统页
 * → onResume 自动复查）。各保活项与设置页「保活与权限」组同步集中（⑦→⑧）。
 */
class GuideFragment : StepFragment() {
    override fun stepIndex() = 3
    override fun titleText() = ctx().getString(R.string.onboard_title_guide)

    private val ui get() = Ui

    // 交互卡状态行
    private var notifStatus: TextView? = null
    private var notifHintText: TextView? = null
    private var btnNotif: androidx.appcompat.widget.AppCompatButton? = null
    private var floatStatus: TextView? = null
    private var floatHintText: TextView? = null
    private var btnFloat: androidx.appcompat.widget.AppCompatButton? = null
    private var pendingFloatingEnable = false

    override fun buildContent(c: Context): View {
        val c1 = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_plugins_title)))
            addView(ui.body(c, c.getString(R.string.guide_plugins_text)))
        }
        // ②「关于 Go 插件」说明卡：当前形态 + 落地后变化预告（纯说明无按钮）
        val cGo = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_goplugin_title)))
            addView(ui.body(c, c.getString(R.string.guide_goplugin_text)))
            addView(ui.hint(c, c.getString(R.string.guide_goplugin_preview)).apply {
                setPadding(0, c.dp(6), 0, 0)
            })
        }
        val c2 = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_tunnel_title)))
            addView(ui.body(c, c.getString(R.string.guide_tunnel_text)))
        }
        // ④-1 后台常驻卡：电池优化忽略
        val c3 = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_bg_title)))
            addView(ui.body(c, c.getString(R.string.guide_bg_text)))
            addView(ui.button(c, c.getString(R.string.guide_battery)) {
                BatteryOpt.request(c)
            }.apply { setPadding(0, c.dp(6), 0, 0) })
        }
        // 局域网监听披露卡（合规复审 2026-09-28 低风险项）：默认开启的边界与关闭入口
        val cLan = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_lan_title)))
            addView(ui.body(c, c.getString(R.string.guide_lan_text)))
        }
        // ④-2 自启动与后台运行卡（v1.4.0 ⑦）：按厂商能力尽力引导
        val cAuto = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_autostart_title)))
            addView(ui.body(c, c.getString(R.string.guide_autostart_text)))
            addView(ui.button(c, c.getString(R.string.guide_autostart_action)) {
                openAutostartSettings(c)
            }.apply { setPadding(0, c.dp(6), 0, 0) })
        }
        // ⑤-1 通知权限卡：状态行（已授权/未授权）+「开启通知」；拒绝→行内降级不阻塞
        val cNotif = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.guide_notif_card_title)))
            notifStatus = ui.secondary(c, "")
            addView(notifStatus!!)
            btnNotif = ui.button(c, c.getString(R.string.notif_banner_enable)) {
                if (android.os.Build.VERSION.SDK_INT >= 33) {
                    cardNotifLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
                }
            }.apply { setPadding(0, c.dp(6), 0, 0) }
            addView(btnNotif!!)
            notifHintText = TextView(c).apply {
                visibility = View.GONE
                textSize = 13f
                setTextColor(Ui.color(this, R.attr.nxStateWarn))
                text = c.getString(R.string.guide_notif_denied_hint)
                setPadding(0, c.dp(4), 0, 0)
            }
            addView(notifHintText!!)
        }
        // ⑤-2 悬浮窗保活卡：=设置页同款 canDrawOverlays → 跳系统页 → onResume 自动复查
        val cFloat = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.setting_floating)))
            floatStatus = ui.secondary(c, "")
            addView(floatStatus!!)
            floatHintText = TextView(c).apply {
                visibility = View.GONE
                textSize = 13f
                setTextColor(Ui.color(this, R.attr.nxStateWarn))
                text = c.getString(R.string.floating_denied_hint)
                setPadding(0, c.dp(4), 0, 0)
            }
            btnFloat = ui.button(c, c.getString(R.string.floating_card_action)) {
                val act = activity ?: return@button
                if (canDrawOverlays(act)) {
                    Prefs.setFloatingKeepAlive(act, true)
                    act.startService(android.content.Intent(act, FloatingWindowService::class.java))
                    refreshCards()
                } else {
                    pendingFloatingEnable = true
                    runCatching {
                        startActivity(android.content.Intent(
                            android.provider.Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                            android.net.Uri.parse("package:${act.packageName}")))
                    }.onFailure {
                        floatHintText?.visibility = View.VISIBLE
                        pendingFloatingEnable = false
                    }
                }
            }.apply { setPadding(0, c.dp(6), 0, 0) }
            addView(btnFloat!!)
            addView(floatHintText!!)
            addView(ui.hint(c, c.getString(R.string.floating_caption)).apply {
                setPadding(0, c.dp(6), 0, 0)
            })
        }
        val btnSkip = ui.button(c, getString(R.string.skip)) { finish() }
        val btnDone = ui.button(c, getString(R.string.done)) { finish() }
        return page(
            c1, cGo, c2, cLan, c3, cAuto, cNotif, cFloat,
            headerButtonRow(btnSkip, btnDone)
        )
    }

    /**
     * 自启动/后台运行按厂商尽力引导（⑦，与设置页「保活与权限」行同源）：
     * 常见厂商自启动管理组件逐一尝试，全部不可达 → 应用详情页兜底。
     */
    private fun openAutostartSettings(c: Context) {
        val vendorTargets = listOf(
            "com.miui.securitycenter" to "com.miui.permcenter.autostart.AutoStartManagementActivity",
            "com.huawei.systemmanager" to "com.huawei.systemmanager.startupmgr.StartupNormalAppListActivity",
            "com.huawei.systemmanager" to "com.huawei.systemmanager.appcontrol.activity.StartupAppControlActivity",
            "com.coloros.safecenter" to "com.coloros.safecenter.permission.startup.StartupAppListActivity",
            "com.oppo.safe" to "com.oppo.safe.permission.startup.StartupAppListActivity",
            "com.vivo.permissionmanager" to "com.vivo.permissionmanager.activity.BgStartUpManagerActivity",
            "com.iqoo.secure" to "com.iqoo.secure.ui.phoneoptimize.BgStartUpManager",
            "com.meizu.safe" to "com.meizu.safe.security.SHOW_APPSEC",
        )
        for ((pkg, cls) in vendorTargets) {
            val i = android.content.Intent().setClassName(pkg, cls)
                .addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
            if (runCatching { c.startActivity(i) }.isSuccess) return
        }
        runCatching {
            c.startActivity(android.content.Intent(
                android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                android.net.Uri.parse("package:${c.packageName}")))
        }.onFailure { toast(c, c.getString(R.string.floating_denied_hint)) }
    }

    private val cardNotifLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (!granted) notifHintText?.visibility = View.VISIBLE
        refreshCards()
    }

    private fun canDrawOverlays(c: Context): Boolean =
        android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.M &&
            android.provider.Settings.canDrawOverlays(c)

    /** 两卡状态刷新（onViewCreated 与 onResume 调用，onboardingSpec ⑤）。 */
    private fun refreshCards() {
        val act = activity ?: return
        val c = ctx()
        // 通知卡
        val notifGranted = (android.os.Build.VERSION.SDK_INT < 33) ||
            (androidx.core.content.ContextCompat.checkSelfPermission(
                act, android.Manifest.permission.POST_NOTIFICATIONS)
                == android.content.pm.PackageManager.PERMISSION_GRANTED)
        notifStatus?.text = c.getString(
            if (notifGranted) R.string.guide_perm_granted else R.string.guide_perm_not_granted)
        notifStatus?.setTextColor(
            Ui.color(notifStatus!!, if (notifGranted) R.attr.nxStateOk else R.attr.nxStateWarn))
        btnNotif?.visibility = if (notifGranted) View.GONE else View.VISIBLE
        // 悬浮窗卡
        val overlayGranted = canDrawOverlays(act)
        val enabled = Prefs.floatingKeepAlive(act)
        if (pendingFloatingEnable) {
            if (overlayGranted) {
                // 从系统授权页返回：已授权 → 顺势置位并启动服务（onboardingSpec ⑤）
                Prefs.setFloatingKeepAlive(act, true)
                act.startService(android.content.Intent(act, FloatingWindowService::class.java))
            } else {
                // 未授权 → 行内 hint，不打断收尾
                floatHintText?.visibility = View.VISIBLE
            }
            pendingFloatingEnable = false
        }
        floatStatus?.text = c.getString(
            when {
                enabled && overlayGranted -> R.string.guide_floating_on
                overlayGranted -> R.string.guide_perm_granted
                else -> R.string.guide_perm_not_granted
            })
        floatStatus?.setTextColor(
            Ui.color(floatStatus!!, if (overlayGranted) R.attr.nxStateOk else R.attr.nxStateWarn))
        btnFloat?.visibility = if (enabled) View.GONE else View.VISIBLE
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        refreshCards()
    }

    override fun onResume() {
        super.onResume()
        refreshCards()
    }

    private fun finish() {
        val act = activity as? MainActivity ?: return
        Prefs.setOnboardingDone(act, true)
        // 通知权限路径②（notificationPermissionSpec）：引导收尾拉起前台服务前，
        // Android 13+ 未授权先走权限流程；拒绝则照常收尾（不静默改 keepForeground，
        // 首页一次性引导卡与设置行保留入口）。
        if (Prefs.keepForeground(act)) {
            if (android.os.Build.VERSION.SDK_INT >= 33 &&
                androidx.core.content.ContextCompat.checkSelfPermission(
                    act, android.Manifest.permission.POST_NOTIFICATIONS)
                != android.content.pm.PackageManager.PERMISSION_GRANTED
            ) {
                guideNotifPermission = { granted ->
                    if (granted) GatewayService.start(act)
                    // 拒绝：不启动服务拉起，keepForeground 保持 true（首页横幅/设置降级文案接管）
                    enterMain(act)
                }
                guideNotifLauncher.launch(android.Manifest.permission.POST_NOTIFICATIONS)
                return
            }
            GatewayService.start(act)
        }
        // 悬浮窗（onboardingSpec 第4步收尾）：引导中已授权则顺势启动服务，未授权不打断
        if (Prefs.floatingKeepAlive(act) && canDrawOverlays(act)) {
            act.startService(android.content.Intent(act, FloatingWindowService::class.java))
        }
        enterMain(act)
    }

    private val guideNotifLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted -> guideNotifPermission?.invoke(granted); guideNotifPermission = null }

    private var guideNotifPermission: ((Boolean) -> Unit)? = null

    private fun enterMain(act: MainActivity) {
        if (CoreController.state == CoreController.State.NOT_STARTED) CoreController.start()
        act.enterMainMode()
    }
}

/** 电池优化跳转（不申请 REQUEST_IGNORE_BATTERY_OPTIMIZATIONS 权限，走系统设置页）。 */
object BatteryOpt {
    fun isIgnoring(c: Context): Boolean {
        val pm = c.getSystemService(Context.POWER_SERVICE) as android.os.PowerManager
        return pm.isIgnoringBatteryOptimizations(c.packageName)
    }

    fun request(c: Context) {
        runCatching {
            c.startActivity(android.content.Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
        }.onFailure {
            toast(c, "settings unavailable")
        }
    }

    private val ui get() = Ui
}

package io.nexport.gateway.app

import io.nexport.gateway.R

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.content.res.Configuration
import android.graphics.PixelFormat
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.TypedValue
import android.view.Gravity
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.dynamicanimation.animation.FloatValueHolder
import androidx.dynamicanimation.animation.SpringAnimation
import androidx.dynamicanimation.animation.SpringForce
import kotlin.math.abs
import kotlin.math.hypot
import kotlin.math.roundToInt

/**
 * FloatingWindowService — 悬浮窗保活（floatingWindowSpec，v1.3.0 重设计）：
 * TYPE_APPLICATION_OVERLAY 进程内状态可视化悬浮窗。
 *
 * 形态（v1.3.0 ①）：44dp 高胶囊（圆角=高/2、半透明 nxCardSurface 底 + nxSeparator 描边，
 * 跟随亮暗主题——主题设置变更整窗重建，颜色经 Ui/attr 解析），内容 = 应用图标 +
 * 网关状态色点 + 隧道状态徽标（建立中/已建立/错误，隧道关闭时隐藏）。
 *
 * 交互（v1.3.0 ①）：
 *  - 拖动 1:1 即时跟手：ACTION_MOVE 即更新 layoutParams.x/y 并 updateViewLayout，
 *    无长按阈值、无迟滞；抬起时若位移 ≤ touchSlop 判定为点按（该 slop 只用于区分
 *    点按与拖动，不参与拖动本身）；
 *  - 点按 = 收缩 ⇄ 展开小型信息卡（网关地址/端口、隧道状态与 URL、快捷停止核心、
 *    打开面板、关闭悬浮窗）；
 *  - 停止拖动 3 秒后 SpringAnimation 吸附到最近竖直边缘并半隐藏（约 60% 滑出屏幕 +
 *    降透明度）；半隐藏态被触摸立即点亮（透明度弹回），从当前锚点 1:1 跟手拖出，
 *    点按则弹回全显位置（不在触摸瞬间跳位——实机验证：跳位会让触摸点瞬间脱窗，
 *    InputDispatcher 立即 ACTION_CANCEL 扼杀拖动）；
 *  - 弹簧参数集中于伴生常量（STIFFNESS/DAMPING/HIDE_ALPHA/HIDE_RATIO/SNAP_DELAY_MS），
 *    可按手感调整。
 *
 * 边界如实（floatingWindowSpec ⑥）：悬浮窗为进程内状态可视化，应用被杀随之消失，
 * 卡片副文案写明「配合前台服务使用，本身不构成额外保活能力」；核心停止/FAILED 显示
 * 灰/红胶囊而非消失；keepForeground 未开时允许单独使用。
 *
 * 授权：SYSTEM_ALERT_WINDOW 走 Settings.canDrawOverlays + ACTION_MANAGE_OVERLAY_PERMISSION
 * （appop 无系统对话框，QA 自动化通道 `adb shell appops set io.nexport.gateway
 * SYSTEM_ALERT_WINDOW allow`；Android 14+ 侧载应用可能触发「受限设置」阻断授权入口，
 * 授权失败路径的降级 hint 由设置页/引导卡覆盖）。
 *
 * Service→Activity 通道（floatingWindowSpec ⑤）：服务内无 Activity 引用，禁止直调
 * showTab——「打开面板」发 Intent(MainActivity, EXTRA_OPEN_TAB=1)，由
 * MainActivity.onNewIntent/onCreate 消费。核心未运行时同样走该机制。
 *
 * 视图主题：Service Context 无 Activity 的 DayNight 主题包装，视图统一用
 * themedContext()（按「外观·主题」设置覆写 uiMode + Theme.NexPort）构建，nx* attr
 * 亮暗正确解析；颜色一律经 Ui.color(view, attr)。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class FloatingWindowService : android.app.Service() {

    companion object {
        /** 胶囊高（dp）与圆角（= 高/2）。 */
        private const val CAPSULE_HEIGHT_DP = 44

        /** 停止拖动后多久吸附贴边（ms）。 */
        private const val SNAP_DELAY_MS = 3_000L

        /** 贴边半隐藏比例：约 60% 滑出屏幕（可见 40%）。 */
        private const val HIDE_RATIO = 0.6f

        /** 半隐藏时透明度（完整显示 = 1f）。 */
        private const val HIDE_ALPHA = 0.45f

        /** 贴边弹簧参数（Q 弹手感，可调）：刚度与阻尼比（<1 过冲回弹）。 */
        private const val SNAP_SPRING_STIFFNESS = 600f
        private const val SNAP_SPRING_DAMPING = 0.55f

        /** 展开卡最宽（dp，floatingWindowSpec ④）。 */
        private const val CARD_MAX_WIDTH_DP = 240
    }

    private val main = Handler(Looper.getMainLooper())
    private var wm: WindowManager? = null
    private var rootView: LinearLayout? = null
    private var curLp: WindowManager.LayoutParams? = null
    private var added = false
    private var expanded = false
    private var hidden = false
    private var builtThemeKey: String? = null

    /** 全显位置记忆（半隐藏弹回目标；拖动/弹回随时更新，Prefs 只存全显位置）。 */
    private var lastFullX = 0
    private var lastFullY = 0

    // 收缩态胶囊
    private lateinit var capsuleBody: LinearLayout
    private lateinit var capsuleBg: GradientDrawable
    private lateinit var capsuleDot: View
    private lateinit var capsuleBadge: TextView
    private lateinit var badgeBg: GradientDrawable

    // 展开态卡片
    private lateinit var cardBody: LinearLayout
    private lateinit var cardStatus: TextView
    private lateinit var cardGateway: TextView
    private lateinit var cardTunnel: TextView
    private lateinit var cardTunnelUrl: TextView

    private var snapAnim: SpringAnimation? = null
    private val snapRunnable = Runnable { snapToEdge() }

    /** 系统手势排除区（API 29+）：贴边半隐藏后胶囊可见部分整个落在系统返回手势的
     *  屏幕边缘抢占区内，不排除的话拖动/滑动会被系统认领并以 ACTION_CANCEL 收场
     *  （实机复现：滑动起点距右缘 30px 即被抢占）。排除上限 200dp/边，胶囊 44dp 高
     *  足额；矩形为视图本地坐标，窗口移动后随帧重报。 */
    private val gestureExclusionRects = listOf(android.graphics.Rect())

    private fun updateGestureExclusion() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) return
        if (!this::capsuleBody.isInitialized) return
        val w = capsuleBody.width
        val h = capsuleBody.height
        if (w <= 0 || h <= 0) return
        gestureExclusionRects[0].set(0, 0, w, h)
        capsuleBody.systemGestureExclusionRects = gestureExclusionRects
    }

    private val listener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            main.post { if (added) refresh() }
        }

        override fun onTunnelEvent(state: String, url: String, message: String) {
            main.post { if (added) refresh() }
        }
    }

    override fun onCreate() {
        super.onCreate()
        CoreController.addListener(listener)
        wm = getSystemService(Context.WINDOW_SERVICE) as WindowManager
        main.post { show() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int = START_STICKY

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        // 屏幕尺寸/旋转变化：整窗重建并重新贴边（位置钳回屏内）
        main.post { if (added) rebuild() }
    }

    override fun onDestroy() {
        main.removeCallbacks(snapRunnable)
        snapAnim?.cancel()
        CoreController.removeListener(listener)
        removeView()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): android.os.IBinder? = null

    // ---- 主题化上下文（nx* attr 亮暗随「外观·主题」设置） ----

    private fun themedContext(): Context {
        val base = when (Prefs.themeMode(this)) {
            "light" -> createConfigurationContext(configurationWithNight(Configuration.UI_MODE_NIGHT_NO))
            "dark" -> createConfigurationContext(configurationWithNight(Configuration.UI_MODE_NIGHT_YES))
            else -> this
        }
        return android.view.ContextThemeWrapper(base, R.style.Theme_NexPort)
    }

    private fun configurationWithNight(mode: Int): Configuration {
        val cfg = Configuration(resources.configuration)
        cfg.uiMode = (cfg.uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or mode
        return cfg
    }

    // ---- 悬浮窗视图 ----

    @SuppressLint("ClickableViewAccessibility")
    private fun show() {
        if (added || wm == null) return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && !Settings.canDrawOverlays(this)) {
            // 授权被撤销（受限设置/用户手动）：不崩溃，停服回到调用方开关回弹态
            stopSelf()
            return
        }
        builtThemeKey = Prefs.themeMode(this)
        val c = themedContext()
        val root = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        capsuleBody = capsule(c)
        cardBody = card(c)
        cardBody.visibility = View.GONE
        root.addView(capsuleBody)
        // 展开卡固定宽 ≤240dp（小屏钳到屏宽-32dp）
        val cardWidth = c.dp(CARD_MAX_WIDTH_DP)
            .coerceAtMost(resources.displayMetrics.widthPixels - c.dp(32))
        root.addView(cardBody, LinearLayout.LayoutParams(cardWidth, ViewGroup.LayoutParams.WRAP_CONTENT))
        // 位置记忆（Prefs.floating_pos_x/y，floatingWindowSpec ①；首启默认屏幕右侧 1/4 高）
        val dm = resources.displayMetrics
        val x = Prefs.floatingPosX(this).takeIf { it != Int.MIN_VALUE }
            ?: (dm.widthPixels - c.dp(CAPSULE_HEIGHT_DP) * 2)
        val y = Prefs.floatingPosY(this).takeIf { it != Int.MIN_VALUE } ?: (dm.heightPixels / 4)
        val lp = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY, // minSdk 26 满足
            WindowManager.LayoutParams.FLAG_NOT_TOUCH_MODAL or
                WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                // 允许窗体越出屏幕边界（贴边半隐藏 60% 滑出必需；实测无此标志时 WM 把
                // 负 x 钳回 0，半隐藏失效。拖动被系统 CANCEL 的真凶是返回手势边缘
                // 抢占区，由 updateGestureExclusion 排除，与此标志无关）
                WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS,
            PixelFormat.TRANSLUCENT
        ).apply {
            gravity = Gravity.TOP or Gravity.START
            this.x = x
            this.y = y
        }
        lastFullX = lp.x
        lastFullY = lp.y
        curLp = lp
        attachDragHandlers(root, lp)
        rootView = root
        wm?.addView(root, lp)
        added = true
        refresh()
        // 首次布局完成后申请手势排除区（贴边胶囊的可触摸条带）
        root.post { if (added) updateGestureExclusion() }
        // 首显同「拖动结束」待遇：3 秒无操作自动贴边
        scheduleSnap()
    }

    private fun removeView() {
        rootView?.let { runCatching { wm?.removeView(it) } }
        rootView = null
        curLp = null
        added = false
    }

    /** 整窗重建（主题设置变更 / 配置变更）：按新主题色重画。 */
    private fun rebuild() {
        if (!added) return
        removeView()
        show()
        if (expanded) {
            expanded = false
            toggleExpand()
        }
    }

    /** 收缩态 44dp 胶囊：应用图标 + 状态点 + 隧道徽标（半透明圆角，随亮暗主题）。 */
    @SuppressLint("ClickableViewAccessibility")
    private fun capsule(c: Context): LinearLayout {
        capsuleBg = GradientDrawable().apply {
            cornerRadius = c.dpF(CAPSULE_HEIGHT_DP / 2)
            // 半透明：卡片底色叠 ~82% 不透明度（保留背后内容可感知）
            setColor((ctxColor(c, R.attr.nxCardSurface) and 0x00FFFFFF) or 0xD1000000.toInt())
            setStroke(c.dp(1).coerceAtLeast(1), ctxColor(c, R.attr.nxSeparator))
        }
        return LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = c.dp(CAPSULE_HEIGHT_DP)
            setPadding(c.dp(10), c.dp(8), c.dp(12), c.dp(8))
            background = capsuleBg
            // 应用图标 22dp
            addView(ImageView(c).apply {
                setImageDrawable(packageManager.getApplicationIcon(applicationInfo))
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            }, LinearLayout.LayoutParams(c.dp(22), c.dp(22)))
            // 网关状态色点 10dp（refresh 按核心/隧道态着色）
            capsuleDot = View(c).apply {
                layoutParams = LinearLayout.LayoutParams(c.dp(10), c.dp(10)).apply {
                    marginStart = c.dp(8)
                }
                background = GradientDrawable().apply { shape = GradientDrawable.OVAL }
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            }
            addView(capsuleDot)
            // 隧道状态徽标：小圆角 pill，10sp 状态词（隧道关闭时隐藏）
            badgeBg = GradientDrawable().apply { cornerRadius = c.dpF(9) }
            capsuleBadge = TextView(c).apply {
                textSize = 10f
                setTypeface(Typeface.DEFAULT_BOLD)
                background = badgeBg
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                visibility = View.GONE
            }
            addView(capsuleBadge, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT
            ).apply { marginStart = c.dp(8) })
        }
    }

    /** 展开态小型信息卡（宽 ≤240dp；Ui.card 同款 12dp 圆角/nxCardSurface/nxSeparator 描边）。 */
    private fun card(c: Context): LinearLayout {
        return LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(14), c.dp(12), c.dp(14), c.dp(10))
            background = strokeBg(c, radiusDp = 12)
            minimumWidth = c.dp(200)
            addView(TextView(c).apply {
                text = c.getString(R.string.app_name)
                textSize = 17f
                setTypeface(Typeface.DEFAULT_BOLD)
                setTextColor(Ui.color(this, R.attr.nxTextPrimary))
            })
            cardStatus = TextView(c).apply {
                textSize = 13f
                setPadding(0, c.dp(4), 0, 0)
            }
            addView(cardStatus)
            addView(TextView(c).apply {
                text = c.getString(R.string.core_gateway_addr)
                textSize = 12f
                setTextColor(Ui.color(this, R.attr.nxTextTertiary))
                setPadding(0, c.dp(8), 0, 0)
            })
            cardGateway = TextView(c).apply {
                textSize = 13f
                setTypeface(Typeface.MONOSPACE)
                setTextColor(Ui.color(this, R.attr.nxTextSecondary))
                setPadding(0, c.dp(1), 0, 0)
                setTextIsSelectable(true)
                setOnClickListener { copyGateway() }
                setOnLongClickListener { copyGateway(); true }
            }
            addView(cardGateway)
            cardTunnel = TextView(c).apply {
                textSize = 12f
                setTextColor(Ui.color(this, R.attr.nxTextTertiary))
                setPadding(0, c.dp(8), 0, 0)
            }
            addView(cardTunnel)
            cardTunnelUrl = TextView(c).apply {
                textSize = 12f
                setTypeface(Typeface.MONOSPACE)
                setTextColor(Ui.color(this, R.attr.nxAccent))
                maxWidth = c.dp(CARD_MAX_WIDTH_DP - 28)
                visibility = View.GONE
                setTextIsSelectable(true)
                setOnLongClickListener { copyTunnelUrl(); true }
                setOnClickListener { copyTunnelUrl() }
            }
            addView(cardTunnelUrl)
            addView(TextView(c).apply {
                // 边界如实（floatingWindowSpec ⑥）：不承诺「防杀/保活增效」
                text = c.getString(R.string.floating_caption)
                textSize = 10f
                setTextColor(Ui.color(this, R.attr.nxTextTertiary))
                setPadding(0, c.dp(6), 0, 0)
            })
            // 底部小按钮行：打开面板（Intent 通道，禁止直调 showTab）+ 快捷停止 + 关闭悬浮窗
            // （三按钮各占 1/3 宽、文字居中——卡片内宽约 212dp，均分后 5 字按钮不溢出）
            val actions = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
            fun actionCell(text: String, action: () -> Unit) {
                actions.addView(smallButton(c, text, action), LinearLayout.LayoutParams(
                    0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            }
            actionCell(c.getString(R.string.open_panel_tab)) {
                val i = Intent(this@FloatingWindowService, MainActivity::class.java).apply {
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
                    putExtra(MainActivity.EXTRA_OPEN_TAB, 1)
                }
                startActivity(i)
            }
            // v1.3.0 ①：快捷停止核心（与首页「停止核心」同 CoreController.stop 通道）
            actionCell(c.getString(R.string.floating_quick_stop)) {
                CoreController.stop { ok, err ->
                    if (!ok) toast(this@FloatingWindowService,
                        getString(R.string.core_action_failed, err ?: ""))
                }
            }
            actionCell(c.getString(R.string.floating_close)) {
                Prefs.setFloatingKeepAlive(this@FloatingWindowService, false)
                stopSelf()
            }
            addView(actions.apply { setPadding(0, c.dp(4), 0, 0) })
        }
    }

    /** 信息卡小按钮：文字居中（配合三等分权重行），涟漪背景。 */
    private fun smallButton(c: Context, text: String, action: () -> Unit): TextView =
        TextView(c).apply {
            this.text = text
            textSize = 13f
            gravity = Gravity.CENTER
            setTextColor(Ui.color(this, R.attr.nxAccent))
            setPadding(0, c.dp(8), 0, c.dp(4))
            val tv = TypedValue()
            c.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
            setBackgroundResource(tv.resourceId)
            setOnClickListener { action() }
        }

    private fun copyGateway() {
        val port = CoreController.gatewayPort
        if (port > 0) {
            copyText(this, "127.0.0.1:$port")
        } else {
            Toast.makeText(this, getString(R.string.core_state_not_started), Toast.LENGTH_SHORT).show()
        }
    }

    private fun copyTunnelUrl() {
        if (CoreController.tunnelUrl.isNotEmpty()) copyText(this, CoreController.tunnelUrl)
    }

    /** 卡面底色 + 1dp 描边（nxCardSurface / nxSeparator，Ui.card 同源 token）。 */
    private fun strokeBg(c: Context, radiusDp: Int): GradientDrawable = GradientDrawable().apply {
        cornerRadius = c.dpF(radiusDp)
        setColor(ctxColor(c, R.attr.nxCardSurface))
        setStroke(c.dp(1).coerceAtLeast(1), ctxColor(c, R.attr.nxSeparator))
    }

    /** Context 级 attr 解析（Ui.color 需要 View；此处直接经 theme 解析同一 token）。 */
    private fun ctxColor(c: Context, attr: Int): Int {
        val tv = TypedValue()
        c.theme.resolveAttribute(attr, tv, true)
        return tv.data
    }

    /** 状态刷新（主线程）：核心四态 + 隧道态（floatingWindowSpec ④ 状态映射）。 */
    private fun refresh() {
        if (!added) return
        // 「外观·主题」设置变更：整窗重建让胶囊底/描边跟随亮暗
        if (Prefs.themeMode(this) != builtThemeKey) {
            main.post { if (added) rebuild() }
            return
        }
        val c = themedContext()
        val (statusText, colorAttr) = when {
            CoreController.state == CoreController.State.RUNNING ->
                c.getString(R.string.core_state_running) to R.attr.nxStateOk
            CoreController.state == CoreController.State.STARTING ->
                c.getString(R.string.core_state_starting) to R.attr.nxStateWarn
            CoreController.state == CoreController.State.FAILED ->
                c.getString(R.string.core_state_failed) to R.attr.nxStateErr
            else -> c.getString(R.string.core_state_not_started) to R.attr.nxTextTertiary
        }
        // 胶囊点：绿=运行中、黄=启动中/隧道建立中、红=隧道错误/失败、灰=未启动
        val dotColorAttr = when {
            CoreController.state == CoreController.State.RUNNING && CoreController.tunnelState == "error" -> R.attr.nxStateErr
            CoreController.state == CoreController.State.RUNNING && CoreController.tunnelState == "starting" -> R.attr.nxStateWarn
            else -> colorAttr
        }
        val dotColor = Ui.color(capsuleDot, dotColorAttr)
        (capsuleDot.background as? GradientDrawable)?.setColor(dotColor)
        // 隧道徽标：starting/active/error 显示（建立中/已建立/错误），关闭时隐藏
        val st = CoreController.tunnelState
        if (st == "starting" || st == "active" || st == "error") {
            val (badgeText, badgeAttr) = when (st) {
                "active" -> c.getString(R.string.floating_tunnel_badge_active) to R.attr.nxStateOk
                "starting" -> c.getString(R.string.floating_tunnel_badge_starting) to R.attr.nxStateWarn
                else -> c.getString(R.string.floating_tunnel_badge_error) to R.attr.nxStateErr
            }
            val badgeColor = Ui.color(capsuleBadge, badgeAttr)
            badgeBg.setColor((badgeColor and 0x00FFFFFF) or 0x33000000)
            capsuleBadge.setTextColor(badgeColor)
            capsuleBadge.text = badgeText
            capsuleBadge.visibility = View.VISIBLE
        } else {
            capsuleBadge.visibility = View.GONE
        }
        // 无障碍：胶囊朗读应用名 + 核心状态
        capsuleBody.contentDescription =
            c.getString(R.string.app_name) + " · " + statusText
        // 卡片内容
        cardStatus.text = statusText
        cardStatus.setTextColor(Ui.color(cardStatus, colorAttr))
        val port = CoreController.gatewayPort
        cardGateway.text = if (port > 0) "127.0.0.1:$port" else "-"
        cardTunnel.text = when (st) {
            "active" -> c.getString(R.string.tunnel_active)
            "starting" -> c.getString(R.string.tunnel_starting)
            "error" -> c.getString(R.string.tunnel_state_error)
            else -> c.getString(R.string.tunnel_off)
        }
        cardTunnel.setTextColor(
            Ui.color(cardTunnel, when (st) {
                "active" -> R.attr.nxStateOk
                "starting" -> R.attr.nxStateWarn
                "error" -> R.attr.nxStateErr
                else -> R.attr.nxTextTertiary
            }))
        cardTunnelUrl.text = CoreController.tunnelUrl
        cardTunnelUrl.visibility =
            if (st == "active" && CoreController.tunnelUrl.isNotEmpty()) View.VISIBLE else View.GONE
    }

    // ---- 触摸（v1.3.0 ①）：1:1 即时跟手拖动；位移 ≤ touchSlop 判点按（收缩 ⇄ 展开）；
    //      半隐藏态触摸立即点亮并从锚点跟手拖出（点按弹回全显位置）；停止拖动 3s 后
    //      弹簧吸附最近竖直边缘并半隐藏 ----

    @SuppressLint("ClickableViewAccessibility")
    private fun attachDragHandlers(root: LinearLayout, lp: WindowManager.LayoutParams) {
        var downRawX = 0f
        var downRawY = 0f
        var downLpX = 0
        var downLpY = 0
        var movedDist = 0f
        val touchSlop = ViewConfiguration.get(this).scaledTouchSlop.toFloat()
        root.setOnTouchListener { v, ev ->
            when (ev.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    cancelSnap()
                    // 触摸立即脱离半隐藏（透明度弹回；位置从当前锚点 1:1 跟手）
                    if (hidden) grabFromHidden()
                    downRawX = ev.rawX
                    downRawY = ev.rawY
                    downLpX = lp.x
                    downLpY = lp.y
                    movedDist = 0f
                    true
                }
                MotionEvent.ACTION_MOVE -> {
                    // 1:1 即时跟手：MOVE 即更新位置，无阈值无迟滞
                    val dx = ev.rawX - downRawX
                    val dy = ev.rawY - downRawY
                    movedDist = maxOf(movedDist, hypot(dx, dy))
                    lp.x = clampX(downLpX + dx.roundToInt(), lp)
                    lp.y = clampY(downLpY + dy.roundToInt(), lp)
                    updateWindow()
                    true
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    if (ev.actionMasked == MotionEvent.ACTION_UP && movedDist <= touchSlop) {
                        // 点按：先弹回全显位置（此刻无活跃手势，跳转不脱窗），再收缩 ⇄ 展开
                        val l = curLp
                        if (l != null) {
                            lastFullY = clampY(lastFullY, l)
                            l.y = lastFullY
                            l.x = clampX(lastFullX, l)
                            updateWindow()
                        }
                        toggleExpand() // 点按：收缩态 ⇄ 展开态
                    } else if (ev.actionMasked == MotionEvent.ACTION_UP) {
                        // 拖动结束：当前位置即全显位置，记忆并落盘，3 秒后弹簧贴边
                        lastFullX = lp.x
                        lastFullY = lp.y
                        Prefs.setFloatingPos(v.context, lp.x, lp.y)
                        scheduleSnap()
                    } else {
                        // 取消（来电/多窗等）：仅复位状态，不落盘
                        lastFullX = lp.x
                        lastFullY = lp.y
                        scheduleSnap()
                    }
                    true
                }
                else -> false
            }
        }
    }

    /** x 钳制：收缩态至少 40% 可见（左侧 -60% 宽 ～ 右侧屏宽-40% 宽）；
     *  展开态信息卡完整可见（8dp 边距），杜绝展开后被 60% 规则推出屏外。 */
    private fun clampX(x: Int, lp: WindowManager.LayoutParams): Int {
        val w = rootView?.width?.takeIf { it > 0 } ?: return x
        val screenW = resources.displayMetrics.widthPixels
        return if (expanded) {
            val m = dp(8)
            x.coerceIn(m, (screenW - w - m).coerceAtLeast(m))
        } else {
            x.coerceIn(-(w * HIDE_RATIO).toInt(), screenW - (w * (1f - HIDE_RATIO)).toInt())
        }
    }

    /** y 钳制：收缩态整窗保持在屏幕竖直范围内；展开态整卡可见（8dp 边距）。 */
    private fun clampY(y: Int, lp: WindowManager.LayoutParams): Int {
        val h = rootView?.height?.takeIf { it > 0 } ?: return y
        val screenH = resources.displayMetrics.heightPixels
        return if (expanded) {
            val m = dp(8)
            y.coerceIn(m, (screenH - h - m).coerceAtLeast(m))
        } else {
            y.coerceIn(0, (screenH - h).coerceAtLeast(0))
        }
    }

    private fun updateWindow() {
        val r = rootView ?: return
        val lp = curLp ?: return
        runCatching { wm?.updateViewLayout(r, lp) }
        updateGestureExclusion() // 窗口随帧移动，排除区按当前视图尺寸持续重报
    }

    /** 半隐藏态被触摸：立即恢复完整透明度并脱离「半隐藏」状态；位置保持不动——
     *  此刻若跳回全显位置，触摸点会瞬间脱窗、InputDispatcher 立刻 ACTION_CANCEL，
     *  后续 1:1 拖动全被扼杀（实机复现）。窗口从当前锚点跟手拖出（1:1），点按时
     *  再弹回全显位置。 */
    private fun grabFromHidden() {
        hidden = false
        rootView?.animate()?.alpha(1f)?.setDuration(120)?.start()
    }

    private fun scheduleSnap() {
        main.removeCallbacks(snapRunnable)
        if (!expanded) main.postDelayed(snapRunnable, SNAP_DELAY_MS)
    }

    private fun cancelSnap() {
        main.removeCallbacks(snapRunnable)
        snapAnim?.cancel()
        snapAnim = null
    }

    /** 弹簧吸附最近竖直边缘并半隐藏（约 60% 滑出 + 降透明度）。展开态不吸附。 */
    private fun snapToEdge() {
        if (!added || expanded) return
        val r = rootView ?: return
        val w = r.width.takeIf { it > 0 } ?: return
        val lp = curLp ?: return
        val screenW = resources.displayMetrics.widthPixels
        // 以窗口中心 x 判定最近竖直边缘
        val targetX = if (lp.x + w / 2 < screenW / 2)
            -(w * HIDE_RATIO).roundToInt()
        else
            screenW - (w * (1f - HIDE_RATIO)).roundToInt()
        if (abs(targetX - lp.x) < 2) {
            finishHide()
            return
        }
        animateXTo(targetX.toFloat(), SNAP_SPRING_STIFFNESS, SNAP_SPRING_DAMPING) { finishHide() }
        r.animate().alpha(HIDE_ALPHA).setDuration(200).start()
    }

    private fun finishHide() {
        hidden = true
        val lp = curLp ?: return
        val r = rootView ?: return
        val w = r.width.takeIf { it > 0 } ?: return
        val screenW = resources.displayMetrics.widthPixels
        lp.x = if (lp.x + w / 2 < screenW / 2) -(w * HIDE_RATIO).roundToInt()
        else screenW - (w * (1f - HIDE_RATIO)).roundToInt()
        updateWindow()
    }

    /** SpringAnimation 驱动 lp.x（每帧 updateViewLayout；onEnd 精确落位）。 */
    private fun animateXTo(target: Float, stiffness: Float, damping: Float, onEnd: () -> Unit = {}) {
        val lp = curLp ?: return
        snapAnim?.cancel()
        val from = lp.x.toFloat()
        if (abs(target - from) < 1f) {
            lp.x = target.roundToInt()
            updateWindow()
            onEnd()
            return
        }
        val anim = SpringAnimation(FloatValueHolder(from)).apply {
            spring = SpringForce(target).apply {
                this.stiffness = stiffness
                this.dampingRatio = damping
            }
            minimumVisibleChange = 0.5f
            addUpdateListener { _, value, _ ->
                val r = rootView ?: return@addUpdateListener
                val l = curLp ?: return@addUpdateListener
                l.x = value.roundToInt()
                runCatching { wm?.updateViewLayout(r, l) }
            }
            addEndListener { _, canceled, _, _ ->
                snapAnim = null
                if (!canceled) {
                    val l = curLp ?: return@addEndListener
                    l.x = target.roundToInt()
                    updateWindow()
                    onEnd()
                }
            }
        }
        snapAnim = anim
        anim.start()
    }

    private fun toggleExpand() {
        expanded = !expanded
        if (expanded) {
            cancelSnap() // 展开态不贴边
            hidden = false
            rootView?.animate()?.alpha(1f)?.setDuration(120)?.start()
            refresh()
        } else {
            scheduleSnap()
        }
        capsuleBody.visibility = if (expanded) View.GONE else View.VISIBLE
        cardBody.visibility = if (expanded) View.VISIBLE else View.GONE
        // 尺寸变化后钳回屏内（贴右缘展开时卡片保持可见）
        rootView?.post {
            rootView?.let { r ->
                curLp?.let { lp ->
                    lp.x = clampX(lp.x, lp)
                    lp.y = clampY(lp.y, lp)
                    runCatching { wm?.updateViewLayout(r, lp) }
                }
            }
        }
        updateWindow()
    }
}

package io.nexport.gateway.app

import io.nexport.gateway.R

import android.content.Intent
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import androidx.activity.OnBackPressedCallback
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.fragment.app.Fragment
import androidx.fragment.app.FragmentManager
import com.google.android.material.bottomnavigation.BottomNavigationView

/**
 * MainActivity — 单 Activity 壳：分步可视化引导（EULA → 账号 → 启动核心 → 指南，
 * 全程可回退）+ 底部四页签主界面（首页/面板/设置/关于）+ 全屏 overlay 二级页。
 *
 * v1.1.0 容器与导航架构（panelSpec 容器策略）：
 *  - 双容器：页签容器 CONTAINER_ID 承载四个页签 Fragment（show/hide 切换，绝不
 *    replace——防 WebView 销毁）；全屏 overlay 容器 OVERLAY_ID 叠于页签容器之上
 *    承载二级页（LICENSE/THIRD_PARTY_NOTICES/协议/声明/使用说明/修改密码/日志），
 *    overlay 激活时隐藏底部导航（全屏语义）；
 *  - 页签状态恢复：onCreate 恢复路径读取 onSaveInstanceState 的 selectedTab，
 *    重新 apply show/hide 与 NavigationBar 选中态（主题/语言切换必经）；
 *  - 预测性返回（accessibilitySpec）：开 = 注册 OnBackAnimationCallback（参与系统
 *    预测式返回动画路径）；关 = 普通 OnBackPressedCallback（立即 pop/finish）。
 *    manifest 的 android:enableOnBackInvokedCallback 为安装期静态标志，系统级
 *    返回手势动画无法经运行时开关关闭（设置副文案如实说明）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class MainActivity : BaseActivity() {

    private lateinit var tabContainer: FrameLayout
    private lateinit var overlayContainer: FrameLayout
    private lateinit var navBar: BottomNavigationView
    private var selectedTab = -1

    private var backCallback: OnBackPressedCallback? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val root = FrameLayout(this)
        val mainColumn = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        tabContainer = FrameLayout(this).apply {
            id = CONTAINER_ID
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f)
        }
        navBar = BottomNavigationView(this).apply {
            inflateMenu(R.menu.bottom_nav)
            visibility = View.GONE // 引导流程期间隐藏，enterMainMode/恢复页签时显示
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            setOnItemSelectedListener { item ->
                val idx = when (item.itemId) {
                    R.id.nav_home -> 0; R.id.nav_panel -> 1; R.id.nav_settings -> 2; R.id.nav_about -> 3
                    else -> return@setOnItemSelectedListener false
                }
                showTab(idx)
                true
            }
        }
        mainColumn.addView(tabContainer)
        mainColumn.addView(navBar)
        root.addView(mainColumn)
        overlayContainer = FrameLayout(this).apply {
            id = OVERLAY_ID
            visibility = View.GONE
            // 二级页底色：不设则透明，底层页签内容透出与正文重叠（QA 回归修复）
            setBackgroundColor(Ui.color(this, R.attr.nxPageBg))
        }
        root.addView(overlayContainer)
        setContentView(root)

        // 状态栏 insets 修复（statusBarSpec）：targetSdk 35 → Android 15 强制 edge-to-edge，
        // values/themes.xml 的 android:statusBarColor 在 API 35+ 被忽略，全代码化 UI 无
        // insets 处理 → 35+ 顶部内容伸进状态栏。单点安装在根 FrameLayout：
        // top=max(状态栏, 刘海)、bottom=max(导航条, 键盘)（合并 type 的 getInsets 返回并
        // 集 max，adjustResize 输入框不被键盘遮挡），CONSUMED 阻止向子视图二次分发。
        // 双 regime 无需版本分支：API 26-34 框架默认 decorFitsSystemWindows=true，root
        // 收到的 systemBars insets 已被框架消费（padding=0），无双重内缩。
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val t = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or
                    WindowInsetsCompat.Type.displayCutout() or
                    WindowInsetsCompat.Type.ime())
            v.setPadding(0, t.top, 0, t.bottom)
            WindowInsetsCompat.CONSUMED
        }

        installBackPolicy()
        supportFragmentManager.addOnBackStackChangedListener {
            closeOverlayIfEmpty()
            updateBackCallback() // v1.4.0 ⑨：overlay 栈深变化 → 重判回调启用层级
        }

        if (savedInstanceState != null) {
            val tab = savedInstanceState.getInt(KEY_SELECTED_TAB, -1)
            if (tab >= 0) {
                selectedTab = tab
                navBar.visibility = View.VISIBLE
                if (!restoreTabs(tab)) installTabs(tab) else navBar.menu.getItem(tab).isChecked = true
            }
            // overlay 若在栈上（主题/语言切换时开着二级页），恢复其可见性
            if (supportFragmentManager.findFragmentById(OVERLAY_ID) != null) {
                overlayContainer.visibility = View.VISIBLE
                navBar.visibility = View.GONE
            }
            // 引导流程恢复：FragmentManager 自行恢复（原逻辑不变）
            handleOpenIntent(intent)
            return
        }
        when {
            !Prefs.eulaAccepted(this) -> replace(EulaFragment(), addToStack = false)
            !Prefs.onboardingDone(this) -> replace(AccountFragment(), addToStack = false)
            else -> { enterMainMode() }
        }
        // 悬浮窗「打开面板」通道（floatingWindowSpec ⑤）：onCreate 冷启动消费 extra
        handleOpenIntent(intent)
    }

    /**
     * Service→Activity 通道：FloatingWindowService「打开面板」无 Activity 引用，禁止直调
     * showTab——发 Intent(EXTRA_OPEN_TAB[, EXTRA_OPEN_ROUTE])，这里统一消费（与首页快捷
     * 操作共用 PanelFragment.openRoute 的 pendingRoute 单一路径）。核心未运行时同样走
     * showTab(1)+openRoute（面板占位态提供启动按钮，RUNNING 后自动导航）。
     */
    private fun handleOpenIntent(intent: Intent?) {
        if (intent == null) return
        if (!Prefs.onboardingDone(this) || !Prefs.eulaAccepted(this)) return // 引导期不消费
        val tab = intent.getIntExtra(EXTRA_OPEN_TAB, -1)
        if (tab !in 0 until TAB_COUNT) return
        if (supportFragmentManager.findFragmentByTag(tabTag(tab)) == null) {
            // 冷启动兜底：页签未安装时先装（enterMainMode 已装则不进此分支）
            navBar.visibility = View.VISIBLE
            installTabs(tab)
        } else {
            showTab(tab)
        }
        val route = intent.getStringExtra(EXTRA_OPEN_ROUTE)
        if (tab == 1 && !route.isNullOrBlank()) {
            (supportFragmentManager.findFragmentByTag(tabTag(1)) as? PanelFragment)?.openRoute(route)
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleOpenIntent(intent)
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putInt(KEY_SELECTED_TAB, selectedTab)
    }

    /** 引导完成后的常规入口：自动拉起核心；若设置了后台常驻则同时拉起前台服务。 */
    private fun startCoreIfConfigured() {
        if (CoreController.state == CoreController.State.NOT_STARTED) {
            CoreController.start()
        }
        if (Prefs.keepForeground(this)) GatewayService.start(this)
    }

    // ---- 引导流程容器（沿用 replace；引导期间无底部导航） ----

    fun replace(f: Fragment, addToStack: Boolean = true) {
        supportFragmentManager.beginTransaction()
            .replace(CONTAINER_ID, f)
            .apply { if (addToStack) addToBackStack(null) }
            .commit()
    }

    /** 清空返回栈并切换（引导收尾 / 主界面互跳用）。 */
    fun clearAndGo(f: Fragment) {
        supportFragmentManager.popBackStack(null, FragmentManager.POP_BACK_STACK_INCLUSIVE)
        supportFragmentManager.beginTransaction().replace(CONTAINER_ID, f).commit()
    }

    /** 引导收尾：清栈、隐藏引导页、安装四页签并显示底部导航。 */
    fun enterMainMode() {
        supportFragmentManager.popBackStack(null, FragmentManager.POP_BACK_STACK_INCLUSIVE)
        supportFragmentManager.executePendingTransactions()
        supportFragmentManager.findFragmentById(CONTAINER_ID)?.let {
            supportFragmentManager.beginTransaction().remove(it).commitNowAllowingStateLoss()
        }
        navBar.visibility = View.VISIBLE
        startCoreIfConfigured()
        installTabs(0)
    }

    // ---- 四页签（show/hide，绝不 replace） ----

    private fun tabFactory(index: Int): Fragment = when (index) {
        0 -> HomeFragment()
        1 -> PanelFragment()
        2 -> SettingsFragment()
        else -> AboutFragment()
    }

    private fun tabTag(index: Int): String = "tab_$index"

    private fun installTabs(initial: Int) {
        val tx = supportFragmentManager.beginTransaction()
        for (i in 0 until TAB_COUNT) tx.add(CONTAINER_ID, tabFactory(i), tabTag(i))
        tx.commitNowAllowingStateLoss()
        showTab(initial)
    }

    /** 重建恢复：FragmentManager 已还原四页签，重放 show/hide 与导航选中态。 */
    private fun restoreTabs(initial: Int): Boolean {
        val frags = (0 until TAB_COUNT).map { supportFragmentManager.findFragmentByTag(tabTag(it)) }
        if (frags.any { it == null }) return false
        val tx = supportFragmentManager.beginTransaction()
        frags.forEachIndexed { i, f -> if (i == initial) tx.show(f!!) else tx.hide(f!!) }
        tx.commitNowAllowingStateLoss()
        selectedTab = initial
        return true
    }

    fun showTab(index: Int) {
        if (selectedTab == index && supportFragmentManager.findFragmentByTag(tabTag(index)) != null) {
            // 已在该页签：仅确保可见性正确（overlay 关闭后返回等场景）
            supportFragmentManager.beginTransaction()
                .show(supportFragmentManager.findFragmentByTag(tabTag(index))!!).commitNowAllowingStateLoss()
            return
        }
        for (i in 0 until TAB_COUNT) {
            val f = supportFragmentManager.findFragmentByTag(tabTag(i)) ?: continue
            val tx = supportFragmentManager.beginTransaction()
            if (i == index) tx.show(f) else tx.hide(f)
            tx.commitNowAllowingStateLoss()
        }
        selectedTab = index
        if (navBar.menu.getItem(index).isChecked.not()) navBar.menu.getItem(index).isChecked = true
        // 切页可感知（accessibilitySpec）：目标页根视图声明 pane 标题
        supportFragmentManager.findFragmentByTag(tabTag(index))?.view?.let { v ->
            androidx.core.view.ViewCompat.setAccessibilityPaneTitle(v, tabTitle(index))
            v.announceForAccessibility(tabTitle(index))
        }
        updateBackCallback() // v1.4.0 ⑨：页签切换 → 重判面板可回退层级
    }

    fun currentTab(): Int = selectedTab

    private fun tabTitle(index: Int): String = when (index) {
        0 -> getString(R.string.tab_home)
        1 -> getString(R.string.tab_panel)
        2 -> getString(R.string.tab_settings)
        else -> getString(R.string.tab_about)
    }

    // ---- 全屏 overlay 二级页 ----

    /** 打开二级页（协议/声明/许可/使用说明/修改密码/日志）。栈返回 = popBackStack 回页签。 */
    fun openOverlay(f: Fragment) {
        navBar.visibility = View.GONE
        overlayContainer.visibility = View.VISIBLE
        supportFragmentManager.beginTransaction()
            .replace(OVERLAY_ID, f)
            .addToBackStack(null)
            .commit()
    }

    fun popOverlay() {
        if (supportFragmentManager.backStackEntryCount > 0) {
            supportFragmentManager.popBackStack()
        }
    }

    /** 二级页页内返回按钮：overlay 在栈上则 pop，否则按返回策略收尾。 */
    fun popOverlayOrFinish() {
        if (supportFragmentManager.backStackEntryCount > 0) {
            supportFragmentManager.popBackStack()
        } else {
            dispatchBack()
        }
    }

    fun isOverlayActive(): Boolean = overlayContainer.visibility == View.VISIBLE

    private fun closeOverlayIfEmpty() {
        if (supportFragmentManager.findFragmentById(OVERLAY_ID) == null) {
            overlayContainer.visibility = View.GONE
            resetOverlayTransform() // v1.4.0 ⑨：overlay 关闭后复位预测式返回变换
            if (Prefs.onboardingDone(this) && Prefs.eulaAccepted(this)) navBar.visibility = View.VISIBLE
        }
    }

    // ---- 返回策略（appearance_predictive_back / v1.4.0 ⑨ 预测性返回修复） ----

    /**
     * v1.4.0 ⑨：enableOnBackInvokedCallback=true 之下为「各级导航」注册参与预测式
     * 返回的回调（OnBackPressedCallback 自 activity 1.8 起实现 OnBackAnimationCallback，
     * dispatcher 会按动画回调向系统注册 OnBackInvokedCallback）：
     *  - 二级页/说明页在 overlay 栈上：回调启用 + 覆写 back 动画事件——handleOnBackProgressed
     *    让 overlay 容器随手势进度缩放/平移（按 swipeEdge 贴边），松手提交 → popBackStack，
     *    取消 → 动画复位（模拟器手势导航下有预测性返回效果）；
     *  - 面板页签 WebView 可回退：回调启用，提交 → PanelFragment.onBack()（WebView 内容
     *    无法逐帧联动，参与提交通路即可）；
     *  - 其余层级（页签根部/引导步）：回调禁用 → 系统接管（enableOnBackInvokedCallback=true
     *    时返回手势由系统执行预测式返回到桌面动画）。
     * 设置项 predictiveBack 仅控制壳内动画（Progressed/Cancelled 覆写），关闭时提交行为
     * 不变（即时 pop）。manifest 的 enableOnBackInvokedCallback 为安装期静态标志，系统级
     * 手势动画本身无法经运行时开关关闭（设置副文案与 FAQ 已如实说明）。
     */
    fun installBackPolicy() {
        backCallback?.remove()
        val cb: OnBackPressedCallback = object : OnBackPressedCallback(false) {
            override fun handleOnBackPressed() { dispatchBack() }
            override fun handleOnBackStarted(backEvent: androidx.activity.BackEventCompat) {
                if (Prefs.predictiveBack(this@MainActivity)) super.handleOnBackStarted(backEvent)
            }
            override fun handleOnBackProgressed(backEvent: androidx.activity.BackEventCompat) {
                if (Prefs.predictiveBack(this@MainActivity) && isOverlayActive()) {
                    animateOverlayBack(backEvent)
                }
            }
            override fun handleOnBackCancelled() {
                if (Prefs.predictiveBack(this@MainActivity)) resetOverlayTransform()
            }
        }
        onBackPressedDispatcher.addCallback(this, cb)
        backCallback = cb
        updateBackCallback()
    }

    /** 按当前导航层级启用/禁用回调（各级导航注册 OnBackInvokedCallback 的判定点）。 */
    fun updateBackCallback() {
        val overlayOpen = supportFragmentManager.backStackEntryCount > 0
        val panelCanBack = selectedTab == 1 &&
            (supportFragmentManager.findFragmentByTag("tab_1") as? PanelFragment)?.canGoBack() == true
        backCallback?.isEnabled = overlayOpen || panelCanBack
    }

    /** 面板 WebView 历史变化通知（PanelFragment.doUpdateVisitedHistory → 此处重判）。 */
    fun onPanelHistoryChanged() { updateBackCallback() }

    private fun dispatchBack() {
        if (supportFragmentManager.backStackEntryCount > 0) {
            resetOverlayTransform()
            supportFragmentManager.popBackStack()
        } else if (selectedTab == 1 &&
            (supportFragmentManager.findFragmentByTag("tab_1") as? PanelFragment)?.onBack() == true
        ) {
            // 面板 WebView 可回退 → goBack（原 PanelActivity.onBackPressed 逻辑平移）
        }
        // 其余情况不 finish：回调在该层级已禁用，走不到这里（系统默认返回到桌面）
    }

    /** 预测式返回手势进度 → overlay 容器贴边缩放/平移（Material 规范的收敛动效）。 */
    private fun animateOverlayBack(e: androidx.activity.BackEventCompat) {
        if (overlayContainer.width == 0) return
        val p = e.progress.coerceIn(0f, 1f)
        val edgeLeft = e.swipeEdge == androidx.activity.BackEventCompat.EDGE_LEFT
        overlayContainer.pivotX = if (edgeLeft) 0f else overlayContainer.width.toFloat()
        overlayContainer.pivotY = overlayContainer.height * 0.5f
        val s = 1f - 0.12f * p
        overlayContainer.scaleX = s
        overlayContainer.scaleY = s
        overlayContainer.translationX = (if (edgeLeft) -1f else 1f) * overlayContainer.width * 0.05f * p
        overlayContainer.alpha = 1f - 0.2f * p
    }

    /** 复位 overlay 容器变换（提交返回/取消手势/栈变化时）。 */
    private fun resetOverlayTransform() {
        overlayContainer.animate().cancel()
        overlayContainer.translationX = 0f
        overlayContainer.scaleX = 1f
        overlayContainer.scaleY = 1f
        overlayContainer.alpha = 1f
    }

    override fun onResume() {
        super.onResume()
        // 进程级前后台：WebView onResume 挂在页签 Fragment（panelSpec 页签生命周期）
        (supportFragmentManager.findFragmentByTag(tabTag(1)) as? PanelFragment)?.onHostResumed()
    }

    override fun onPause() {
        super.onPause()
        (supportFragmentManager.findFragmentByTag(tabTag(1)) as? PanelFragment)?.onHostPaused()
    }

    companion object {
        /** 程序化 UI 的固定容器 id（须小于 0x01000000，且不与资源 id 0x7f 段冲突）。 */
        const val CONTAINER_ID = 0x00ff0001
        const val OVERLAY_ID = 0x00ff0002

        /** 悬浮窗「打开面板」extras（floatingWindowSpec ⑤；route 走 PanelFragment.openRoute）。 */
        const val EXTRA_OPEN_TAB = "io.nexport.gateway.extra.OPEN_TAB"
        const val EXTRA_OPEN_ROUTE = "io.nexport.gateway.extra.OPEN_ROUTE"

        const val TAB_COUNT = 4
        private const val KEY_SELECTED_TAB = "selectedTab"
    }
}

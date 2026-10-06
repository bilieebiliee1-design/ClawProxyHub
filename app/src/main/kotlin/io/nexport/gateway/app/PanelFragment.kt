package io.nexport.gateway.app

import io.nexport.gateway.BuildConfig
import io.nexport.gateway.R

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.content.res.Configuration
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.DownloadListener
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.fragment.app.Fragment
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import com.google.android.material.progressindicator.LinearProgressIndicator

/**
 * PanelFragment — 「面板」页签：WebView 内嵌本地面板 http://127.0.0.1:<GatewayPort>/。
 *
 * 容器与生命周期（panelSpec）：
 *  - WebView 实例由 retained 无界面 Fragment（HeadlessPanelHolder，setRetainInstance(true)）
 *    持有：本 Fragment onCreateView 时 attach、onDestroyView 时 detach（仅 removeView，
 *    不 destroy）；Activity 因主题/语言切换重建时 WebView 不销毁、登录态与滚动保留；
 *    Holder.onDestroy（宿主 Activity 真终止）才 webView.destroy() 防泄漏；
 *  - 页签 show/hide 不触发 onPause/onResume：webView.onResume()/onPause()、核心仍在跑
 *    校验、主题重同步挂在 onHiddenChanged 与 MainActivity 级 onHostResumed/onHostPaused
 *    （进程级前后台）；
 *  - 懒创建：首次进入页签才建 WebView；核心未运行（state!=RUNNING 或 gatewayPort<=0）
 *    显示占位态：居中说明+主按钮「启动核心」+次按钮「重试」；
 *  - 加载/错误态（净新增）：加载中顶部 2dp 不定长进度条+页面渐显；onReceivedError/
 *    onRenderProcessGone → 错误态：图标+「面板加载失败」+errMsg 全文+复制+重试；
 *    核心停止 → 「核心已停止」态。
 *
 * 主题/字号联动（壳层为唯一事实源）：
 *  - 主路径 WebViewCompat.addDocumentStartJavaScript（任何页面脚本之前执行）：写
 *    localStorage 'cph-theme' + 设 documentElement[theme-mode] + 注入对比度补丁
 *    <style id=nx-contrast>（选择器镜像 TDesign ':root.dark, :root[theme-mode=dark]'
 *    与 ':root, :root[theme-mode=light]'，特异性 0-2-0，补丁块后置追加即胜）；
 *  - 特性不支持时回退双载：loadUrl → onPageFinished 写入 → reload 一次；
 *  - WebSettings.textZoom 按「字体大小」映射 90/100/120/140。
 *
 * 安全基线原样保留（securityPlan ④）：仅回环明文、禁 file、无 addJavascriptInterface、
 * blob 下载/文件上传两条单向 Java→JS 桥与 nexport-dl/nexport-assist 垫片；外链 Custom Tabs。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class PanelFragment : Fragment() {

    private val ui get() = Ui
    private var webView: WebView? = null
    private var loadedPort = 0

    private lateinit var root: LinearLayout
    private lateinit var progressBar: LinearProgressIndicator
    private lateinit var stateHost: FrameLayout
    private lateinit var webViewHost: FrameLayout
    private var stateView: View? = null

    private var filePathCallback: ValueCallback<Array<Uri>>? = null
    private var pendingSave: PendingSave? = null

    private class PendingSave(val name: String, val mime: String, val bytes: ByteArray)

    private val saveLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        val p = pendingSave
        pendingSave = null
        val uri = res.data?.data
        if (res.resultCode == Activity.RESULT_OK && uri != null && p != null) {
            try {
                requireContext().contentResolver.openOutputStream(uri)?.use { it.write(p.bytes) }
                toast(requireContext(), getString(R.string.panel_download_saved, p.name))
            } catch (t: Throwable) {
                toast(requireContext(), getString(R.string.panel_download_failed, t.message ?: "?"))
            }
        }
    }

    private val fileChooserLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        filePathCallback?.onReceiveValue(
            WebChromeClient.FileChooserParams.parseResult(res.resultCode, res.data))
        filePathCallback = null
    }

    override fun onCreateView(
        inflater: android.view.LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        root = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        progressBar = LinearProgressIndicator(c).apply {
            isIndeterminate = true
            visibility = View.GONE
            trackThickness = c.dp(2)
        }
        stateHost = FrameLayout(c).apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f)
        }
        webViewHost = FrameLayout(c).apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f)
            visibility = View.GONE
        }
        root.addView(progressBar, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(stateHost)
        root.addView(webViewHost)
        if (coreReady()) {
            // attach 涉及 Fragment 事务，post 出去执行——onCreateView 可能正处在
            // FragmentManager 事务中（如 installTabs 的 commitNow），嵌套 commitNow 会抛
            // "FragmentManager is already executing transactions"
            root.post { if (isAdded && coreReady()) attachWebView() }
        } else {
            showState(placeholderState())
        }
        return root
    }

    // ---- 核心状态 ----

    private fun coreReady(): Boolean =
        CoreController.state == CoreController.State.RUNNING && CoreController.gatewayPort > 0

    private fun placeholderState(): View {
        val c = requireContext()
        return ui.stateView(
            c, R.drawable.ic_error,
            c.getString(R.string.panel_placeholder_title),
            c.getString(R.string.panel_placeholder_body),
            listOf(
                ui.button(c, c.getString(R.string.core_start)) {
                    CoreController.start { _, _ -> checkCoreState() }
                },
                ui.subButton(c, c.getString(R.string.retry)) { checkCoreState() }
            ))
    }

    private fun stoppedState(): View {
        val c = requireContext()
        return ui.stateView(
            c, R.drawable.ic_error,
            c.getString(R.string.panel_core_stopped),
            c.getString(R.string.panel_placeholder_body),
            listOf(
                ui.button(c, c.getString(R.string.core_start)) {
                    CoreController.start { _, _ -> checkCoreState() }
                }
            ))
    }

    private fun errorState(msg: String): View {
        val c = requireContext()
        return ui.stateView(
            c, R.drawable.ic_error,
            c.getString(R.string.panel_error),
            msg,
            listOf(
                ui.subButton(c, c.getString(R.string.panel_copy_error)) { copyText(c, msg, showToast = true) },
                ui.button(c, c.getString(R.string.retry)) { loadPanel() }
            ))
    }

    private fun showState(v: View) {
        stateView = v
        stateHost.removeAllViews()
        stateHost.addView(v, FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        stateHost.visibility = View.VISIBLE
        webViewHost.visibility = View.GONE
        progressBar.visibility = View.GONE
    }

    private fun showContent() {
        stateHost.removeAllViews()
        stateView = null
        stateHost.visibility = View.GONE
        webViewHost.visibility = View.VISIBLE
    }

    /** 核心状态变化入口：未运行 → 占位/已停止态；运行 → 确保 WebView 已挂载并加载。 */
    private fun checkCoreState() {
        if (!isAdded) return
        when {
            !coreReady() -> showState(
                if (CoreController.state == CoreController.State.STARTING) placeholderState() else stoppedState())
            webView == null -> attachWebView()
            loadedPort != CoreController.gatewayPort -> {
                // 端口变化（核心重启）：token 供给幂等门按端口重置（panelLoginSpec A①）
                tokenProvisionedForPort = 0
                loadPanelAndConsumeRoute()
            }
            stateHost.visibility == View.VISIBLE -> loadPanelAndConsumeRoute() // 错误/占位态时重进重试
            else -> { showContent(); consumePendingRoute() }
        }
    }

    // ---- WebView（retained Holder 持有） ----

    @SuppressLint("SetJavaScriptEnabled", "AddJavascriptInterface")
    private fun attachWebView() {
        if (webView != null) return
        if (BuildConfig.DEBUG) { // 仅调试包：允许 CDP 检查面板 WebView（发布包保持关闭）
            WebView.setWebContentsDebuggingEnabled(true)
        }
        val holder = headlessHolder()
        webView = holder.ensureWebView(requireActivity())
        val wv = webView!!
        // v1.5.0 修复②（应用升级后 WebView 缓存旧 index.html 致面板导航静默失效，
        // v1.4.10 发现）：版本变更后首次创建 WebView 时清 HTTP 缓存——缓存里可能
        // 残留旧版 index.html，新 dist 的新路由会被旧入口静默吞掉。只清 HTTP 缓存
        // 不动 DOM storage/localStorage，面板登录态（JWT）与偏好不受影响。
        if (Prefs.panelCacheClearedVersion(requireContext()) != BuildConfig.VERSION_CODE) {
            wv.clearCache(true)
            Prefs.setPanelCacheClearedVersion(requireContext(), BuildConfig.VERSION_CODE)
        }
        wv.layoutParams = FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
        wv.alpha = 0f
        configureWebView(wv)
        webViewHost.addView(wv)
        // 存储持久化兜底（panelLoginSpec D）：常规 cookie 双写持久化（JWT 在
        // localStorage，CookieManager 只承载面板可能落入的 cookie）。
        android.webkit.CookieManager.getInstance().setAcceptCookie(true)
        // 面板 token 自动注入门控（panelLoginSpec A）：一次性供给后再 loadPanel
        ensurePanelToken { loadPanelAndConsumeRoute() }
    }

    private var holderRef: HeadlessPanelHolder? = null

    private fun headlessHolder(): HeadlessPanelHolder {
        holderRef?.let { return it }
        val fm = parentFragmentManager
        var h = fm.findFragmentByTag(HeadlessPanelHolder.TAG) as? HeadlessPanelHolder
        if (h == null) {
            h = HeadlessPanelHolder()
            // 异步 commit：本方法可能在 Fragment 事务中调用，不允许 commitNow
            fm.beginTransaction().add(h, HeadlessPanelHolder.TAG).commitAllowingStateLoss()
        }
        holderRef = h
        return h
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun configureWebView(wv: WebView) {
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true // pinia 持久化 / 面板 localStorage
            allowFileAccess = false  // 禁 file 协议
            allowContentAccess = true // 文件选择器回读 content:// 需要
            allowFileAccessFromFileURLs = false
            allowUniversalAccessFromFileURLs = false
            javaScriptCanOpenWindowsAutomatically = false
            setSupportMultipleWindows(false)
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            mediaPlaybackRequiresUserGesture = true
            cacheMode = WebSettings.LOAD_DEFAULT
        }
        // 无 addJavascriptInterface（securityPlan ④）——仅下面两条单向桥
        wv.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean =
                handleUrl(request.url)

            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                progressBar.visibility = View.VISIBLE
            }

            override fun onPageFinished(view: WebView, url: String) {
                progressBar.visibility = View.GONE
                view.animate().alpha(1f).setDuration(150).start()
                if (!documentStartSupported) {
                    // 回退双载：页面加载后写入主题再 reload 一次（首载多一次往返）
                    injectThemeScript(view)
                    if (!fallbackReloaded) {
                        fallbackReloaded = true
                        view.reload()
                    }
                }
                injectShim(view)
            }

            override fun doUpdateVisitedHistory(view: WebView, url: String, isReload: Boolean) {
                // v1.4.0 ⑨：WebView 历史变化 → 壳层重判预测性返回回调启用层级
                (activity as? MainActivity)?.onPanelHistoryChanged()
            }

            override fun onReceivedError(
                view: WebView, request: WebResourceRequest, error: WebResourceError
            ) {
                if (request.isForMainFrame) {
                    showError(error.description?.toString() ?: "net::ERR_FAILED")
                }
            }

            override fun onReceivedHttpError(
                view: WebView, request: WebResourceRequest, errorResponse: WebResourceResponse
            ) {
                if (request.isForMainFrame) {
                    showError("HTTP ${errorResponse.statusCode}")
                }
            }

            override fun onRenderProcessGone(view: WebView, detail: android.webkit.RenderProcessGoneDetail): Boolean {
                // 渲染进程崩溃：销毁实例并重建，避免整包崩溃
                if (Build.VERSION.SDK_INT >= 26 && detail.didCrash()) {
                    webViewHost.removeView(view)
                    webView = null
                    headlessHolder().dropWebView()
                    showState(errorState("render process gone"))
                    return true // 已处理，宿主不再被杀
                }
                return false
            }
        }
        wv.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                view: WebView,
                callback: ValueCallback<Array<Uri>>,
                params: FileChooserParams
            ): Boolean {
                // 面板离线上传（POST /admin/plugins/install-upload multipart）与
                // 备份导入（POST /admin/system/restore multipart）依赖此桥
                filePathCallback?.onReceiveValue(null)
                filePathCallback = callback
                return try {
                    fileChooserLauncher.launch(params.createIntent())
                    true
                } catch (t: Throwable) {
                    filePathCallback = null
                    toast(requireContext(), getString(R.string.panel_upload_failed))
                    false
                }
            }
        }
        wv.setDownloadListener(DownloadListener { url, _, contentDisposition, mimeType, _ ->
            handleDownload(url, contentDisposition, mimeType)
        })
    }

    // ---- 加载与错误 ----

    /** 深链路由消费（homeQuickActionsSpec / floatingWindowSpec ⑤ 共用单一路径）。 */
    private var pendingRoute: String? = null

    /**
     * 面板深链唯一消费实现：置 pendingRoute → showTab(1)。核心未运行 → toast 后停留
     * 占位态（coreListener RUNNING 分支自动导航）；WebView 已在屏则立即加载。
     * 悬浮窗「打开面板」EXTRA_OPEN_ROUTE 经 MainActivity 转发到此处。
     */
    fun openRoute(path: String) {
        val act = activity as? MainActivity ?: return
        pendingRoute = path
        act.showTab(1)
        when {
            !coreReady() -> if (isAdded) {
                toast(requireContext(), getString(R.string.panel_route_need_core))
            }
            webView != null && stateHost.visibility != View.VISIBLE -> consumePendingRoute()
            // WebView 未挂载：attachWebView → ensurePanelToken → loadPanelAndConsumeRoute 消费
        }
    }

    private fun loadPanelAndConsumeRoute() {
        val route = pendingRoute
        if (route == null) {
            loadPanel()
        } else {
            pendingRoute = null
            loadPanel(route)
        }
    }

    private fun consumePendingRoute() {
        val route = pendingRoute ?: return
        pendingRoute = null
        webView?.loadUrl(panelUrl().trimEnd('/') + route)
    }

    private fun loadPanel(path: String? = null) {
        if (!isAdded || webView == null) return
        fallbackReloaded = false
        lastErrorShown = null
        showContent()
        loadedPort = CoreController.gatewayPort
        syncThemeAndFont()
        webView!!.alpha = 0f
        webView!!.loadUrl(panelUrl().trimEnd('/') + (path ?: ""))
    }

    private fun panelUrl(): String = "http://127.0.0.1:${CoreController.gatewayPort}/"

    private var lastErrorShown: String? = null
    private fun showError(msg: String) {
        if (!isAdded) return
        if (lastErrorShown == msg) return // 同一错误不重复刷态（子资源错误等）
        lastErrorShown = msg
        showState(errorState(msg))
    }

    /** URL 分流：仅 127.0.0.1/localhost 留在 WebView；其余一律外部打开。 */
    private fun handleUrl(uri: Uri): Boolean {
        val scheme = uri.scheme ?: return true
        if (scheme == "nexport-dl") { // 垫片信号：取最近一次 blob 锚点下载
            pullStashedDownload()
            return true
        }
        if (scheme == "nexport-assist") { // 垫片信号（v1.4.9 ②）：面板 window.open 外链 → Custom Tabs
            uri.getQueryParameter("u")?.let { openExternal(requireContext(), it) }
            return true
        }
        if ((scheme == "http" || scheme == "https") &&
            (uri.host == "127.0.0.1" || uri.host == "localhost")
        ) return false
        openExternal(requireContext(), uri.toString())
        return true
    }

    // ---- 主题/字号同步（壳层为唯一事实源） ----

    private var documentStartSupported = false
    private var fallbackReloaded = false
    private var injectedMode: String? = null
    private var scriptHandler: androidx.webkit.ScriptHandler? = null

    // ---- 面板 token 自动供给（panelLoginSpec A） ----

    /** 幂等门：已为该端口完成 token 供给探测（后续 loadPanel 复用，不重复 POST）。 */
    private var tokenProvisionedForPort = 0

    /** document-start 种子 token（非空时脚本含幂等注入；只「缺」才写，不覆盖面板内登录的新 token）。 */
    private var seedToken: String? = null

    /**
     * 面板 token 自动注入门控（panelLoginSpec A；attachWebView 一次性供给，非逐次 loadPanel）：
     * ①幂等门 tokenProvisionedForPort == 网关端口 → 直接 onReady；
     * ②主线程回调式异步探测 localStorage 'cph-admin-token'（严禁复用 currentPanelToken 的
     *   runOnUiThread+CountDownLatch 阻塞式——主线程复用会自锁）；已有 token 或面板有
     *   「已退出」标记（panelLoginSpec E）→ 标记已供给，注册纯主题脚本直接加载（零 POST）；
     * ③缺失 → 凭据判空 + Keystore 解密 + AdminApi.login 全部在工作线程（perf 修复轮：
     *   原实现在本回调主线程里连读 Prefs.adminUser/adminPass——adminPass 每调用走
     *   KeyEnvelope.decryptPrefs 的 Keystore AES-GCM 解密且被连调两次，恰逢核心刚
     *   RUNNING 的主线程任务峰值，是升级后首启 ANR 的最强静态候选）。解密为空 →
     *   回主线程走纯主题零 POST 分支；凭据在 → AdminApi.login 铸新 token
     *   （AdminApi.reset 随核心停/启清空，每核心会话至多一次 POST）→ 注册合并脚本
     *   （主题+种子注入）→ onReady；
     * ④铸失败 → 纯主题脚本照常加载（面板自身显示登录表单=合法兜底）+ Snackbar 明示。
     */
    private fun ensurePanelToken(onReady: () -> Unit) {
        val wv = webView
        if (wv == null || CoreController.gatewayPort <= 0) { onReady(); return }
        if (tokenProvisionedForPort == CoreController.gatewayPort) { onReady(); return }
        // ②异步探测：主线程回调只依据 token/loggedOut 两个 JS 结果分支，不做任何 SP/Keystore 读取
        wv.evaluateJavascript(
            "(function(){try{return JSON.stringify({t:localStorage.getItem('cph-admin-token')," +
                "lo:sessionStorage.getItem('nx-logged-out')});}catch(e){return '{\"t\":null,\"lo\":null}';}})()"
        ) { json ->
            if (!isAdded) return@evaluateJavascript
            var token: String? = null
            var loggedOut = false
            try {
                val o = org.json.JSONObject(json ?: "{}")
                token = o.optString("t").takeIf { it.isNotEmpty() && it != "null" }
                loggedOut = o.optString("lo") == "1"
            } catch (_: Exception) {}
            if (token != null || loggedOut) {
                // 已有 token / 用户已主动退出：纯主题脚本直接加载（零 POST）
                seedToken = null
                tokenProvisionedForPort = CoreController.gatewayPort
                syncThemeAndFont()
                onReady()
                return@evaluateJavascript
            }
            // ③凭据判空 + 解密 + 铸新 token（全部工作线程；同步阻塞的 SP/Keystore/HTTP 不占主线程）
            val appCtx = requireContext().applicationContext
            Thread {
                val user = Prefs.adminUser(appCtx)
                val pass = if (user != null) Prefs.adminPass(appCtx) else null
                if (user == null || pass == null) {
                    // 无本机凭据（或解封失败）：回主线程走纯主题零 POST 分支
                    if (!isAdded) return@Thread
                    requireActivity().runOnUiThread {
                        if (!isAdded) return@runOnUiThread
                        seedToken = null
                        tokenProvisionedForPort = CoreController.gatewayPort
                        syncThemeAndFont()
                        onReady()
                    }
                    return@Thread
                }
                val r = AdminApi.login(user, pass)
                if (!isAdded) return@Thread
                requireActivity().runOnUiThread {
                    if (!isAdded) return@runOnUiThread
                    if (r.isSuccess) {
                        seedToken = AdminApi.currentToken()
                    } else {
                        seedToken = null
                        view?.let {
                            com.google.android.material.snackbar.Snackbar.make(
                                it, getString(R.string.panel_token_mint_failed),
                                com.google.android.material.snackbar.Snackbar.LENGTH_LONG).show()
                        }
                    }
                    tokenProvisionedForPort = CoreController.gatewayPort
                    syncThemeAndFont()
                    onReady()
                }
            }.start()
        }
    }

    private fun currentMode(): String {
        val mask = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK)
        return if (mask == Configuration.UI_MODE_NIGHT_YES) "dark" else "light"
    }

    /** 文字缩放映射：小 90 / 标准 100 / 大 120 / 特大 140。 */
    private fun textZoom(): Int =
        intArrayOf(90, 100, 120, 140)[Prefs.fontSizeIndex(requireContext()).coerceIn(0, 3)]

    /** 进入/重载/主题变化时同步（document-start 脚本随 mode/port 更新重新注册）。 */
    fun syncThemeAndFont() {
        val wv = webView ?: return
        val mode = currentMode()
        wv.settings.textZoom = textZoom()
        val script = documentScript(mode)
        if (WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
            documentStartSupported = true
            // 规则语法为 SCHEME://HOST[:PORT]（PORT 为字面量，不支持端口通配符——
            // 规格草拟的 "http://127.0.0.1:*" 会抛 IllegalArgumentException），
            // 故以当前网关端口精确注册；端口变化（核心重启）由 loadPanel→本方法重注册。
            scriptHandler?.remove()
            scriptHandler = WebViewCompat.addDocumentStartJavaScript(
                wv, script, setOf("http://127.0.0.1:${CoreController.gatewayPort}"))
            if (injectedMode != null && injectedMode != mode && wv.url?.startsWith("http://127.0.0.1") == true) {
                // 已加载页面即时生效（脚本含幂等标记，可直接执行）
                wv.evaluateJavascript(script, null)
            }
            injectedMode = mode
        } else {
            documentStartSupported = false
            if (injectedMode != null && injectedMode != mode) fallbackReloaded = false // 新模式允许再双载
            injectedMode = mode
        }
    }

    /**
     * document-start 全量脚本（panelLoginSpec A/B/E）：
     *  - 主题/对比度补丁（themeScript 原有职责）；
     *  - B /login 环不变量：有 token 且 location.pathname==='/login' → location.replace('/')
     *    （history 中 /login 条目被替换为 '/'，残余「一次无感知空按」由返回守卫 C 消除）；
     *  - E 退出标记：seed 注入前置条件追加 sessionStorage 'nx-logged-out'!=='1'；
     *  - 种子 token 幂等注入（A③）：只「缺」才写，不覆盖面板内登录的新 token，
     *    token 经 JSONObject.quote 转义（同 pullBlob 写法）。
     */
    private fun documentScript(mode: String): String {
        val extra = StringBuilder()
        seedToken?.let { tok ->
            extra.append("  try {\n")
            extra.append("    if (!localStorage.getItem('cph-admin-token') && sessionStorage.getItem('nx-logged-out') !== '1') {\n")
            extra.append("      localStorage.setItem('cph-admin-token', ${org.json.JSONObject.quote(tok)});\n")
            extra.append("    }\n")
            extra.append("  } catch (e) {}\n")
        }
        extra.append(
            "  try {\n" +
            "    if (localStorage.getItem('cph-admin-token') && location.pathname === '/login') {\n" +
            "      location.replace('/');\n" +
            "    }\n" +
            "  } catch (e) {}\n")
        return themeScript(mode) + "\n" + extra
    }

    /**
     * 主题同步 + 对比度补丁（document-start 执行；幂等标记 window.__nxContrast）。
     * 补丁选择器镜像 TDesign（es/style/index.css:47-49 浅色 ':root, :root[theme-mode=light]'、
     * :184-185 暗色 ':root.dark, :root[theme-mode=dark]'，特异性 0-2-0）；
     * 文字变量为库级 --td-text-color-primary/secondary/placeholder（index.css:160-162/:297-299）。
     * 补丁值锁定 designSystem 深浅色板（values(-night)/colors.xml 同源）。
     */
    private fun themeScript(mode: String): String {
        return """
            (function(){
              var mode = '$mode';
              try { localStorage.setItem('cph-theme', mode); } catch (e) {}
              var css = [
                ':root, :root[theme-mode=light] {',
                '  --td-text-color-primary: #0F172A;',
                '  --td-text-color-secondary: #475569;',
                '  --td-text-color-placeholder: #5D6B7E;',
                '  --td-text-color-disabled: #64748B;',
                '  --td-font-gray-1: #0F172A;',
                '  --td-font-gray-2: #475569;',
                '  --td-font-gray-3: #5D6B7E;',
                '}',
                ':root.dark, :root[theme-mode=dark] {',
                '  --td-text-color-primary: #E8EDF5;',
                '  --td-text-color-secondary: #A8B8CF;',
                '  --td-text-color-placeholder: #7C8DA8;',
                '  --td-text-color-disabled: #64748B;',
                '  --td-font-white-1: #E8EDF5;',
                '  --td-font-white-2: #A8B8CF;',
                '  --td-font-white-3: #7C8DA8;',
                '}'
              ].join('\n');
              function apply() {
                var de = document.documentElement;
                if (!de) return;
                de.setAttribute('theme-mode', mode);
                var s = document.getElementById('nx-contrast');
                if (!s) {
                  s = document.createElement('style');
                  s.id = 'nx-contrast';
                  (document.head || de).appendChild(s);
                }
                s.textContent = css;
              }
              apply();
              document.addEventListener('DOMContentLoaded', apply);
              window.__nxContrast = true;
            })(); void 0
        """.trimIndent()
    }

    private fun injectThemeScript(view: WebView) {
        // 回退双载路径同样带种子注入与 /login 不变量（documentScript 与 document-start 一致）
        view.evaluateJavascript(documentScript(currentMode()), null)
    }

    // ---- 下载桥（备份导出 zip / 调用日志 CSV；经 SAF 落用户目录） ----

    private fun handleDownload(url: String, contentDisposition: String?, mimeType: String?) {
        val name = Regex("filename\\s*=\\s*\"?([^\";]+)\"?")
            .find(contentDisposition ?: "")?.groupValues?.get(1)?.trim()
        when {
            url.startsWith("blob:") -> pullBlob(url, name ?: suggestName(mimeType))
            url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:") ->
                fetchAuthed(url, name ?: suggestName(mimeType))
            else -> openExternal(requireContext(), url)
        }
    }

    private fun suggestName(mime: String?) = when {
        mime?.contains("zip") == true -> "nexport-backup.zip"
        mime?.contains("csv") == true -> "nexport-logs.csv"
        else -> "nexport-export.bin"
    }

    /** 从页面垫片取最近一次锚点下载（blob URL + 文件名）。 */
    private fun pullStashedDownload() {
        val wv = webView ?: return
        wv.evaluateJavascript(
            "(function(){var d=window.__nxLastDownload;return d?JSON.stringify(d):null;})()"
        ) { json ->
            var href: String? = null; var name: String? = null
            if (json != null && json != "null") {
                try {
                    val o = org.json.JSONObject(json)
                    href = o.optString("href")
                    name = o.optString("name")
                } catch (_: Exception) {}
            }
            if (href.isNullOrEmpty()) {
                toast(requireContext(), getString(R.string.panel_download_failed, "blob unavailable"))
            } else {
                pullBlob(href, name?.ifEmpty { null } ?: "nexport-export.bin")
            }
        }
    }

    /** blob: → dataURL（Java→JS 单向，轮询取结果）→ SAF。 */
    private fun pullBlob(blobUrl: String, name: String) {
        val wv = webView ?: return
        val js = """
            (function(){
              window.__nxPayload = null;
              try {
                fetch(${org.json.JSONObject.quote(blobUrl)}).then(function(r){
                  if (!r.ok) throw new Error('HTTP ' + r.status);
                  return r.blob();
                }).then(function(b){
                  var fr = new FileReader();
                  fr.onload = function(){ window.__nxPayload = fr.result; };
                  fr.onerror = function(){ window.__nxPayload = 'ERR:fileReader'; };
                  fr.readAsDataURL(b);
                }).catch(function(e){ window.__nxPayload = 'ERR:' + e; });
              } catch(e) { window.__nxPayload = 'ERR:' + e; }
              return 'ok';
            })()
        """.trimIndent()
        wv.evaluateJavascript(js) {}
        var attempts = 0
        val poll = object : Runnable {
            override fun run() {
                val w = webView ?: return
                attempts++
                w.evaluateJavascript("window.__nxPayload === null ? 'null' : String(window.__nxPayload)") { v ->
                    val payload = v?.trim('"')
                    when {
                        payload != null && payload != "null" && payload != "undefined" ->
                            if (payload.startsWith("data:")) {
                                saveViaSaf(name, payload)
                            } else {
                                toast(requireContext(),
                                    getString(R.string.panel_download_failed, payload.take(120)))
                            }
                        attempts > 100 ->
                            toast(requireContext(), getString(R.string.panel_download_failed, "timeout"))
                        else -> w.postDelayed(this, 100)
                    }
                }
            }
        }
        wv.postDelayed(poll, 100)
    }

    /** 直链下载（面板 blob 之外的场景）：带面板 Bearer token 重新请求。 */
    private fun fetchAuthed(url: String, name: String) {
        Thread {
            try {
                val conn = java.net.URL(url).openConnection() as java.net.HttpURLConnection
                conn.connectTimeout = 5000
                conn.readTimeout = 20000
                val token = currentPanelToken()
                if (!token.isNullOrEmpty()) conn.setRequestProperty("Authorization", "Bearer $token")
                val code = conn.responseCode
                if (code !in 200..299) throw Exception("HTTP $code")
                val bytes = conn.inputStream.use { it.readBytes() }
                conn.disconnect()
                requireActivity().runOnUiThread {
                    pendingSave = PendingSave(name, guessMime(name), bytes); launchSaf(name)
                }
            } catch (t: Throwable) {
                if (isAdded) requireActivity().runOnUiThread {
                    toast(requireContext(), getString(R.string.panel_download_failed, t.message ?: "?"))
                }
            }
        }.start()
    }

    /** 面板 token：localStorage('cph-admin-token')（dashboard/src/api/client.ts 已核实）。 */
    private fun currentPanelToken(): String? {
        val wv = webView ?: return null
        var token: String? = null
        val latch = java.util.concurrent.CountDownLatch(1)
        requireActivity().runOnUiThread {
            wv.evaluateJavascript(
                "(function(){return localStorage.getItem('cph-admin-token');})()"
            ) { v ->
                token = v?.trim('"')?.takeIf { it.isNotEmpty() && it != "null" }
                latch.countDown()
            }
        }
        latch.await(2, java.util.concurrent.TimeUnit.SECONDS)
        return token
    }

    private fun saveViaSaf(name: String, dataUrl: String) {
        try {
            val b64 = dataUrl.substringAfter("base64,")
            val bytes = android.util.Base64.decode(b64, android.util.Base64.DEFAULT)
            pendingSave = PendingSave(name, guessMime(name), bytes)
            launchSaf(name)
        } catch (t: Throwable) {
            toast(requireContext(), getString(R.string.panel_download_failed, t.message ?: "?"))
        }
    }

    private fun launchSaf(name: String) {
        val intent = Intent(Intent.ACTION_CREATE_DOCUMENT).apply {
            addCategory(Intent.CATEGORY_OPENABLE)
            type = guessMime(name)
            putExtra(Intent.EXTRA_TITLE, name)
        }
        saveLauncher.launch(intent)
    }

    private fun guessMime(name: String): String = when {
        name.endsWith(".zip", true) -> "application/zip"
        name.endsWith(".csv", true) -> "text/csv"
        name.endsWith(".json", true) -> "application/json"
        else -> "application/octet-stream"
    }

    // ---- 垫片（每次页面加载后注入；仅改写本页面 JS 行为，无 JS→Java 桥） ----

    private fun injectShim(view: WebView) {
        val js = """
            (function(){
              if (window.__nxShim) return;
              window.__nxShim = true;
              var origRevoke = URL.revokeObjectURL.bind(URL);
              URL.revokeObjectURL = function(u){
                setTimeout(function(){ try { origRevoke(u); } catch(e) {} }, 60000);
              };
              var origClick = HTMLAnchorElement.prototype.click;
              HTMLAnchorElement.prototype.click = function(){
                try {
                  var href = this.href || '';
                  if (href.indexOf('blob:') === 0) {
                    window.__nxLastDownload = { href: href, name: this.download || '', ts: Date.now() };
                    setTimeout(function(){ try { window.location.href = 'nexport-dl://queued'; } catch(e) {} }, 0);
                  }
                } catch(e) {}
                return origClick.apply(this, arguments);
              };
              var origWinOpen = window.open;
              window.open = function(u, t, f){
                try {
                  // v1.4.9 ② QA 实测：面板 WebView 配置（javaScriptCanOpenWindowsAutomatically
                  // =false + setSupportMultipleWindows=false）下 window.open('_blank') 被整体
                  // 吞掉（无导航、不触发 shouldOverrideUrlLoading），M2 open_url 链路断在
                  // 第一跳。照 nexport-dl 垫片先例：非本机 http(s) 外链改道
                  // nexport-assist://open?u=…，由壳层 handleUrl → openExternal（Custom Tabs）。
                  if (typeof u === 'string' &&
                      (u.indexOf('https:') === 0 || u.indexOf('http:') === 0)) {
                    var a = document.createElement('a'); a.href = u;
                    var h = a.hostname;
                    if (h !== '127.0.0.1' && h !== 'localhost') {
                      setTimeout(function(){
                        try { window.location.href = 'nexport-assist://open?u=' + encodeURIComponent(u); }
                        catch(e) {}
                      }, 0);
                      return null;
                    }
                  }
                } catch(e) {}
                return origWinOpen.apply(window, arguments);
              };
            })(); void 0
        """.trimIndent()
        view.evaluateJavascript(js, null)
    }

    // ---- 返回与生命周期 ----

    /**
     * 返回键消费（panelLoginSpec C 返回守卫）：预判 goBack 目标页而非判 wv.url——
     * 主场景按下返回时 wv.url==/dashboard，判 wv.url 拦不住。目标为 /login 且其前
     * 还有条目 → 连跳两级（goBackOrForward(-2)）消除「/login 替换后空按」；/login
     * 为第 0 条 → 交壳层 finish（document-start B 已保证 /login 不会以表单形态可见）。
     * 残余行为：仅当 B 的替换发生在用户已回退到 /login 之后（replace 后回退落到同页
     * '/'）才多按一次，属可接受（panelLoginSpec 已写明）。
     */
    fun onBack(): Boolean {
        val wv = webView ?: return false
        val bl = wv.copyBackForwardList()
        if (bl.currentIndex > 0) {
            val target = bl.getItemAtIndex(bl.currentIndex - 1).url
            if (Uri.parse(target).path == "/login") {
                return if (bl.currentIndex >= 2) { wv.goBackOrForward(-2); true } else false
            }
        }
        return if (wv.canGoBack()) { wv.goBack(); true } else false
    }

    /**
     * 只读版 onBack（v1.4.0 ⑨）：供 MainActivity 重判预测性返回回调启用层级
     * （面板页签且 WebView 可回退 → 启用；目标为 /login 且需连跳两级同样算可回退）。
     */
    fun canGoBack(): Boolean {
        val wv = webView ?: return false
        val bl = wv.copyBackForwardList()
        if (bl.currentIndex > 0) {
            val target = bl.getItemAtIndex(bl.currentIndex - 1).url
            if (Uri.parse(target).path == "/login") return bl.currentIndex >= 2
        }
        return wv.canGoBack()
    }

    /** 进程级前后台（MainActivity.onResume/onPause 转发）。 */
    fun onHostResumed() {
        if (!isAdded) return
        checkCoreState()
        syncThemeAndFont()
        if (CoreController.state == CoreController.State.RUNNING) webView?.onResume()
    }

    fun onHostPaused() {
        if (!isAdded) return
        webView?.onPause()
        // 存储持久化兜底（panelLoginSpec D）：WebView 状态落盘（含 cookie）
        runCatching { android.webkit.CookieManager.getInstance().flush() }
    }

    /** 页签 show/hide 联动（show/hide 不触发 onPause/onResume）。 */
    override fun onHiddenChanged(hidden: Boolean) {
        super.onHiddenChanged(hidden)
        if (hidden) {
            webView?.onPause()
        } else {
            // show/hide 事务可在 onCreateView 之前派发（冷启动且进程存活时经
            // OPEN_TAB 直达面板：installTabs 先 hide 本页签，随后 showTab(1) 的
            // 事务在视图创建前触发本回调——FragmentStateManager 实测会延迟视图
            // 创建）。此时直接跳过：onCreateView 的 root.post 自愈路径随后补挂。
            if (!::stateHost.isInitialized || !::webViewHost.isInitialized) return
            checkCoreState()
            syncThemeAndFont()
            if (CoreController.state == CoreController.State.RUNNING) webView?.onResume()
        }
    }

    private val coreListener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            if (!isAdded) return
            when {
                state == CoreController.State.RUNNING -> {
                    if (webView == null) {
                        attachWebView() // → ensurePanelToken → loadPanelAndConsumeRoute（pendingRoute 自动导航）
                    } else if (loadedPort != CoreController.gatewayPort) {
                        tokenProvisionedForPort = 0
                        loadPanelAndConsumeRoute()
                    } else {
                        showContent()
                        consumePendingRoute()
                    }
                }
                state == CoreController.State.STARTING -> {
                    if (webViewHost.visibility == View.VISIBLE) showState(placeholderState())
                }
                else -> {
                    // 核心停止/失败：WebView 在场则显示「核心已停止」态（panelSpec）
                    if (webView != null && stateHost.visibility != View.VISIBLE) showState(stoppedState())
                    else if (stateView != null) showState(stoppedState())
                }
            }
        }
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        CoreController.addListener(coreListener)
    }

    override fun onResume() {
        super.onResume()
        checkCoreState()
        syncThemeAndFont()
        if (CoreController.state == CoreController.State.RUNNING) webView?.onResume()
    }

    override fun onPause() {
        super.onPause()
        webView?.onPause()
    }

    override fun onDestroyView() {
        CoreController.removeListener(coreListener)
        // 仅 detach：WebView 归 retained Holder 所有，Activity 重建后重新挂载
        webView?.let { wv ->
            (wv.parent as? ViewGroup)?.removeView(wv)
            wv.onPause()
        }
        // 存储持久化兜底（panelLoginSpec D）：Activity 重建/页签销毁前 flush WebView 状态
        runCatching { android.webkit.CookieManager.getInstance().flush() }
        webView = null
        super.onDestroyView()
    }
}

/**
 * HeadlessPanelHolder — retained 无界面 Fragment，跨 Activity 重建持有 WebView 实例
 * （panelSpec「WebView retained 持有」）。宿主 Activity 真终止（isFinishing 引发的
 * FragmentManager 销毁）才 destroy，防泄漏。
 */
class HeadlessPanelHolder : Fragment() {

    private var webView: WebView? = null

    fun ensureWebView(activity: android.app.Activity): WebView {
        webView?.let { return it }
        val wv = WebView(activity)
        webView = wv
        return wv
    }

    /** 渲染进程崩溃等场景的实例重置。 */
    fun dropWebView() {
        webView?.destroy()
        webView = null
    }

    override fun onDestroy() {
        webView?.destroy()
        webView = null
        super.onDestroy()
    }

    companion object {
        const val TAG = "headless_panel_holder"
    }
}

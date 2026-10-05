package io.nexport.gateway.app

import io.nexport.gateway.R

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.appcompat.app.AlertDialog
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.google.android.material.button.MaterialButton
import com.google.android.material.progressindicator.LinearProgressIndicator

/**
 * BrowserLoginActivity — 「浏览器登录助手」（v1.4.9 ①，M1 Cookie 预填；coreFixes #1/#2）。
 *
 * 面板/应用层通用机制，插件源码与 gRPC 契约零改动：应用内独立 WebView（非面板 WebView）
 * 打开供应商登录页 → 用户完成登录 → CookieManager.getCookie(目标域) 取 Cookie 头
 * → 按下方 RECIPES 映射表（插件 → 登录 URL + 目标域 + method_id + 预填字段名）预填
 * → 经 AdminApi 回环 Bearer 客户端 POST /admin/accounts/login 直接建档（与面板向导
 * 同一提交链，core/adminapi/server.go:278-324 JSON body {plugin, method_id, form}）
 * → 成功后经 MainActivity EXTRA_OPEN_ROUTE 深链通道回面板账号列表
 * （MainActivity.handleOpenIntent → PanelFragment.openRoute 单一路径）。
 *
 * 配方口径（字段名逐个对源码核验，gateway-mobile/core/plugmgr/builtin/<名>/main.go）：
 *  - doubao  cookie_header → 字段 cookie（整头，main.go:96-104）
 *  - loomy   cookie_header → cookie（整头，main.go:94-106）
 *  - postman cookie → cookie（整头，main.go:223-231；workspace_id/team_id 可选留空自动取）
 *  - improvado cookie → cookie（整头）+ workspace_id 必填手动补（main.go:76-99）
 *  - notion  cookie → cookie（整头）+ space_id/user_id 必填手动补（main.go:237-263）
 *  - ima     cookie → content（x-ima-cookie 语义：整头含 IMA-TOKEN 即被
 *            auth.go sniffCredential:275 接受）
 *  - puter   auth_token → auth_token（浏览器 popup 产物试点：evaluateJavascript 扫
 *            localStorage 中 JWT 形 token，失败退回整头/手动粘贴）
 * 字段清单不硬编码：运行时 GET /admin/plugins/{name}/auth-methods 取字段定义
 * （server.go authMethodView），除预填字段外必填项由用户在确认单中补齐，可选项留空。
 *
 * 安全基线与面板 WebView 同源收窄：http(s) 一律留在助手 WebView（跨域 SSO 跳转的
 * cookie 落在应用存储——Custom Tabs 与应用内 WebView 双向不共享 cookie，外跳会丢
 * 会话，这是 M1 的既有边界）、非 http(s) scheme 外跳 Custom Tabs、禁 file 协议、
 * 无 addJavascriptInterface（提取只走 CookieManager + 单向 evaluateJavascript 读）。
 *
 * 已知边界（如实）：站点若强制对嵌入式 WebView 拒绝的第三方 SSO（如 Google 登录
 * 403 disallowed_useragent），登录无法在本 WebView 内完成——用户应改用面板向导
 * 手动粘贴；助手不隐藏该失败，提取不到凭据时给出明确状态而非假装成功。
 *
 * v1.4.11：① onCreate 后套根布局 insets 监听（Android 15 强制 edge-to-edge，顶栏
 * 侵入状态栏修复，MainActivity.kt 同款）；② Recipe.requiredCookieKeys 会话 Cookie
 * 预检（doubao=[sessionid, sessionid_ss] any-of、loomy=[loomy_web_session]），缺失
 * 按未登录拒绝预填——插件侧 doubao loginCookieHeader 同尺校验（双重防线）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class BrowserLoginActivity : BaseActivity() {

    // ---- 配方表（app 层映射：插件 → 登录 URL + 目标域 + method_id + 预填字段名） ----

    /** storageHosts 非空 = 额外做 localStorage JWT 扫描（puter 型 site-token 试点）。
     *  requiredCookieKeys 非空 = 提取的 Cookie 头必须含至少一键（any-of）才预填，
     *  缺失按未登录拒绝（v1.4.11 缺陷②：doubao 登录前匿名 Cookie 建档必 401）。 */
    private data class Recipe(
        val plugin: String,
        val labelZh: String,
        val methodId: String,
        val loginUrl: String,
        val cookieUrls: List<String>,
        val cookieField: String,
        val storageHosts: List<String> = emptyList(),
        val requiredCookieKeys: List<String> = emptyList(),
    )

    companion object {
        /** 支持浏览器辅助登录的插件配方（插件名以 /admin/plugins 的 name 为准）。 */
        private val RECIPES = listOf(
            // requiredCookieKeys（v1.4.11 缺陷②）：doubao 会话键 sessionid/sessionid_ss
            //（any-of，具体哪个必现待真机登录验证，存疑标注）；loomy 关键键
            // loomy_web_session（loomy/account.go:20）。其余配方关键 cookie 名未经
            // 确认且插件侧已自带登录校验，本轮不加。
            Recipe("doubao", "豆包", "cookie_header", "https://www.doubao.com/",
                listOf("https://www.doubao.com"), "cookie",
                requiredCookieKeys = listOf("sessionid", "sessionid_ss")),
            Recipe("loomy", "Loomy", "cookie_header", "https://loomy.xunfei.cn/",
                listOf("https://loomy.xunfei.cn"), "cookie",
                requiredCookieKeys = listOf("loomy_web_session")),
            Recipe("postman", "Postman", "cookie", "https://go.postman.co/login",
                listOf("https://go.postman.co", "https://postman.co"), "cookie"),
            Recipe("improvado", "Improvado", "cookie", "https://report.improvado.io/",
                listOf("https://report.improvado.io"), "cookie"),
            Recipe("notion", "Notion", "cookie", "https://www.notion.so/login",
                listOf("https://www.notion.so", "https://notion.so"), "cookie"),
            Recipe("ima", "ima", "cookie", "https://ima.qq.com/",
                listOf("https://ima.qq.com"), "content"),
            Recipe("puter", "Puter", "auth_token", "https://puter.com/",
                listOf("https://puter.com"), "auth_token", storageHosts = listOf("puter.com")),
        )

        /** 插件是否受支持（HomeFragment 长按入口的判定与提示文案用）。 */
        fun supports(plugin: String): Boolean = RECIPES.any { it.plugin == plugin }

        /** 入口：HomeFragment 供应商格子长按。plugin 受支持 → 直达；否则进选择器。 */
        fun start(ctx: Context, plugin: String?) {
            ctx.startActivity(Intent(ctx, BrowserLoginActivity::class.java).apply {
                putExtra(EXTRA_PLUGIN, plugin ?: "")
            })
        }

        private const val EXTRA_PLUGIN = "io.nexport.gateway.extra.BROWSER_LOGIN_PLUGIN"
        private const val STATE_PLUGIN = "browser_login_plugin"
    }

    private val ui get() = Ui
    private val main = Handler(Looper.getMainLooper())

    private var recipe: Recipe? = null
    private var webView: WebView? = null
    private var extracting = false

    private lateinit var progressBar: LinearProgressIndicator
    private lateinit var statusView: TextView
    private lateinit var extractBtn: MaterialButton

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(ui.color(this, R.attr.nxPageBg))
        }

        // 顶栏：标题 + 当前配方副标题
        root.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(12), dp(16), dp(8))
            addView(TextView(this@BrowserLoginActivity).apply {
                textSize = 17f
                setTypeface(typeface, android.graphics.Typeface.BOLD)
                setTextColor(ui.color(this, R.attr.nxTextPrimary))
                text = getString(R.string.browser_login_title)
            })
            addView(TextView(this@BrowserLoginActivity).apply {
                textSize = 13f
                setTextColor(ui.color(this, R.attr.nxTextTertiary))
                text = getString(R.string.browser_login_pick_prompt)
                setPadding(0, dp(2), 0, 0)
            })
        })

        progressBar = LinearProgressIndicator(this).apply {
            isIndeterminate = true
            visibility = View.GONE
            trackThickness = dp(2)
        }
        root.addView(progressBar, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))

        val webViewHost = FrameLayout(this).apply {
            setBackgroundColor(ui.color(this, R.attr.nxCardSurface))
        }
        root.addView(webViewHost, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))

        // 底栏：状态行 + 动作按钮（提取 / 重开登录页）
        statusView = TextView(this).apply {
            textSize = 13f
            setTextColor(ui.color(this, R.attr.nxTextSecondary))
            text = getString(R.string.browser_login_status_idle)
        }
        extractBtn = ui.button(this, getString(R.string.browser_login_extract)) { extractAndConfirm() }
        val reloadBtn = ui.subButton(this, getString(R.string.browser_login_reload)) {
            recipe?.let { webView?.loadUrl(it.loginUrl) }
        }
        root.addView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(8), dp(16), dp(12))
            addView(statusView)
            addView(LinearLayout(this@BrowserLoginActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                setPadding(0, dp(8), 0, 0)
                addView(extractBtn, LinearLayout.LayoutParams(
                    0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply { marginEnd = dp(8) })
                addView(reloadBtn, LinearLayout.LayoutParams(
                    0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            })
        }, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            setMargins(0, dp(8), 0, 0)
        })

        setContentView(root)

        // 状态栏 insets 修复（v1.4.11 缺陷①）：targetSdk 35 → Android 15 强制
        // edge-to-edge，themes.xml 的 android:statusBarColor 在 API 35+ 被忽略，本页
        // 全代码化 UI 无 insets 处理 → 顶栏伸进状态栏。套 MainActivity.kt 同款单点
        // 监听于根 LinearLayout：top=max(状态栏, 刘海)、bottom=max(导航条, 键盘)（合并
        // type 的 getInsets 返回并集 max，adjustResize 下输入框不被键盘遮挡），CONSUMED
        // 阻止向子视图二次分发。双 regime 无版本分支：API 26-34 框架默认
        // decorFitsSystemWindows=true，root 收到的 systemBars insets 已被框架消费
        // （padding=0），无双重内缩。
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val t = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or
                    WindowInsetsCompat.Type.displayCutout() or
                    WindowInsetsCompat.Type.ime())
            v.setPadding(0, t.top, 0, t.bottom)
            WindowInsetsCompat.CONSUMED
        }

        attachWebView(webViewHost)

        val saved = savedInstanceState?.getString(STATE_PLUGIN).orEmpty()
        val wanted = intent?.getStringExtra(EXTRA_PLUGIN).orEmpty().ifEmpty { saved }
        val r = RECIPES.firstOrNull { it.plugin == wanted }
        if (r != null) startRecipe(r) else showPicker()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString(STATE_PLUGIN, recipe?.plugin ?: "")
    }

    private fun showPicker() {
        val labels = RECIPES.map { "${it.labelZh}（${it.plugin}）" }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.browser_login_pick_prompt))
            .setItems(labels.toTypedArray()) { d, which ->
                d.dismiss()
                startRecipe(RECIPES[which])
            }
            .show()
    }

    private fun startRecipe(r: Recipe) {
        recipe = r
        statusView.text = getString(R.string.browser_login_status_idle)
        webView?.loadUrl(r.loginUrl)
    }

    // ---- WebView（助手私有；提取走 CookieManager，无 JS→Java 桥） ----

    @SuppressLint("SetJavaScriptEnabled")
    private fun attachWebView(host: FrameLayout) {
        CookieManager.getInstance().setAcceptCookie(true)
        val wv = WebView(this)
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            allowContentAccess = true
            allowFileAccessFromFileURLs = false
            allowUniversalAccessFromFileURLs = false
            // SSO 弹窗并入本 WebView 导航（多窗口关闭时 window.open 落回当前视图），
            // 保证登录会话 cookie 全部落在应用内存储
            javaScriptCanOpenWindowsAutomatically = true
            setSupportMultipleWindows(false)
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            mediaPlaybackRequiresUserGesture = true
            cacheMode = WebSettings.LOAD_DEFAULT
        }
        CookieManager.getInstance().setAcceptThirdPartyCookies(wv, true)
        wv.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val scheme = request.url.scheme ?: return true
                if (scheme == "http" || scheme == "https") return false // 全部留在助手 WebView
                openExternal(this@BrowserLoginActivity, request.url.toString())
                return true
            }

            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                progressBar.visibility = View.VISIBLE
            }

            override fun onPageFinished(view: WebView, url: String) {
                progressBar.visibility = View.GONE
            }

            override fun onReceivedError(
                view: WebView, request: WebResourceRequest, error: WebResourceError
            ) {
                if (request.isForMainFrame) {
                    statusView.text = error.description?.toString()
                        ?: getString(R.string.browser_login_net_error)
                }
            }
        }
        wv.webChromeClient = object : WebChromeClient() {
            override fun onProgressChanged(view: WebView?, newProgress: Int) {
                progressBar.visibility = if (newProgress in 1..99) View.VISIBLE else View.GONE
            }
        }
        host.addView(wv, FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        webView = wv
    }

    // ---- 提取与预填 ----

    /** 取目标域 Cookie 头（多候选取最长——通常即登录落域的完整头）。 */
    private fun pickCookie(r: Recipe): String? {
        var best: String? = null
        for (u in r.cookieUrls) {
            val c = CookieManager.getInstance().getCookie(u)
            if (!c.isNullOrEmpty() && (best == null || c.length > best.length)) best = c
        }
        return best
    }

    /**
     * puter 型：读页面 localStorage 中首个 JWT 形 token（单向 evaluateJavascript，3s 兜底）。
     * 回调值为 JSON 序列化结果，字符串结果带引号（PanelFragment.pullBlob trim('"') 同口径）。
     */
    private fun scanStorage(r: Recipe, onDone: (String?) -> Unit) {
        val wv = webView
        val host = Uri.parse(wv?.url ?: "").host ?: ""
        if (wv == null || r.storageHosts.none { host.endsWith(it) }) {
            onDone(null); return
        }
        var settled = false
        val finish: (String?) -> Unit = { v -> if (!settled) { settled = true; onDone(v) } }
        main.postDelayed({ finish(null) }, 3000)
        wv.evaluateJavascript(
            "(function(){try{var ks=Object.keys(localStorage);" +
                "for(var i=0;i<ks.length;i++){var v=String(localStorage.getItem(ks[i])||'');" +
                "if(/^eyJ[\\w-]{8,}\\.[\\w-]{8,}\\./.test(v)){return v;}}" +
                "return '';}catch(e){return '';}})()"
        ) { json ->
            val token = json?.trim('"')?.takeIf { it.isNotEmpty() && it != "null" }
            finish(token)
        }
    }

    private fun extractAndConfirm() {
        if (extracting) return
        val r = recipe ?: return
        extracting = true
        extractBtn.isEnabled = false
        statusView.text = getString(R.string.browser_login_extracting)
        val cookieHeader = pickCookie(r)
        // v1.4.11 缺陷②：会话 Cookie 预检——配方要求的关键键缺失（登录态未落 Cookie，
        // 如 doubao 登录前仅 ttwid/msToken 等匿名项）时按未登录处理，拒绝预填必死档
        // （复用 browser_login_no_cookie 状态通道，插件侧 loginCookieHeader 同尺预检）。
        if (missingRequiredCookies(r, cookieHeader)) {
            doneExtracting()
            statusView.text = getString(R.string.browser_login_no_cookie)
            return
        }
        if (r.storageHosts.isNotEmpty()) {
            scanStorage(r) { token -> proceed(r, cookieHeader, token) }
        } else {
            proceed(r, cookieHeader, null)
        }
    }

    /** 配方声明 requiredCookieKeys 时，提取头须含至少一键（any-of）才算已登录。 */
    private fun missingRequiredCookies(r: Recipe, cookieHeader: String?): Boolean {
        if (r.requiredCookieKeys.isEmpty()) return false
        if (cookieHeader.isNullOrEmpty()) return true
        val keys = cookieHeader.split(';')
            .map { it.trim().substringBefore('=') }
            .filter { it.isNotEmpty() }
            .toSet()
        return r.requiredCookieKeys.none { it in keys }
    }

    private fun proceed(r: Recipe, cookieHeader: String?, storageToken: String?) {
        val extracted = storageToken ?: cookieHeader
        if (extracted.isNullOrEmpty()) {
            doneExtracting()
            statusView.text = getString(R.string.browser_login_no_cookie)
            return
        }
        val summary = describeExtraction(storageToken, cookieHeader)
        if (!coreReady()) {
            // 核心未运行：无建档通道——提取结果给出可复制兜底（回面板手动粘贴），不静默
            doneExtracting()
            showCopyFallback(extracted, summary)
            return
        }
        Thread {
            // auth-methods 与提交链都要求插件实例运行中：先幂等拉起（marketplace.go:415）
            AdminApi.ensurePluginRunning(r.plugin)
            val methods = AdminApi.authMethods(r.plugin)
            val method = methods?.firstOrNull { it.id == r.methodId }
            runOnUiThread {
                doneExtracting()
                if (method == null || method.fields.isEmpty()) {
                    // 插件未运行/清单不可得：同样走复制兜底
                    showCopyFallback(extracted, summary)
                } else {
                    showConfirmDialog(r, method, extracted, summary)
                }
            }
        }.start()
    }

    private fun doneExtracting() {
        extracting = false
        extractBtn.isEnabled = true
    }

    private fun describeExtraction(storageToken: String?, cookieHeader: String?): String = when {
        storageToken != null -> getString(R.string.browser_login_got_storage)
        cookieHeader != null -> getString(
            R.string.browser_login_got_cookie,
            cookieHeader.split(';').count { it.trim().isNotEmpty() })
        else -> ""
    }

    /** 提取成功但核心/插件不可用：展示值 + 复制按钮（回面板粘贴），不假装可提交。 */
    private fun showCopyFallback(value: String, summary: String) {
        statusView.text = getString(R.string.browser_login_core_down)
        val box = EditText(this).apply {
            setText(value)
            minLines = 3
            gravity = Gravity.TOP
            setTextIsSelectable(true)
        }
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(8), dp(20), 0)
            addView(TextView(this@BrowserLoginActivity).apply {
                textSize = 13f
                setTextColor(ui.color(this, R.attr.nxTextTertiary))
                text = summary
            })
            addView(box, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(8)
            })
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.browser_login_fallback_title))
            .setView(ScrollView(this).apply { addView(col) })
            .setPositiveButton(R.string.browser_login_copy) { _, _ -> copyText(this, box.text.toString()) }
            .setNegativeButton(R.string.browser_login_back, null)
            .show()
    }

    /** 确认单：预填字段 + 手动补齐字段 → 必填校验 → 提交建档。 */
    private fun showConfirmDialog(
        r: Recipe,
        method: AdminApi.AuthMethodInfo,
        extracted: String,
        summary: String,
    ) {
        val editors = mutableMapOf<String, EditText>()
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(8), dp(20), 0)
        }
        col.addView(TextView(this).apply {
            textSize = 13f
            setTextColor(ui.color(this, R.attr.nxTextTertiary))
            text = summary
        })
        col.addView(TextView(this).apply {
            textSize = 13f
            setTextColor(ui.color(this, R.attr.nxTextTertiary))
            text = getString(R.string.browser_login_manual_hint)
            setPadding(0, dp(6), 0, 0)
        })
        for (f in method.fields) {
            col.addView(TextView(this).apply {
                textSize = 12f
                setTextColor(ui.color(this, R.attr.nxTextTertiary))
                text = if (f.required) "${f.label} *" else getString(R.string.browser_login_optional, f.label)
                setPadding(0, dp(10), 0, dp(2))
            })
            val et = EditText(this).apply {
                hint = f.placeholder.ifEmpty { f.label }
                if (f.type == "textarea") {
                    inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
                    minLines = 3
                    gravity = Gravity.TOP
                } else {
                    inputType = InputType.TYPE_CLASS_TEXT
                    setSingleLine(true)
                }
                if (f.name == r.cookieField) setText(extracted)
            }
            editors[f.name] = et
            col.addView(et, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
        val scroll = ScrollView(this).apply { addView(col) }
        val dialog = AlertDialog.Builder(this)
            .setTitle("${r.labelZh} · ${method.label}")
            .setView(scroll)
            .create()
        dialog.setButton(AlertDialog.BUTTON_POSITIVE, getString(R.string.browser_login_submit)) { _, _ -> }
        dialog.setButton(AlertDialog.BUTTON_NEGATIVE, getString(R.string.browser_login_back)) { _, _ -> }
        dialog.show()
        // 覆写确认键：必填校验不过不关单（AlertDialog 默认点击即 dismiss，不可回改）
        dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
            val form = editors.mapValues { (_, et) -> et.text.toString().trim() }
            val missing = method.fields.filter { it.required && form[it.name].isNullOrEmpty() }
            if (missing.isNotEmpty()) {
                toast(this, getString(
                    R.string.browser_login_missing_required, missing.joinToString("、") { it.label }))
            } else {
                dialog.dismiss()
                submit(r, method.id, form)
            }
        }
    }

    private fun submit(r: Recipe, methodId: String, form: Map<String, String>) {
        statusView.text = getString(R.string.browser_login_submitting)
        extractBtn.isEnabled = false
        Thread {
            val res = AdminApi.submitLogin(r.plugin, methodId, form)
            runOnUiThread {
                extractBtn.isEnabled = true
                res.onSuccess { outcome ->
                    when (outcome) {
                        is AdminApi.LoginOutcome.Done -> {
                            statusView.text = getString(
                                R.string.browser_login_success, outcome.accountId)
                            toast(this, getString(R.string.browser_login_success_toast))
                            // 深链回面板账号列表（coreFixes #2：MainActivity
                            // EXTRA_OPEN_ROUTE → PanelFragment.openRoute 单一路径）
                            main.postDelayed({ backToPanel("/accounts") }, 900)
                        }
                        is AdminApi.LoginOutcome.NextStep -> {
                            statusView.text = getString(R.string.browser_login_need_next)
                            toast(this, getString(R.string.browser_login_need_next))
                            main.postDelayed({
                                backToPanel("/accounts?add=1&plugin=${r.plugin}")
                            }, 900)
                        }
                    }
                }.onFailure { e ->
                    statusView.text = getString(
                        R.string.browser_login_submit_failed, e.message ?: "?")
                }
            }
        }.start()
    }

    private fun backToPanel(route: String) {
        startActivity(Intent(this, MainActivity::class.java).apply {
            putExtra(MainActivity.EXTRA_OPEN_TAB, 1)
            putExtra(MainActivity.EXTRA_OPEN_ROUTE, route)
            addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        })
        finish()
    }

    private fun coreReady(): Boolean =
        CoreController.state == CoreController.State.RUNNING && CoreController.gatewayPort > 0

    override fun onDestroy() {
        main.removeCallbacksAndMessages(null)
        webView?.destroy()
        webView = null
        super.onDestroy()
    }
}

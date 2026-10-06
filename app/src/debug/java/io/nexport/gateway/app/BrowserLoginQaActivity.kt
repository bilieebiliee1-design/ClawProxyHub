package io.nexport.gateway.app

import io.nexport.gateway.R

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.os.Bundle
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

/**
 * BrowserLoginQaActivity — 【仅 debug 构建存在，release 源集不含本目录】
 * v1.4.9 ① M1 前置 QA 载具（Pixel_3a_API_35_x86_64 全新安装）：
 *
 * QA-a window.open handoff（coreFixes #3a）：以与 PanelFragment.configureWebView
 * （PanelFragment.kt:258-274）一致的 WebView 配置（javaScriptCanOpenWindowsAutomatically
 * =false + setSupportMultipleWindows(false) + 无 onCreateWindow 的裸 WebChromeClient）
 * 加载 mock 页，页面 window.open(target,'_blank')。判据：shouldOverrideUrlLoading
 * 是否对 target 触发（= 面板 handleUrl→openExternal(Custom Tabs) 链路成立）。
 *
 * QA-b CookieManager HttpOnly（coreFixes #3b）：mock 页落 Set-Cookie(HttpOnly)，
 * onPageFinished 后 CookieManager.getCookie(目标) 是否含该 HttpOnly cookie
 * （= M1 六家 Cookie 预填的前提）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class BrowserLoginQaActivity : BaseActivity() {

    private val host = "http://127.0.0.1:8199" // adb reverse tcp:8199 tcp:8199（明文白名单内）

    private lateinit var status: TextView
    private lateinit var webViewHost: LinearLayout
    private var webView: WebView? = null

    private fun log(msg: String) {
        android.util.Log.i("NXQA", msg)
        runOnUiThread {
            status.text = "${status.text}\n$msg"
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            addView(Button(this@BrowserLoginQaActivity).apply {
                text = "QA-a window.open"
                setOnClickListener { runQaA() }
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(Button(this@BrowserLoginQaActivity).apply {
                text = "QA-b HttpOnly"
                setOnClickListener { runQaB() }
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(Button(this@BrowserLoginQaActivity).apply {
                text = "QA-c roundtrip+domain"
                setOnClickListener { runQaC() }
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        })
        status = TextView(this).apply { textSize = 12f; text = "NXQA ready ($host)" }
        root.addView(ScrollView(this).apply {
            addView(status)
        }, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        webViewHost = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(webViewHost, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        setContentView(root)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun ensureWebView(panelStyle: Boolean): WebView {
        webView?.let { return it }
        val wv = WebView(this)
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            // 以下两行 = PanelFragment.configureWebView 同款（QA-a 平台行为判定的前提）
            javaScriptCanOpenWindowsAutomatically = !panelStyle // 面板=false
            if (panelStyle) setSupportMultipleWindows(false) else setSupportMultipleWindows(false)
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }
        CookieManager.getInstance().setAcceptCookie(true)
        wv.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                log("SOVL: ${request.url}") // 面板 handleUrl 等价信号
                val scheme = request.url.scheme ?: return true
                if (scheme == "http" || scheme == "https") {
                    val h = request.url.host ?: ""
                    if (h == "127.0.0.1" || h == "localhost") return false
                    openExternal(this@BrowserLoginQaActivity, request.url.toString())
                    return true
                }
                return true
            }

            override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                log("START: $url")
            }

            override fun onPageFinished(view: WebView, url: String) {
                log("FINISHED: $url")
                if (url.contains("qa-httponly")) reportCookie(url)
            }

            override fun onReceivedError(
                view: WebView, request: WebResourceRequest, error: WebResourceError
            ) {
                if (request.isForMainFrame) log("ERROR: ${error.description}")
            }
        }
        // 裸 WebChromeClient（无 onCreateWindow）——与面板一致
        wv.webChromeClient = object : android.webkit.WebChromeClient() {}
        webViewHost.addView(wv, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        webView = wv
        return wv
    }

    private fun runQaA() {
        webViewHost.removeAllViews(); webView = null
        val wv = ensureWebView(panelStyle = true)
        log("QA-a BEGIN（面板同款配置）")
        wv.loadUrl("$host/qa-windowopen")
        android.os.Handler(mainLooper).postDelayed({
            log("QA-a END: 当前 URL=${webView?.url}（若含 qa-target 或上方出现 SOVL:…qa-target 即 handoff 成立）")
        }, 6000)
    }

    private fun runQaB() {
        webViewHost.removeAllViews(); webView = null
        val wv = ensureWebView(panelStyle = false)
        log("QA-b BEGIN")
        wv.loadUrl("$host/qa-httponly")
    }

    /** QA-c：Java setCookie 往返（隔离 API 本身）+ 域名宿主（隔离 127.0.0.1 特殊性）。 */
    private fun runQaC() {
        webViewHost.removeAllViews(); webView = null
        val wv = ensureWebView(panelStyle = false)
        log("QA-c BEGIN: roundtrip")
        Thread {
            val cm = CookieManager.getInstance()
            cm.setCookie("$host/", "QA_JAVA=roundtrip; Path=/")
            cm.flush()
            val back = cm.getCookie("$host")
            log("QA-c ROUNDTRIP: getCookie=$back")
            runOnUiThread {
                log("QA-c 加载 httpbin.org 真实域名 Set-Cookie(HttpOnly)")
                wv.loadUrl("https://httpbin.org/response-headers?Set-Cookie=QA_HTONLY%3Dsecret123%3B%20HttpOnly")
                android.os.Handler(mainLooper).postDelayed({
                    val c = CookieManager.getInstance().getCookie("https://httpbin.org")
                    log("QA-c DOMAIN_COOKIE_RAW: ${c ?: "<null>"}")
                    log("QA-c DOMAIN_RESULT: HttpOnly ${
                        if (c?.contains("QA_HTONLY=secret123") == true) "RETAINED（M1 前提成立）" else "DROPPED"
                    }")
                }, 6000)
            }
        }.start()
    }

    private fun reportCookie(url: String) {
        Thread {
            // 与 BrowserLoginActivity.pickCookie 同一 API 面：CookieManager.getCookie
            val c = CookieManager.getInstance().getCookie(url)
            log("COOKIE_RAW: ${c ?: "<null>"}")
            val httponly = c?.contains("QA_HTONLY=secret123") == true
            log("QA-b RESULT: HttpOnly ${if (httponly) "RETAINED（M1 预填前提成立）" else "DROPPED（M1 需改配方）"}")
        }.start()
    }

    override fun onDestroy() {
        webView?.destroy()
        webView = null
        super.onDestroy()
    }
}

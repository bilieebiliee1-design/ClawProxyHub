package io.nexport.gateway.app

import io.nexport.gateway.BuildConfig
import io.nexport.gateway.R
import androidx.core.content.ContextCompat
import androidx.core.graphics.drawable.RoundedBitmapDrawableFactory

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.fragment.app.Fragment
import com.google.android.material.dialog.MaterialAlertDialogBuilder

/**
 * AboutFragment — 「关于」页签（aboutSpec v1.3.0 改版）：
 *  ⓪赞赏码 banner（页首第一张卡）：72dp 圆角缩略图 +「赞赏支持」→ overlay 预览页
 *    （SupportPageFragment：大图 + 扫码提示 + 保存图片 SAF + 分享 FileProvider）；
 *  ①版本信息（NexPort versionName + versionCode + 核心版本；检查更新不设新入口——核心已有
 *    /admin/version 更新检查，由面板承载，壳不重复。v1.3.0 ⑤：版本行经 CoreController
 *    监听器 + onHiddenChanged 实时刷新，修复页签 show/hide 不重建导致的核心启动后
 *    仍显示「核心版本：未启动」缺陷）；
 *  ②使用说明（8 节分步图文文案 → overlay 全文页）；
 *  ③交流反馈（QQ 群 1124936153 大号展示 + 复制群号 + 「一键加群」四步回退链）；
 *  ④协议与声明（使用协议/免责声明 → TextPageFragment 各自 Markdown 渲染）；
 *  ⑤许可与署名（AGPL 署名链三件套原样保留、不可隐藏不可折叠：上游 ClawProxyHub +
 *    LICENSE + THIRD_PARTY_NOTICES。v1.3.0 ④：随包源码包 nexport-source.zip 自 APK
 *    assets 移除，源码提供改为 BuildConfig.FORK_SOURCE_URL 指向的公开 fork 仓库，
 *    说明行超链保留。v1.4.1 ②：新增 fork 二改声明行——欢迎 fork 但必须依规
 *    AGPL-3.0（保留 LICENSE 与署名、开放源码），并引导为本仓库与上游点 Star）。
 *
 * 卡片化修复排版（aboutSpec ①）：全部组 = groupHeader + ui.card{listRow+Ui.separator}，
 * 行结构复用 optionEntry+chevron，与设置页同一视觉（v1.1.0 的 groupCard 不包 ui.card
 * 导致行悬空、卡中套卡问题修复）。
 *
 * 注意：二级全文页一律走 MainActivity.openOverlay（全屏 overlay 容器），不得沿用
 * 页签容器 replace——旧路径会移除同容器的 PanelFragment 并销毁 WebView。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class AboutFragment : Fragment() {

    private val ui get() = Ui

    /** 产品与版本卡正文（v1.3.0 ⑤：核心状态变化时实时刷新，修复「核心版本：未启动」残留）。 */
    private var versionBody: TextView? = null

    private val coreListener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            if (isAdded) refreshCoreVersion()
        }

        override fun onTunnelEvent(state: String, url: String, message: String) {
            if (isAdded) refreshCoreVersion()
        }
    }

    private fun coreVerNow(c: Context): String =
        if (CoreController.state == CoreController.State.RUNNING) BridgeVersion.current()
        else c.getString(R.string.core_state_not_started)

    private fun refreshCoreVersion() {
        val c = context ?: return
        versionBody?.text = c.getString(
            R.string.about_product_text, appVersion(c), coreVerNow(c))
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val version = appVersion(c)
        val coreVer = coreVerNow(c)

        // ⓪ 赞赏码 banner（页首第一张卡，无分组头）
        // 行结构：72dp 固定缩略图 + 文字列(weight) + chevron（不经 rowAligned——
        // 其首子会被 weight=1 拉伸，QR 缩略图须保持 72dp 固定尺寸）
        val cardSupport = ui.card(c) {
            val row = LinearLayout(c).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = android.view.Gravity.CENTER_VERTICAL
                addView(supportThumb(c))
                addView(optionEntry(c,
                    c.getString(R.string.support_banner_title),
                    c.getString(R.string.support_banner_sub)),
                    LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                        .apply { marginStart = c.dp(12) })
                addView(chevron(c))
            }
            addView(ui.listRow(c, row) { (activity as? MainActivity)?.openOverlay(SupportPageFragment()) })
        }

        // ① 版本信息
        val groupVersion = groupCard(c, c.getString(R.string.about_group_version), listOf(
            ui.card(c) {
                addView(ui.title(c, c.getString(R.string.about_product)))
                val body = ui.body(c, c.getString(R.string.about_product_text, version, coreVer)).apply {
                    setPadding(0, c.dp(6), 0, 0)
                }
                addView(body)
                versionBody = body // v1.3.0 ⑤：状态变化实时刷新（onHiddenChanged/监听器）
                addView(ui.hint(c, c.getString(R.string.about_version_code_fmt,
                    runCatching {
                        c.packageManager.getPackageInfo(c.packageName, 0).let { pi ->
                            if (android.os.Build.VERSION.SDK_INT >= 28) pi.longVersionCode.toInt() else @Suppress("DEPRECATION") pi.versionCode
                        }
                    }.getOrDefault(4))).apply { setPadding(0, c.dp(2), 0, 0) })
                addView(ui.hint(c, c.getString(R.string.about_update_hint)).apply { setPadding(0, c.dp(2), 0, 0) })
            }
        ))

        // ② 使用说明（v1.4.0 ⑩：原组标题/行标题/副标题三处同文「使用说明」，去重并
        // 改善标题层级 = 组标题(使用说明) > 行标题(快速上手指南) > 副标题(8 节概览)）
        val groupGuide = groupCard(c, c.getString(R.string.about_group_guide), listOf(
            ui.card(c) {
                addView(ui.listRow(c,
                    optionEntry(c,
                        c.getString(R.string.about_guide_entry_title),
                        c.getString(R.string.about_guide_entry_sub)),
                    trailing = chevron(c)
                ) { (activity as? MainActivity)?.openOverlay(GuidePageFragment()) })
            }
        ))

        // ②b Lua 插件开发（v1.4.0 收尾②）：一句话简介 + 查看开发指南（应用内
        // Markdown 渲染，复用 LicenseViewFragment 的 assets+Markwon 基建）+
        // 三种 SAF 导出（SKILL.md / 官方示例 autoclaw / hello-world 模板，zip 根
        // 即 manifest.json 可直接离线安装）。资产位于 assets/lua-dev-skill/。
        val groupLuaDev = groupCard(c, c.getString(R.string.lua_dev_title), listOf(
            ui.card(c) {
                addView(ui.body(c, c.getString(R.string.lua_dev_intro)).apply {
                    setPadding(0, c.dp(2), 0, 0)
                })
                addView(ui.hint(c, c.getString(R.string.lua_dev_export_hint)).apply {
                    setPadding(0, c.dp(6), 0, 0)
                })
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.lua_dev_open_guide), ""), trailing = chevron(c)
                ) { (activity as? MainActivity)?.openOverlay(
                    LicenseViewFragment.newInstance("lua-dev-skill/SKILL.md", c.getString(R.string.lua_dev_guide_title))) })
                addView(ui.separator(c))
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.lua_dev_export_skill), "assets/lua-dev-skill/SKILL.md"), trailing = chevron(c)
                ) { exportSkillLauncher.launch("nexport-lua-dev-SKILL.md") })
                addView(ui.separator(c))
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.lua_dev_export_autoclaw), "examples/autoclaw · manifest+main+icon"), trailing = chevron(c)
                ) { pendingZipSource = "lua-dev-skill/examples/autoclaw"; exportZipLauncher.launch("autoclaw-lua-plugin.zip") })
                addView(ui.separator(c))
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.lua_dev_export_hello), "examples/hello-world · manifest+main"), trailing = chevron(c)
                ) { pendingZipSource = "lua-dev-skill/examples/hello-world"; exportZipLauncher.launch("hello-world-lua-template.zip") })
            }
        ))

        // ③ 交流反馈：QQ 群大号展示 + 复制 + 一键加群（四步回退链）
        val cardFeedback = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.about_qq_group)))
            addView(TextView(c).apply {
                text = c.getString(R.string.about_qq_number)
                textSize = 28f
                setTypeface(Typeface.MONOSPACE, Typeface.BOLD)
                setTextColor(ContextCompat.getColorStateList(context, R.color.nx_text_primary))
                setPadding(0, c.dp(8), 0, c.dp(4))
                setTextIsSelectable(true)
            })
            addView(ui.button(c, c.getString(R.string.about_qq_join)) { joinQQGroup(this@AboutFragment) }
                .apply { (layoutParams as? LinearLayout.LayoutParams)?.topMargin = c.dp(8) })
            addView(ui.button(c, c.getString(R.string.about_qq_copy)) {
                copyText(c, c.getString(R.string.about_qq_number))
            }.apply { (layoutParams as? LinearLayout.LayoutParams)?.topMargin = c.dp(4) })
            addView(ui.hint(c, c.getString(R.string.about_qq_hint)).apply { setPadding(0, c.dp(6), 0, 0) })
        }
        val groupFeedback = groupCard(c, c.getString(R.string.about_group_feedback), listOf(cardFeedback))

        // ④ 协议与声明（v1.2.0 拆分两篇 Markdown：TextPageFragment 按入口分载）
        val groupEula = groupCard(c, c.getString(R.string.about_group_eula), listOf(
            ui.card(c) {
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.about_eula_entry), ""),
                    trailing = chevron(c)
                ) { (activity as? MainActivity)?.openOverlay(TextPageFragment.newInstance(PAGE_EULA)) })
                addView(ui.separator(c))
                addView(ui.listRow(c,
                    optionEntry(c, c.getString(R.string.about_disclaimer_entry), ""),
                    trailing = chevron(c)
                ) { (activity as? MainActivity)?.openOverlay(TextPageFragment.newInstance(PAGE_DISCLAIMER)) })
            }
        ))

        // ⑤ 许可与署名（AGPL 署名链三件套，不可隐藏；v1.3.0 ④：随包源码包移除，
        // 源码提供改为 FORK_SOURCE_URL 指向的公开 fork 仓库，说明行超链保留）
        val licenseRows = ui.card(c) {
            addView(ui.title(c, c.getString(R.string.about_upstream)))
            addView(ui.body(c, c.getString(R.string.about_upstream_text)).apply { setPadding(0, c.dp(6), 0, 0) })
            addView(ui.body(c, BuildConfig.UPSTREAM_URL, mono = true).apply { setPadding(0, c.dp(2), 0, 0) })
            addView(LinearLayout(c).apply {
                orientation = LinearLayout.HORIZONTAL
                addView(ui.textButton(c, c.getString(R.string.about_upstream_open)) {
                    openExternal(c, BuildConfig.UPSTREAM_URL)
                })
                addView(ui.textButton(c, c.getString(R.string.copy_link)) {
                    copyText(c, BuildConfig.UPSTREAM_URL)
                })
            })
            addView(ui.body(c, c.getString(R.string.about_licenses_text)).apply { setPadding(0, c.dp(6), 0, 0) })
            val forkUrl = BuildConfig.FORK_SOURCE_URL
            if (forkUrl.isNotBlank()) {
                // FORK_SOURCE_URL 保留作说明行超链（aboutSpec ⑤）
                addView(ui.body(c, forkUrl, mono = true).apply { setPadding(0, c.dp(2), 0, 0) })
                addView(LinearLayout(c).apply {
                    orientation = LinearLayout.HORIZONTAL
                    addView(ui.textButton(c, c.getString(R.string.open_link)) {
                        openExternal(c, forkUrl)
                    })
                    addView(ui.textButton(c, c.getString(R.string.copy_link)) {
                        copyText(c, forkUrl)
                    })
                })
            }
            // v1.4.1 ②：fork 二改声明——AGPL-3.0 依规（保留 LICENSE 与署名、开放源码）
            // + 为本仓库与上游点 Star 的引导
            addView(ui.body(c, c.getString(R.string.about_fork_statement)).apply {
                setPadding(0, c.dp(8), 0, 0)
            })
            addView(ui.separator(c))
            addView(ui.listRow(c,
                optionEntry(c, c.getString(R.string.license_view), ""),
                trailing = chevron(c)
            ) { (activity as? MainActivity)?.openOverlay(LicenseViewFragment.newInstance("LICENSE", "LICENSE — GNU AGPL-3.0")) })
            addView(ui.separator(c))
            addView(ui.listRow(c,
                optionEntry(c, c.getString(R.string.notices_view), ""),
                trailing = chevron(c)
            ) { (activity as? MainActivity)?.openOverlay(LicenseViewFragment.newInstance("THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES")) })
        }
        val groupLicense = groupCard(c, c.getString(R.string.about_group_license), listOf(licenseRows))

        return ui.pageWithTitle(c, c.getString(R.string.about_title),
            cardSupport, groupVersion, groupGuide, groupLuaDev, groupFeedback, groupEula, groupLicense)
    }

    // ---- Lua 插件开发卡导出（v1.4.0 收尾②；SAF CreateDocument，无新权限） ----

    private val exportSkillLauncher = registerForActivityResult(
        ActivityResultContracts.CreateDocument("text/markdown")
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        exportAssetTo("lua-dev-skill/SKILL.md", uri)
    }

    /** zip 导出走公共 launcher：pendingZipSource 记录待打包目录，onCreate 后取用。 */
    private val exportZipLauncher = registerForActivityResult(
        ActivityResultContracts.CreateDocument("application/zip")
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        val src = pendingZipSource
        pendingZipSource = ""
        if (src.isNotEmpty()) exportZipTo(src, uri)
    }
    private var pendingZipSource: String = ""

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        // v1.3.0 ⑤：页签 show/hide 不重建视图，核心状态变化经监听器实时刷新版本行
        CoreController.addListener(coreListener)
    }

    /** 页签 show/hide 联动（MainActivity 不触发 onResume，须走 onHiddenChanged）。 */
    override fun onHiddenChanged(hidden: Boolean) {
        super.onHiddenChanged(hidden)
        if (!hidden) refreshCoreVersion()
    }

    override fun onDestroyView() {
        CoreController.removeListener(coreListener)
        versionBody = null
        super.onDestroyView()
    }

    /** 单文件导出：assets → SAF uri（收尾②；合规源码包复用，消息可定制）。 */
    private fun exportAssetTo(
        assetPath: String, uri: android.net.Uri,
        savedMsg: (Context, String) -> String = { c, name -> c.getString(R.string.lua_dev_export_saved, name) },
        failMsg: (Context, String) -> String = { c, e -> c.getString(R.string.lua_dev_export_failed, e) },
    ) {
        val c = requireContext()
        try {
            c.contentResolver.openOutputStream(uri)?.use { out ->
                c.assets.open(assetPath).use { it.copyTo(out) }
            }
            toast(c, savedMsg(c, assetPath.substringAfterLast("/")))
        } catch (t: Throwable) {
            toast(c, failMsg(c, t.message ?: "?"))
        }
    }

    /** 目录打包导出：assets 下目录内全部文件平铺为 zip 根（manifest.json 在根，可直接离线安装）。 */
    private fun exportZipTo(assetDir: String, uri: android.net.Uri) {
        val c = requireContext()
        try {
            c.contentResolver.openOutputStream(uri)?.use { out ->
                java.util.zip.ZipOutputStream(out.buffered()).use { zip ->
                    val files = c.assets.list(assetDir).orEmpty()
                    for (name in files) {
                        zip.putNextEntry(java.util.zip.ZipEntry(name))
                        c.assets.open(assetDir + "/" + name).use { it.copyTo(zip) }
                        zip.closeEntry()
                    }
                }
            }
            toast(c, c.getString(R.string.lua_dev_export_saved,
                assetDir.substringAfterLast("/") + ".zip"))
        } catch (t: Throwable) {
            toast(c, c.getString(R.string.lua_dev_export_failed, t.message ?: "?"))
        }
    }

    /** 赞赏码 72dp 圆角缩略图（drawable-nodpi/support_qr.jpg，nodpi 防重采样）。 */
    private fun supportThumb(c: Context): View {
        val bmp = BitmapFactory.decodeResource(c.resources, R.drawable.support_qr)
        val d = RoundedBitmapDrawableFactory.create(c.resources, bmp)
        d.cornerRadius = c.dpF(12)
        d.setAntiAlias(true)
        return android.widget.ImageView(c).apply {
            setImageDrawable(d)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            contentDescription = c.getString(R.string.support_banner_title)
            layoutParams = LinearLayout.LayoutParams(c.dp(72), c.dp(72))
        }
    }

    /** 卡片化分组（aboutSpec ①）：groupHeader + ui.card{row+separator}。 */
    private fun groupCard(c: Context, title: String, cards: List<View>): View {
        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(ui.groupHeader(c, title))
        cards.forEach { wrap.addView(it) }
        return wrap
    }

    private fun optionEntry(c: Context, title: String, sub: String): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(TextView(c).apply {
            text = title
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        })
        if (sub.isNotEmpty()) col.addView(TextView(c).apply {
            text = sub
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            setPadding(0, c.dp(1), 0, 0)
        })
        return col
    }

    private fun chevron(c: Context): View = TextView(c).apply {
        text = "›"
        textSize = 20f
        setTextColor(Ui.color(this, R.attr.nxTextTertiary))
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    private fun appVersion(c: Context): String = runCatching {
        c.packageManager.getPackageInfo(c.packageName, 0).versionName ?: "1.3.0"
    }.getOrDefault("1.3.0")

    companion object {
        const val PAGE_EULA = "eula"
        const val PAGE_DISCLAIMER = "disclaimer"
    }
}

/**
 * QQ 一键加群四步回退链（aboutSpec ③ / homeQuickActionsSpec ⑥ 共用 helper）：
 *  第 0 步无条件复制群号 → ⅰ mqqapi URI 交 com.tencent.mobileqq → ⅱ 同 URI 交
 *  com.tencent.tim → ⅲ 网页加群页 https://qm.qq.com/（openExternal Custom Tabs，
 *  hint 粘贴群号搜索加群；群主未生成 key 时无法一键直达，文案如实）→ ⅳ 全部失败
 *  → MaterialAlertDialog「未检测到 QQ/TIM」+ 群号大字 +「复制群号」。
 */
fun joinQQGroup(host: Fragment) {
    val c = host.requireContext()
    copyText(c, c.getString(R.string.about_qq_number), showToast = false) // 第 0 步无条件复制
    val uri = Uri.parse(
        "mqqapi://card/show_pslcard?src_type=internal&version=1" +
            "&uin=1124936153&card_type=group&source=qrcode")
    // ⅰ/ⅱ：QQ → TIM（manifest <queries> 声明保证 API 30+ 包可见性）
    for (pkg in listOf("com.tencent.mobileqq", "com.tencent.tim")) {
        if (c.packageManager.getLaunchIntentForPackage(pkg) == null) continue
        val i = Intent(Intent.ACTION_VIEW, uri).setPackage(pkg)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            host.startActivity(i)
            toast(c, c.getString(R.string.about_qq_join))
            return
        } catch (_: ActivityNotFoundException) { /* 尝试下一级 */ }
    }
    // ⅲ 网页加群页（openExternal：Custom Tabs，失败回退浏览器）
    try {
        openExternal(c, "https://qm.qq.com/")
        toast(c, c.getString(R.string.about_qq_join_web_hint))
    } catch (_: Throwable) {
        // ⅳ 全部失败
        MaterialAlertDialogBuilder(c)
            .setTitle(R.string.about_qq_join_failed)
            .setMessage(c.getString(R.string.about_qq_number))
            .setPositiveButton(R.string.about_qq_copy) { _, _ ->
                copyText(c, c.getString(R.string.about_qq_number))
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }
}

/**
 * SupportPageFragment — 赞赏码预览页（aboutSpec ②）：居中大图 + 提示行「截图或保存后，
 * 在微信/支付宝中扫码」+「保存图片」（ACTION_CREATE_DOCUMENT image/jpeg，SAF，走
 * PanelFragment.launchSaf 同款模式，无新权限）与「分享」（先复制到 cacheDir/shared/ 再
 * FileProvider.getUriForFile → ACTION_SEND image/jpeg；manifest 已登记 provider +
 * res/xml/file_paths.xml cache-path shared/）。
 * v1.4.1 ①：大图下方新增赞赏文案卡——为什么赞赏（持续维护动力 / 为爱发电）与
 * VIP 会员群说明（反馈优先满足、持久技术支持、更多会员特权），附 QQ 群 1124936153
 * 加群入口（复用 joinQQGroup 四步回退链）。首页「一键赞赏」与关于页 banner 共用本页。
 */
class SupportPageFragment : Fragment() {

    private val ui get() = Ui

    private val saveLauncher = registerForActivityResult(
        ActivityResultContracts.CreateDocument("image/jpeg")
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        val c = requireContext()
        try {
            c.contentResolver.openOutputStream(uri)?.use { out ->
                c.resources.openRawResource(R.drawable.support_qr).use { it.copyTo(out) }
            }
            toast(c, c.getString(R.string.support_saved, "support-nexport.jpg"))
        } catch (t: Throwable) {
            toast(c, c.getString(R.string.support_save_failed, t.message ?: "?"))
        }
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(backHeader(c, c.getString(R.string.support_banner_title)) { (activity as? MainActivity)?.popOverlayOrFinish() })

        // 居中大图（ScrollView 内可滚动；宽 = 屏宽 - 边距）
        val scroll = ScrollView(c)
        val center = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            gravity = android.view.Gravity.CENTER_HORIZONTAL
            setPadding(c.dp(16), c.dp(24), c.dp(16), c.dp(24))
        }
        val side = (c.resources.displayMetrics.widthPixels * 0.72f).toInt()
        center.addView(android.widget.ImageView(c).apply {
            setImageResource(R.drawable.support_qr)
            adjustViewBounds = true
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            contentDescription = c.getString(R.string.support_banner_title)
        }, LinearLayout.LayoutParams(side, side).apply { gravity = android.view.Gravity.CENTER_HORIZONTAL })
        center.addView(ui.hint(c, c.getString(R.string.support_scan_hint)).apply {
            gravity = android.view.Gravity.CENTER
            setPadding(0, c.dp(16), 0, 0)
        })

        // v1.4.1 ①：赞赏文案卡（为什么赞赏 / VIP 会员群）。颜色一律走 nx* 主题 token
        // （亮暗自动跟随），与关于页同一卡片/字阶体系。一键加群复用 joinQQGroup
        // 四步回退链（QQ → TIM → 网页加群页 → 复制群号对话框），见本文件底部 helper。
        center.addView(ui.card(c) {
            addView(ui.title(c, c.getString(R.string.support_motive_title)))
            addView(ui.body(c, c.getString(R.string.support_motive_1)).apply {
                setPadding(0, c.dp(8), 0, 0)
            })
            addView(ui.body(c, c.getString(R.string.support_motive_2)).apply {
                setPadding(0, c.dp(2), 0, 0)
            })
            addView(ui.separator(c))
            addView(ui.title(c, c.getString(R.string.support_vip_title)).apply {
                setPadding(0, c.dp(8), 0, 0)
            })
            addView(ui.body(c, c.getString(R.string.support_vip_text)).apply {
                setPadding(0, c.dp(6), 0, 0)
            })
            addView(ui.secondary(c,
                c.getString(R.string.support_vip_qq_fmt, c.getString(R.string.about_qq_number)),
                mono = true).apply { setPadding(0, c.dp(6), 0, 0) })
            addView(ui.button(c, c.getString(R.string.about_qq_join)) {
                joinQQGroup(this@SupportPageFragment)
            }, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT
            ).apply { topMargin = c.dp(8) })
            addView(ui.hint(c, c.getString(R.string.about_qq_hint)).apply {
                setPadding(0, c.dp(6), 0, 0)
            })
        }, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = c.dp(16) })
        scroll.addView(center)
        col.addView(scroll, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))

        val actions = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        actions.addView(ui.button(c, c.getString(R.string.support_save)) {
            saveLauncher.launch("support-nexport.jpg")
        })
        actions.addView(ui.subButton(c, c.getString(R.string.support_share)) { shareQR(c) })
        col.addView(actions.apply { setPadding(c.dp(16), c.dp(4), c.dp(16), c.dp(16)) })
        return col
    }

    /** 分享：复制到 cacheDir/shared/ → FileProvider URI → ACTION_SEND image/jpeg。 */
    private fun shareQR(c: Context) {
        try {
            val dir = java.io.File(c.cacheDir, "shared").apply { mkdirs() }
            val f = java.io.File(dir, "support-nexport.jpg")
            c.resources.openRawResource(R.drawable.support_qr).use { input ->
                f.outputStream().use { input.copyTo(it) }
            }
            val uri = androidx.core.content.FileProvider.getUriForFile(
                c, "${c.packageName}.fileprovider", f)
            val i = Intent(Intent.ACTION_SEND).apply {
                type = "image/jpeg"
                putExtra(Intent.EXTRA_STREAM, uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(i, c.getString(R.string.support_share)))
        } catch (t: Throwable) {
            toast(c, c.getString(R.string.support_save_failed, t.message ?: "?"))
        }
    }
}

/**
 * GuidePageFragment — 使用说明全文页（8 节分步文案：纯文字编号步骤，每步一行主文 +
 * 一行辅助文，不内嵌截图、不虚构图片）。走 overlay 全屏容器。
 */
class GuidePageFragment : Fragment() {

    private val ui get() = Ui

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val cards = mutableListOf<View>()
        val sections = listOf(
            Triple(R.string.guide_s1_title, R.array.guide_s1_steps, R.array.guide_s1_subs),
            Triple(R.string.guide_s2_title, R.array.guide_s2_steps, R.array.guide_s2_subs),
            Triple(R.string.guide_s3_title, R.array.guide_s3_steps, R.array.guide_s3_subs),
            Triple(R.string.guide_s4_title, R.array.guide_s4_steps, R.array.guide_s4_subs),
            Triple(R.string.guide_s5_title, R.array.guide_s5_steps, R.array.guide_s5_subs),
            Triple(R.string.guide_s6_title, R.array.guide_s6_steps, R.array.guide_s6_subs),
            Triple(R.string.guide_s7_title, R.array.guide_s7_steps, R.array.guide_s7_subs),
            Triple(R.string.guide_s8_title, R.array.guide_s8_steps, R.array.guide_s8_subs),
        )
        sections.forEachIndexed { idx, (titleRes, stepsRes, subsRes) ->
            val steps = resources.getStringArray(stepsRes)
            val subs = resources.getStringArray(subsRes)
            cards += ui.card(c) {
                addView(ui.title(c, c.getString(titleRes)))
                steps.forEachIndexed { i, step ->
                    addView(ui.body(c, "${idx + 1}.${i + 1}  $step").apply {
                        setPadding(0, c.dp(8), 0, 0)
                    })
                    addView(ui.hint(c, subs.getOrElse(i) { "" }).apply { setPadding(0, c.dp(2), 0, 0) })
                }
            }
        }

        val root = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        root.addView(backHeader(c, c.getString(R.string.guide_page_title)))
        root.addView(ui.page(c, *cards.toTypedArray()),
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        return root
    }
}

/**
 * TextPageFragment — 协议/声明全文页（markdownSpec ①）：按入口分载——使用协议 =
 * R.string.eula_markdown、免责声明 = R.string.disclaimer_markdown（条款 1/3/5 与 2/4
 * 自 eula_text 忠实拆分，内容平移不新增承诺）；Markwon 渲染（跟随主题），保留可选中
 * 与「复制全文」（复制原始 markdown 源文）。
 */
class TextPageFragment : Fragment() {

    private val ui get() = Ui
    private var pageKey: String = AboutFragment.PAGE_EULA

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        arguments?.getString(ARG_KEY)?.let { pageKey = it }
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val (titleRes, markdownRes) = when (pageKey) {
            AboutFragment.PAGE_DISCLAIMER -> R.string.about_disclaimer_entry to R.string.disclaimer_markdown
            else -> R.string.about_eula_entry to R.string.eula_markdown
        }
        val title = c.getString(titleRes)
        val markdown = c.getString(markdownRes)
        val tv = TextView(c).apply {
            setTextIsSelectable(true)
            setPadding(c.dp(16), c.dp(8), c.dp(16), c.dp(24))
        }
        NexMarkdown.render(c, tv, markdown)
        val root = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            addView(backHeader(c, title))
            addView(ui.textButton(c, c.getString(R.string.copy)) { copyText(c, markdown) })
            addView(ScrollView(c).apply { addView(tv) },
                LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        }
        return root
    }

    companion object {
        private const val ARG_KEY = "pageKey"
        fun newInstance(pageKey: String): TextPageFragment =
            TextPageFragment().apply { arguments = Bundle().apply { putString(ARG_KEY, pageKey) } }
    }
}

/**
 * LicenseViewFragment — 许可证/声明全文查看（assets 内 LICENSE / THIRD_PARTY_NOTICES.md），
 * 附复制全文（markdownSpec ③）：THIRD_PARTY_NOTICES.md → Markwon 渲染（跟随主题，表格
 * 经 NexMarkdown TablePlugin 渲染）；LICENSE 纯文本等宽排版——MONOSPACE 13sp、行距 1.25、
 * 自动换行（v1.3.0 ②：原 HorizontalScrollView 方案在长行处仍被右缘裁切，改为按宽换行，
 * 无任何方向裁切）、保留可选中+复制。经 newInstance 构造（支撑 overlay 栈上的状态恢复）。
 */
class LicenseViewFragment : Fragment() {

    private val ui get() = Ui
    private var asset: String = ""
    private var title: String = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        arguments?.let {
            asset = it.getString(ARG_ASSET) ?: asset
            title = it.getString(ARG_TITLE) ?: title
        }
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val text = try {
            c.assets.open(asset).bufferedReader(Charsets.UTF_8).use { it.readText() }
        } catch (t: Throwable) {
            "(asset missing: $asset) ${t.message}"
        }

        val content: View
        val scroll: View
        if (asset.endsWith(".md", true)) {
            // THIRD_PARTY_NOTICES：Markwon 渲染（nx* token 亮暗跟随）
            val tv = TextView(c).apply {
                setTextIsSelectable(true)
                setPadding(c.dp(16), c.dp(8), c.dp(16), c.dp(24))
            }
            NexMarkdown.render(c, tv, text)
            scroll = ScrollView(c).apply { addView(tv) }
            content = scroll
        } else {
            // LICENSE：纯文本等宽排版 + 自动换行（不裁切；v1.3.0 ② 由横滚改为换行）
            val tv = TextView(c).apply {
                this.text = text
                textSize = 13f
                setTypeface(android.graphics.Typeface.MONOSPACE)
                setTextColor(Ui.color(this, R.attr.nxTextPrimary))
                setLineSpacing(0f, 1.25f)
                setPadding(c.dp(16), c.dp(8), c.dp(16), c.dp(24))
                setTextIsSelectable(true)
            }
            scroll = ScrollView(c).apply { addView(tv) }
            content = scroll
        }

        val root = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            addView(backHeader(c, title))
            addView(ui.textButton(c, c.getString(R.string.copy)) { copyText(c, text) })
            addView(content, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        }
        return root
    }

    companion object {
        private const val ARG_ASSET = "asset"
        private const val ARG_TITLE = "title"
        fun newInstance(asset: String, title: String): LicenseViewFragment =
            LicenseViewFragment().apply {
                arguments = Bundle().apply {
                    putString(ARG_ASSET, asset); putString(ARG_TITLE, title)
                }
            }
    }
}

/** 二级页头：返回 + 标题（overlay 页内返回按钮，与系统返回双通道）。 */
fun backHeader(c: Context, title: String, onBack: (() -> Unit)? = null): View {
    val btn = Ui.textButton(c, "← ${c.getString(R.string.back)}") {
        if (onBack != null) {
            onBack()
        } else {
            val act = c as? android.app.Activity
            if (act is MainActivity) act.popOverlayOrFinish() else act?.finish()
        }
    }
    return LinearLayout(c).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = android.view.Gravity.CENTER_VERTICAL
        addView(btn)
        addView(Ui.title(c, title).apply { setPadding(c.dp(8), 0, 0, 0) },
            LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
    }
}

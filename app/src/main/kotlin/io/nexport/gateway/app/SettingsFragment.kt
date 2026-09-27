package io.nexport.gateway.app

import io.nexport.gateway.R

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.text.InputType
import android.view.Gravity
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.os.Handler
import android.os.Looper
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.content.ContextCompat
import androidx.core.os.LocaleListCompat
import androidx.fragment.app.Fragment
import com.google.android.material.bottomsheet.BottomSheetDialog
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.progressindicator.LinearProgressIndicator
import com.google.android.material.snackbar.Snackbar
import java.io.File
import java.net.ServerSocket

/**
 * SettingsFragment — 「设置」页签（v1.4.0，HIG 分组内缩列表）：
 * 外观（主题/字体大小/预测性返回）、通用（语言/开机自启/退出并停止核心/关于行）、
 * 网络（网关端口 自动/固定+端口输入）、
 * v1.4.0 ⑥ 面板登录信息（自首页整体迁入：用户名/密码 显隐复制）、
 * 隧道（自首页整体迁入的临时隧道卡：开关+状态+域名+错误重试 + 自动重连）、
 * v1.4.0 ⑧ 保活与权限（原「服务与通知」更名并集中全部保活项：前台服务/通知权限/
 * 电池优化/自启动与后台运行[按厂商尽力引导]/悬浮窗保活）、
 * 安全（修改管理员密码/清除本机凭据记忆）、诊断（运行日志/清除缓存）。
 * 二级页（改密/日志）一律走 overlay 全屏容器。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class SettingsFragment : Fragment() {

    private val ui get() = Ui

    // 网关端口区
    private var portAutoBtn: com.google.android.material.button.MaterialButton? = null
    private var portFixedBtn: com.google.android.material.button.MaterialButton? = null
    private var portInput: EditText? = null
    private var portStatus: TextView? = null
    private var restartBar: LinearLayout? = null

    // 面板登录信息（v1.4.0 ⑥ 自首页迁入）
    private lateinit var credUserValue: TextView
    private lateinit var credUserCopy: View
    private lateinit var credPassValue: TextView
    private lateinit var credPassEye: View
    private lateinit var credPassCopy: View
    private var credRevealed = false

    // 隧道卡（v1.4.0 ⑥ 自首页迁入）
    private lateinit var tunnelSwitch: MaterialSwitch
    private lateinit var tunnelStateText: TextView
    private lateinit var tunnelProgress: LinearProgressIndicator
    private lateinit var tunnelUrlText: TextView
    private lateinit var tunnelWarn: TextView
    private lateinit var tunnelErrActions: LinearLayout
    private var switchingTunnel = false
    private var tunnelStartingSince: Long = 0
    private var tunnelListenerAttached = false
    private val main = Handler(Looper.getMainLooper())

    // 通知权限（保活与权限组）
    private var foregroundSwitch: MaterialSwitch? = null
    private var notifHint: TextView? = null
    private var pendingEnable = false

    // 悬浮窗保活（floatingWindowSpec，保活与权限组）
    private var floatingSwitch: MaterialSwitch? = null
    private var floatingHint: TextView? = null
    private var pendingFloatingEnable = false

    private val notifPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        val c = requireContext()
        if (granted) {
            Prefs.setKeepForeground(c, true)
            GatewayService.start(c)
        } else {
            // 拒绝分支：不改开关（notificationPermissionSpec ②），给降级文案
            Prefs.setKeepForeground(c, false)
            foregroundSwitch?.isChecked = false
            showNotifDeniedHint()
        }
    }

    /** 「保活与权限·通知权限」行专用 launcher（未授权时行内直接发起请求）。 */
    private val keepaliveNotifPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { refreshKeepaliveRows() }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        return ui.pageWithTitle(c, c.getString(R.string.settings_title),
            appearanceGroup(c), generalGroup(c), networkGroup(c),
            panelLoginGroup(c), tunnelGroup(c), serviceGroup(c),
            securityGroup(c), diagnosticsGroup(c))
    }

    override fun onHiddenChanged(hidden: Boolean) {
        super.onHiddenChanged(hidden)
        if (hidden) {
            main.removeCallbacks(tick)
            detachTunnelListener()
            credRevealed = false
            if (isAdded) refreshCredentials()
        } else {
            refreshPortSection()
            refreshCredentials()
            attachTunnelListener()
            refreshTunnelCard()
            main.postDelayed(tick, 1000)
        }
    }

    override fun onResume() {
        super.onResume()
        // 悬浮窗授权复查（floatingWindowSpec ②）：从系统授权页返回自动置位/降级
        refreshFloatingOnResume()
        refreshKeepaliveRows()
        refreshCredentials()
        attachTunnelListener()
        refreshTunnelCard()
        main.postDelayed(tick, 1000)
    }

    override fun onPause() {
        super.onPause()
        main.removeCallbacks(tick)
        detachTunnelListener()
        credRevealed = false
        if (isAdded) refreshCredentials()
    }

    /** 隧道卡 1s 节拍（重连倒计时/建立中秒数等动态行）。 */
    private val tick = object : Runnable {
        override fun run() {
            if (!isAdded) return
            refreshTunnelCard()
            main.postDelayed(this, 1000)
        }
    }

    private val tunnelListener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            if (isAdded) { refreshTunnelCard(); refreshPortSection() }
        }

        override fun onTunnelEvent(state: String, url: String, message: String) {
            if (isAdded) refreshTunnelCard()
        }
    }

    private fun attachTunnelListener() {
        if (!tunnelListenerAttached) { CoreController.addListener(tunnelListener); tunnelListenerAttached = true }
    }

    private fun detachTunnelListener() {
        if (tunnelListenerAttached) { CoreController.removeListener(tunnelListener); tunnelListenerAttached = false }
    }

    // ---- 外观 ----

    private fun appearanceGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }

        // 主题（单选·底部弹层）
        val themeEntry = optionText(c, c.getString(R.string.setting_theme), themeLabel(c))
        themeSummary = themeEntry.getChildAt(1) as? TextView
        val themeRow = ui.listRow(
            c,
            themeEntry,
            trailing = chevron(c)
        ) { showThemeSheet(c) }
        col.addView(themeRow)
        col.addView(ui.hint(c, c.getString(R.string.theme_subtitle)).apply { setPadding(0, 0, 0, c.dp(4)) })

        // 字体大小（单选 + 即时预览行）
        val fontRow = ui.listRow(
            c,
            optionText(c, c.getString(R.string.setting_font_size), fontLabel(c)),
            trailing = chevron(c)
        ) { showFontSheet(c) }
        col.addView(fontRow)
        col.addView(TextView(c).apply {
            text = c.getString(R.string.font_preview_sample)
            textSize = 15f
            setTypeface(typeface, android.graphics.Typeface.ITALIC)
            setTextColor(Ui.color(this, R.attr.nxTextSecondary))
            setPadding(0, 0, 0, c.dp(4))
        })

        // 预测性返回（开关）
        val pbSwitch = MaterialSwitch(c).apply { isChecked = Prefs.predictiveBack(c) }
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.setting_predictive_back), c.getString(R.string.predictive_back_subtitle)),
            trailing = pbSwitch
        ))
        pbSwitch.setOnCheckedChangeListener { _, checked ->
            Prefs.setPredictiveBack(c, checked)
            (activity as? MainActivity)?.installBackPolicy()
        }

        return groupCard(c, c.getString(R.string.group_appearance), col)
    }

    private fun showThemeSheet(c: Context) {
        val options = listOf(c.getString(R.string.theme_system), c.getString(R.string.theme_light), c.getString(R.string.theme_dark))
        val current = when (Prefs.themeMode(c)) { "light" -> 1; "dark" -> 2; else -> 0 }
        val col = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(16), c.dp(12), c.dp(16), c.dp(16))
        }
        options.forEachIndexed { i, label ->
            col.addView(choiceRow(c, radio(c, i == current), optionText(c, label, "")) {
                val mode = when (i) { 1 -> "light"; 2 -> "dark"; else -> "system" }
                Prefs.setThemeMode(c, mode)
                AppCompatDelegate.setDefaultNightMode(
                    when (i) { 1 -> AppCompatDelegate.MODE_NIGHT_NO; 2 -> AppCompatDelegate.MODE_NIGHT_YES; else -> AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM })
                // 等效夜间模式相同的两档间切换（跟随系统(浅)↔白天）不触发重建，
                // 行摘要须就地更新（QA 回归修复：重建后自愈→立即同步）
                themeSummary?.text = themeLabel(c)
                sheet?.dismiss()
            })
        }
        sheet = BottomSheetDialog(c).apply {
            setContentView(col)
            show()
        }
    }

    private var sheet: BottomSheetDialog? = null
    private var themeSummary: TextView? = null

    private fun showFontSheet(c: Context) {
        val options = listOf(
            c.getString(R.string.font_small), c.getString(R.string.font_standard),
            c.getString(R.string.font_large), c.getString(R.string.font_xlarge))
        val current = Prefs.fontSizeIndex(c)
        val col = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(16), c.dp(12), c.dp(16), c.dp(16))
        }
        options.forEachIndexed { i, label ->
            col.addView(choiceRow(c, radio(c, i == current), optionText(c, label, "")) {
                Prefs.setFontSizeIndex(c, i)
                sheet?.dismiss()
                activity?.recreate() // fontScale 在 attachBaseContext 覆写，需重建
            })
        }
        sheet = BottomSheetDialog(c).apply {
            setContentView(col)
            show()
        }
    }

    /** 单选行：前圆点 + 文案，整行可点。 */
    private fun choiceRow(c: Context, dot: View, content: View, onClick: () -> Unit): LinearLayout {
        val row = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = c.dp(48)
            isClickable = true
            isFocusable = true
            setOnClickListener { onClick() }
            addView(dot, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT))
            addView(content, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = c.dp(8)
            })
        }
        return row
    }

    private fun themeLabel(c: Context) = when (Prefs.themeMode(c)) {
        "light" -> c.getString(R.string.theme_light)
        "dark" -> c.getString(R.string.theme_dark)
        else -> c.getString(R.string.theme_system)
    }

    private fun fontLabel(c: Context) = when (Prefs.fontSizeIndex(c)) {
        0 -> c.getString(R.string.font_small); 2 -> c.getString(R.string.font_large)
        3 -> c.getString(R.string.font_xlarge); else -> c.getString(R.string.font_standard)
    }

    // ---- 通用 ----

    private fun generalGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }

        // 语言（单选）
        val langRow = ui.listRow(
            c,
            optionText(c, c.getString(R.string.setting_language), languageLabel(c)),
            trailing = chevron(c)
        ) { showLanguageDialog(c) }
        col.addView(langRow)

        // 开机自启
        val autoSwitch = MaterialSwitch(c).apply { isChecked = Prefs.autoStart(c) }
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.autostart_title), c.getString(R.string.autostart_summary)),
            trailing = autoSwitch
        ))
        autoSwitch.setOnCheckedChangeListener { _, checked -> Prefs.setAutoStart(c, checked) }

        // 退出并停止核心
        col.addView(ui.listRow(
            c, optionText(c, c.getString(R.string.exit_all), ""), trailing = null
        ) { confirmExit(c) })

        // 关于 NexPort
        col.addView(ui.listRow(
            c, optionText(c, c.getString(R.string.setting_about), ""), trailing = chevron(c)
        ) { (activity as? MainActivity)?.showTab(3) })

        return groupCard(c, c.getString(R.string.group_general), col)
    }

    private fun showLanguageDialog(c: Context) {
        val options = arrayOf(
            c.getString(R.string.lang_system), c.getString(R.string.lang_zh), c.getString(R.string.lang_en))
        val current = when (Prefs.language(c)) { "zh" -> 1; "en" -> 2; else -> 0 }
        MaterialAlertDialogBuilder(c)
            .setTitle(R.string.setting_language)
            .setSingleChoiceItems(options, current) { d, which ->
                d.dismiss()
                val tag = when (which) { 1 -> "zh"; 2 -> "en"; else -> "system" }
                if (tag != Prefs.language(c)) {
                    Prefs.setLanguage(c, tag)
                    // 跟随系统 = 空 LocaleList；API<33 持久化由 manifest
                    // AppLocalesMetadataHolderService(autoStoreLocales) 承担
                    val locales = when (tag) {
                        "zh" -> LocaleListCompat.forLanguageTags("zh-CN")
                        "en" -> LocaleListCompat.forLanguageTags("en")
                        else -> LocaleListCompat.getEmptyLocaleList()
                    }
                    AppCompatDelegate.setApplicationLocales(locales) // 自动重建 Activity
                }
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    private fun languageLabel(c: Context) = when (Prefs.language(c)) {
        "zh" -> c.getString(R.string.lang_zh)
        "en" -> c.getString(R.string.lang_en)
        else -> c.getString(R.string.lang_system)
    }

    private fun confirmExit(c: Context) {
        MaterialAlertDialogBuilder(c)
            .setTitle(R.string.exit_confirm_title)
            .setMessage(R.string.exit_confirm_text)
            .setPositiveButton(R.string.confirm) { _, _ ->
                CoreController.stopTunnel()
                GatewayService.stop(c)
                CoreController.stop { _, _ -> }
                requireActivity().finishAffinity()
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    // ---- 网络：网关端口 ----

    private fun networkGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(TextView(c).apply {
            text = c.getString(R.string.setting_gateway_port)
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        })
        portStatus = ui.hint(c, "").apply { setPadding(0, c.dp(2), 0, c.dp(6)) }

        // 二段选择：自动·随机 | 固定
        val seg = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        portAutoBtn = segButton(c, c.getString(R.string.port_auto))
        portFixedBtn = segButton(c, c.getString(R.string.port_fixed))
        seg.addView(portAutoBtn, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply { marginEnd = c.dp(8) })
        seg.addView(portFixedBtn, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        col.addView(seg)

        portInput = EditText(c).apply {
            hint = c.getString(R.string.port_hint)
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(Prefs.gatewayPortValue(c).toString())
            visibility = View.GONE
        }
        col.addView(portInput)

        portAutoBtn?.setOnClickListener { savePortMode(c, false) }
        portFixedBtn?.setOnClickListener { savePortMode(c, true) }
        portInput?.setOnFocusChangeListener { _, hasFocus -> if (!hasFocus) commitPort(c) }
        portInput?.setOnEditorActionListener { _, _, _ -> commitPort(c); true }

        // 「重启核心以生效」横条
        restartBar = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            visibility = View.GONE
            setPadding(0, c.dp(8), 0, 0)
            addView(TextView(c).apply {
                text = c.getString(R.string.port_saved_restart)
                textSize = 13f
                setTextColor(Ui.color(this, R.attr.nxStateWarn))
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(ui.textButton(c, c.getString(R.string.action_restart_now)) {
                CoreController.restart { ok, err ->
                    if (isAdded) {
                        toast(c, if (ok) c.getString(R.string.start_ok)
                        else c.getString(R.string.core_action_failed, err ?: ""))
                        restartBar?.visibility = View.GONE
                    }
                }
            })
        }
        col.addView(restartBar)
        restartBar?.tag = false

        refreshPortSection()
        return groupCard(c, c.getString(R.string.group_network), col)
    }

    private fun segButton(c: Context, text: String) =
        com.google.android.material.button.MaterialButton(c).apply {
            this.text = text
            isAllCaps = false
            minHeight = c.dp(44)
            strokeWidth = c.dp(1)
            strokeColor = android.content.res.ColorStateList.valueOf(Ui.color(this, R.attr.nxSeparator))
            setBackgroundColor(android.graphics.Color.TRANSPARENT)
            setTextColor(Ui.color(this, R.attr.nxTextSecondary))
        }

    private fun refreshPortSection() {
        if (!isAdded) return
        val c = requireContext()
        val fixed = Prefs.gatewayPortFixed(c)
        portAutoBtn?.strokeWidth = if (!fixed) c.dp(2) else c.dp(1)
        portFixedBtn?.strokeWidth = if (fixed) c.dp(2) else c.dp(1)
        portAutoBtn?.setTextColor(Ui.color(portAutoBtn!!, if (!fixed) R.attr.nxAccent else R.attr.nxTextSecondary))
        portFixedBtn?.setTextColor(Ui.color(portFixedBtn!!, if (fixed) R.attr.nxAccent else R.attr.nxTextSecondary))
        portInput?.visibility = if (fixed) View.VISIBLE else View.GONE
        portStatus?.text = buildString {
            if (CoreController.gatewayPort > 0) {
                append(c.getString(R.string.core_gateway_addr) + ": 127.0.0.1:" + CoreController.gatewayPort)
            }
        }
    }

    private fun savePortMode(c: Context, fixed: Boolean) {
        if (fixed) {
            Prefs.setGatewayPortFixed(c, true)
            refreshPortSection()
            portInput?.requestFocus()
        } else {
            Prefs.setGatewayPortFixed(c, false)
            refreshPortSection()
            markRestartNeeded()
        }
    }

    private fun commitPort(c: Context): Boolean {
        val v = portInput?.text?.toString()?.trim()?.toIntOrNull()
        if (v == null || v < 1024 || v > 65535) {
            toast(c, c.getString(R.string.port_invalid))
            return true
        }
        Thread {
            val free = try {
                ServerSocket().use { s -> s.bind(java.net.InetSocketAddress("127.0.0.1", v)); true }
            } catch (_: Exception) { false }
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (free) {
                    Prefs.setGatewayPortValue(c, v)
                    toast(c, c.getString(R.string.port_saved_restart))
                    markRestartNeeded()
                } else {
                    toast(c, c.getString(R.string.port_occupied, v))
                }
            }
        }.start()
        return true
    }

    private fun markRestartNeeded() {
        if (CoreController.state == CoreController.State.RUNNING) {
            restartBar?.visibility = View.VISIBLE
        }
    }

    // ---- 面板登录信息（v1.4.0 ⑥ 自首页整体迁入） ----

    private fun panelLoginGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        credUserValue = ui.secondary(c, "")
        credUserCopy = ui.iconButton(c, R.drawable.ic_copy, R.string.cred_copy_user) {
            Prefs.adminUser(requireContext())?.let { copyText(c, it) }
        }
        col.addView(ui.rowAligned(ui.label(c, c.getString(R.string.cred_user_label)), credUserCopy).apply {
            setPadding(0, c.dp(4), 0, 0)
        })
        col.addView(credUserValue)

        credPassEye = ui.iconButton(c, R.drawable.ic_eye, R.string.cred_show_password) { toggleReveal() }
        credPassCopy = ui.iconButton(c, R.drawable.ic_copy, R.string.cred_copy_pass) {
            Prefs.adminPass(requireContext())?.let { copyText(c, it) }
        }
        col.addView(ui.rowAligned(ui.label(c, c.getString(R.string.cred_pass_label)), credPassEye, credPassCopy).apply {
            setPadding(0, c.dp(10), 0, 0)
        })
        credPassValue = ui.secondary(c, "", mono = true)
        col.addView(credPassValue)

        col.addView(ui.hint(c, c.getString(R.string.cred_footer)).apply { setPadding(0, c.dp(10), 0, 0) })
        return groupCard(c, c.getString(R.string.group_panel_login), col)
    }

    private fun toggleReveal() {
        credRevealed = !credRevealed
        refreshCredentials()
    }

    /** 凭据卡刷新（密码默认 U+2022 脱敏 8–12 个按实际长度；缺失→折叠说明行）。
     *  注意：onHiddenChanged 可能在 onCreateView 之前触发（installTabs 的 hide 路径），
     *  须先判 lateinit 视图已初始化，否则首启即 UninitializedPropertyAccessException。 */
    private fun refreshCredentials() {
        if (!isAdded || !::credUserValue.isInitialized) return
        val c = requireContext()
        val user = Prefs.adminUser(c)
        val pass = Prefs.adminPass(c)
        if (user == null || pass == null) {
            credUserValue.text = c.getString(R.string.cred_missing)
            credPassValue.text = ""
            credUserCopy.visibility = View.GONE
            credPassEye.visibility = View.GONE
            credPassCopy.visibility = View.GONE
            return
        }
        credUserCopy.visibility = View.VISIBLE
        credPassEye.visibility = View.VISIBLE
        credPassCopy.visibility = View.VISIBLE
        credUserValue.text = user
        (credPassEye as android.widget.ImageButton).setImageResource(
            if (credRevealed) R.drawable.ic_eye_off else R.drawable.ic_eye)
        (credPassEye as android.widget.ImageButton).contentDescription = c.getString(
            if (credRevealed) R.string.cred_hide_password else R.string.cred_show_password)
        credPassValue.text = if (credRevealed) pass else "\u2022".repeat(pass.length.coerceIn(8, 12))
    }

    // ---- 隧道（v1.4.0 ⑥ 自首页整体迁入：临时隧道卡 + 自动重连） ----

    private fun tunnelGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        // 修复⑥：原先开关独立成行且 text 为空（无文字选项）——改为带标题/副文案的列表行
        tunnelSwitch = MaterialSwitch(c).apply {
            text = ""
            setOnCheckedChangeListener { _, checked ->
                if (switchingTunnel) return@setOnCheckedChangeListener
                if (checked) {
                    MaterialAlertDialogBuilder(c)
                        .setTitle(R.string.tunnel_confirm_title)
                        .setMessage(R.string.tunnel_confirm_text)
                        .setPositiveButton(R.string.confirm) { _, _ -> startTunnel() }
                        .setNegativeButton(R.string.cancel) { _, _ -> refreshTunnelCard() }
                        .setOnCancelListener { refreshTunnelCard() }
                        .show()
                } else {
                    CoreController.stopTunnel()
                    refreshTunnelCard()
                }
            }
        }
        col.addView(ui.listRow(c,
            labeledText(c, c.getString(R.string.tunnel_title), c.getString(R.string.tunnel_switch_sub)),
            trailing = tunnelSwitch
        ))
        tunnelProgress = LinearProgressIndicator(c).apply {
            isIndeterminate = true
            visibility = View.GONE
            trackCornerRadius = c.dp(1)
        }
        col.addView(tunnelProgress)
        tunnelStateText = ui.secondary(c, "")
        col.addView(tunnelStateText)
        tunnelUrlText = ui.body(c, "", mono = true).apply {
            setOnClickListener {
                if (CoreController.tunnelUrl.isNotEmpty()) copyText(c, CoreController.tunnelUrl)
            }
            setOnLongClickListener {
                if (CoreController.tunnelUrl.isNotEmpty()) { copyText(c, CoreController.tunnelUrl); true } else false
            }
        }
        col.addView(tunnelUrlText)
        tunnelWarn = ui.hint(c, "")
        col.addView(tunnelWarn)
        tunnelErrActions = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(ui.textButton(c, c.getString(R.string.tunnel_view_logs)) { openLogs() })
            addView(ui.textButton(c, c.getString(R.string.tunnel_manual_retry)) {
                CoreController.retryTunnelManually { ok, err ->
                    if (!ok && isAdded) toast(c, c.getString(R.string.core_action_failed, err ?: ""))
                    refreshTunnelCard()
                }
            })
        }
        col.addView(tunnelErrActions.apply { setPadding(0, c.dp(4), 0, 0) })

        // 自动重连开关（既有项，随卡片同组）
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.setting_auto_reconnect), c.getString(R.string.auto_reconnect_subtitle)),
            trailing = MaterialSwitch(c).apply {
                isChecked = Prefs.tunnelAutoReconnect(c)
                setOnCheckedChangeListener { _, checked -> Prefs.setTunnelAutoReconnect(c, checked) }
            }
        ).apply { setPadding(0, c.dp(8), 0, 0) })

        refreshTunnelCard()
        return groupCard(c, c.getString(R.string.group_tunnel), col)
    }

    private fun startTunnel() {
        val c = requireContext()
        toast(c, c.getString(R.string.tunnel_starting))
        CoreController.startTunnel { ok, err ->
            if (!ok && isAdded) {
                toast(c, c.getString(R.string.core_action_failed, err ?: c.getString(R.string.tunnel_state_error)))
            }
            refreshTunnelCard()
        }
    }

    private fun openLogs() {
        // 预置 [tunnel] 过滤（tunnelUxSpec 日志入口）
        (activity as? MainActivity)?.openOverlay(LogsFragment.newInstance("tunnel"))
    }

    private fun refreshTunnelCard() {
        if (!isAdded || !::tunnelSwitch.isInitialized) return
        val c = requireContext()
        val st = CoreController.tunnelState

        switchingTunnel = true
        tunnelSwitch.isChecked = st == "active" || st == "starting"
        switchingTunnel = false

        val reconnecting = st == "error" && CoreController.reconnectAttempt > 0 && !CoreController.reconnectGaveUp
        val gaveUp = st == "error" && CoreController.reconnectGaveUp

        when {
            st == "starting" -> {
                if (tunnelStartingSince == 0L) tunnelStartingSince = System.currentTimeMillis()
                tunnelProgress.visibility = View.VISIBLE
                val waited = ((System.currentTimeMillis() - tunnelStartingSince) / 1000).coerceAtMost(90)
                tunnelStateText.text = c.getString(R.string.tunnel_starting) +
                    "\n" + c.getString(R.string.tunnel_waited_fmt, waited)
                tunnelStateText.setTextColor(Ui.color(tunnelStateText, R.attr.nxTextSecondary))
                tunnelUrlText.visibility = View.GONE
                tunnelWarn.text = ""
                tunnelWarn.setOnLongClickListener(null)
                tunnelErrActions.visibility = View.GONE
            }
            st == "active" -> {
                tunnelProgress.visibility = View.GONE
                tunnelStartingSince = 0
                tunnelStateText.text = c.getString(R.string.tunnel_active)
                tunnelStateText.setTextColor(Ui.color(tunnelStateText, R.attr.nxStateOk))
                tunnelUrlText.text = CoreController.tunnelUrl
                tunnelUrlText.visibility = View.VISIBLE
                tunnelWarn.text = c.getString(R.string.tunnel_exposed) + "\n" +
                    c.getString(R.string.tunnel_dns_propagation)
                tunnelWarn.setTextColor(Ui.color(tunnelWarn, R.attr.nxStateWarn))
                tunnelErrActions.visibility = View.GONE
            }
            st == "error" -> {
                tunnelProgress.visibility = View.GONE
                val msg = CoreController.lastTunnelMessage
                    .ifEmpty { c.getString(R.string.tunnel_state_error) }
                tunnelStateText.text = when {
                    reconnecting -> {
                        val remain = ((CoreController.reconnectNextAtMs - System.currentTimeMillis()) / 1000).coerceAtLeast(0)
                        c.getString(R.string.tunnel_reconnecting_fmt,
                            CoreController.reconnectAttempt, CoreController.RECONNECT_MAX_ATTEMPTS, remain)
                    }
                    else -> c.getString(R.string.tunnel_failed_title)
                }
                tunnelStateText.setTextColor(Ui.color(tunnelStateText, R.attr.nxStateErr))
                tunnelUrlText.visibility = View.GONE
                tunnelWarn.text = buildString {
                    append(msg)
                    append("\n")
                    append(tunnelCauseHint(c, msg))
                    if (gaveUp) append("\n" + c.getString(R.string.tunnel_gave_up))
                }
                tunnelWarn.setTextColor(Ui.color(tunnelWarn, R.attr.nxTextSecondary))
                // 长按复制 errMsg
                tunnelWarn.setOnLongClickListener { copyText(c, msg); true }
                tunnelErrActions.visibility = View.VISIBLE
            }
            else -> {
                tunnelProgress.visibility = View.GONE
                tunnelStartingSince = 0
                tunnelStateText.text = c.getString(R.string.tunnel_off)
                tunnelStateText.setTextColor(Ui.color(tunnelStateText, R.attr.nxTextSecondary))
                tunnelUrlText.visibility = View.GONE
                tunnelWarn.text = ""
                tunnelWarn.setOnLongClickListener(null)
                tunnelErrActions.visibility = View.GONE
            }
        }
    }

    /** errMsg → 常见原因映射（tunnelUxSpec：新错误形态）。 */
    private fun tunnelCauseHint(c: Context, msg: String): String = when {
        msg.contains("边缘") || msg.contains("edge", true) || msg.contains("SRV", true) ->
            c.getString(R.string.tunnel_err_edge)
        msg.contains("超时") ->
            c.getString(R.string.tunnel_err_timeout)
        msg.contains("断开") ->
            c.getString(R.string.tunnel_err_disconnect)
        msg.contains("http2", true) || msg.contains("quic", true) ->
            c.getString(R.string.tunnel_err_http2)
        else -> c.getString(R.string.tunnel_err_generic)
    }

    // ---- 保活与权限（v1.4.0 ⑧：原「服务与通知」更名并集中全部保活项） ----

    private fun serviceGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }

        foregroundSwitch = MaterialSwitch(c).apply { isChecked = Prefs.keepForeground(c) }
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.setting_foreground), c.getString(R.string.foreground_subtitle)),
            trailing = foregroundSwitch
        ))
        foregroundSwitch?.setOnCheckedChangeListener { _, checked -> toggleForeground(checked) }

        notifHint = TextView(c).apply {
            visibility = View.GONE
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
        }
        col.addView(notifHint)

        // 通知权限行（⑧：集中保活项；未授权行内直接请求，已授权/永久拒绝 → 系统通知设置页）
        notifRowMain = TextView(c).apply {
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        }
        notifRowSub = TextView(c).apply {
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            setPadding(0, c.dp(1), 0, 0)
        }
        val notifRowCol = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            addView(notifRowMain); addView(notifRowSub)
        }
        col.addView(ui.listRow(
            c,
            notifRowCol,
            trailing = chevron(c)
        ) {
            val granted = Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(
                c, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
            if (!granted && Build.VERSION.SDK_INT >= 33) {
                keepaliveNotifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
            } else {
                runCatching {
                    startActivity(Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, c.packageName))
                }
            }
        })

        // 电池优化入口（后台常驻卡迁移项）
        val batteryText = TextView(c).apply {
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        }
        fun refreshBattery() {
            batteryText.text = if (BatteryOpt.isIgnoring(requireContext()))
                c.getString(R.string.bg_battery_ok) else c.getString(R.string.bg_battery)
        }
        refreshBattery()
        col.addView(ui.listRow(
            c, batteryText, trailing = chevron(c)
        ) { BatteryOpt.request(c); batteryText.postDelayed({ if (isAdded) refreshBattery() }, 800) })

        // 自启动与后台运行（⑦⑧：按厂商能力尽力引导——常见厂商自启动页 → 应用详情页兜底）
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.keepalive_autostart_row), c.getString(R.string.keepalive_autostart_sub)),
            trailing = chevron(c)
        ) { openAutostartSettings(c) })

        // 悬浮窗保活（floatingWindowSpec ②）：开关 + 授权流 + 行内降级 hint
        floatingSwitch = MaterialSwitch(c).apply { isChecked = Prefs.floatingKeepAlive(c) }
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.setting_floating), c.getString(R.string.floating_subtitle)),
            trailing = floatingSwitch
        ))
        floatingSwitch?.setOnCheckedChangeListener { _, checked -> toggleFloating(checked) }
        floatingHint = TextView(c).apply {
            visibility = View.GONE
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            setOnClickListener {
                // 点 hint 再走一次授权流（Android 14+ 受限设置阻断场景的再入口）
                if (isAdded) requestFloatingOverlay()
            }
        }
        col.addView(floatingHint)

        // 重启核心（重设①：自首页移入保活相关区域，带确认）
        col.addView(ui.separator(c))
        col.addView(ui.listRow(c,
            labeledText(c, c.getString(R.string.core_restart), c.getString(R.string.core_restart_row_sub)),
            trailing = chevron(c)
        ) { confirmRestartCore(c) })

        refreshKeepaliveRows()
        return groupCard(c, c.getString(R.string.group_service), col)
    }

    /** 通知权限行状态副文（⑧）：已授权/未授权实时刷新。 */
    private fun refreshKeepaliveRows() {
        if (!isAdded) return
        val granted = Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(
            requireContext(), Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
        notifRowMain?.text = getString(R.string.keepalive_notif_row)
        notifRowSub?.text = getString(
            if (granted) R.string.guide_perm_granted else R.string.keepalive_notif_sub)
        notifRowSub?.setTextColor(Ui.color(notifRowSub!!,
            if (granted) R.attr.nxStateOk else R.attr.nxTextTertiary))
    }

    private var notifRowMain: TextView? = null
    private var notifRowSub: TextView? = null

    /**
     * 自启动/后台运行按厂商尽力引导（⑦）：常见厂商自启动管理组件逐一尝试
     * （MIUI/EMUI/ColorOS/OriginOS/Flyme），全部不可达 → 应用详情页兜底。
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
            val i = Intent().setClassName(pkg, cls)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            if (runCatching { c.startActivity(i) }.isSuccess) return
        }
        runCatching {
            startActivity(Intent(android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                Uri.parse("package:${c.packageName}")))
        }.onFailure { toast(c, c.getString(R.string.floating_denied_hint)) }
    }

    /** 重启核心确认（重设①：设置页入口，避免误触）。 */
    private fun confirmRestartCore(c: Context) {
        MaterialAlertDialogBuilder(c)
            .setTitle(R.string.core_restart)
            .setMessage(R.string.core_restart_confirm)
            .setPositiveButton(R.string.confirm) { _, _ ->
                toast(c, c.getString(R.string.loading))
                CoreController.restart { ok, err ->
                    if (!isAdded) return@restart
                    if (ok) toast(c, c.getString(R.string.start_ok))
                    else toast(c, c.getString(R.string.core_action_failed, err ?: ""))
                }
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    /** 悬浮窗开关（floatingWindowSpec ②）：canDrawOverlays 直启；否则弹说明 + 去授权。 */
    private fun toggleFloating(keep: Boolean) {
        val c = requireContext()
        if (keep) {
            if (canDrawOverlays(c)) {
                Prefs.setFloatingKeepAlive(c, true)
                c.startService(Intent(c, FloatingWindowService::class.java))
                floatingHint?.visibility = View.GONE
            } else {
                // 开关先回弹，授权成功经 onResume 复查自动置位
                floatingSwitch?.isChecked = false
                pendingFloatingEnable = true
                requestFloatingOverlay()
            }
        } else {
            Prefs.setFloatingKeepAlive(c, false)
            c.stopService(Intent(c, FloatingWindowService::class.java))
            pendingFloatingEnable = false
            floatingHint?.visibility = View.GONE
        }
    }

    private fun canDrawOverlays(c: Context): Boolean =
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && Settings.canDrawOverlays(c)

    /** 用途说明弹窗 + 「去授权」跳系统悬浮窗设置页（appop 无系统对话框，已核）。 */
    private fun requestFloatingOverlay() {
        val c = requireContext()
        MaterialAlertDialogBuilder(c)
            .setTitle(R.string.setting_floating)
            .setMessage(R.string.floating_permission_text)
            .setPositiveButton(R.string.floating_go_grant) { _, _ ->
                runCatching {
                    startActivity(
                        Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                            Uri.parse("package:${c.packageName}"))
                    )
                }.onFailure { toast(c, c.getString(R.string.floating_denied_hint)) }
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    /** onResume 复查（floatingWindowSpec ②）：从系统授权页返回后自动置位/降级回弹。 */
    private fun refreshFloatingOnResume() {
        val c = context ?: return
        if (Prefs.floatingKeepAlive(c)) {
            // 已置位但授权被撤销（受限设置场景）：停服 + 开关回弹 + hint
            if (!canDrawOverlays(c)) {
                Prefs.setFloatingKeepAlive(c, false)
                floatingSwitch?.isChecked = false
                c.stopService(Intent(c, FloatingWindowService::class.java))
                showFloatingDeniedHint()
            }
            return
        }
        if (pendingFloatingEnable) {
            if (canDrawOverlays(c)) {
                Prefs.setFloatingKeepAlive(c, true)
                c.startService(Intent(c, FloatingWindowService::class.java))
                floatingSwitch?.isChecked = true
                floatingHint?.visibility = View.GONE
            } else {
                showFloatingDeniedHint()
            }
            pendingFloatingEnable = false
        }
    }

    /** 行内降级 hint（复用 notifHint 模式）：授权失败/被撤销时的说明与再入口。 */
    private fun showFloatingDeniedHint() {
        if (!isAdded) return
        floatingHint?.visibility = View.VISIBLE
        floatingHint?.text = getString(R.string.floating_denied_hint)
    }

    private fun toggleForeground(keep: Boolean) {
        val c = requireContext()
        if (keep) {
            if (Build.VERSION.SDK_INT >= 33 &&
                ContextCompat.checkSelfPermission(c, Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED
            ) {
                pendingEnable = true
                if (shouldShowRequestPermissionRationale(Manifest.permission.POST_NOTIFICATIONS)) {
                    notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
                } else if (Prefs.notifBannerDismissed(c) || askedOnce(c)) {
                    // 永久拒绝：不再弹系统框，直接降级开启（服务运行、通知不可见）
                    Prefs.setKeepForeground(c, true)
                    GatewayService.start(c)
                    showNotifDeniedHint()
                } else {
                    markAskedOnce(c)
                    notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
                }
            } else {
                Prefs.setKeepForeground(c, true)
                GatewayService.start(c)
            }
        } else {
            Prefs.setKeepForeground(c, false)
            GatewayService.stop(c)
            notifHint?.visibility = View.GONE
        }
    }

    private fun showNotifDeniedHint() {
        if (!isAdded) return
        val c = requireContext()
        notifHint?.visibility = View.VISIBLE
        notifHint?.text = ""
        notifHint?.append(c.getString(R.string.notif_denied_hint) + "  ")
        // 「去系统设置」深链本应用通知页
        notifHint?.append(c.getString(R.string.notif_go_settings))
        notifHint?.setOnClickListener {
            runCatching {
                val i = Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                    .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, c.packageName)
                startActivity(i)
            }
        }
        Snackbar.make(requireView(), c.getString(R.string.notif_denied_hint), Snackbar.LENGTH_LONG).show()
    }

    private fun askedOnce(c: Context): Boolean = c.getSharedPreferences(PREF_FILE, Context.MODE_PRIVATE)
        .getBoolean("notif_asked_once", false)

    private fun markAskedOnce(c: Context) {
        c.getSharedPreferences(PREF_FILE, Context.MODE_PRIVATE)
            .edit().putBoolean("notif_asked_once", true).apply()
    }

    // ---- 安全 ----

    private fun securityGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(ui.listRow(
            c, optionText(c, c.getString(R.string.cp_title), ""), trailing = chevron(c)
        ) { (activity as? MainActivity)?.openOverlay(ChangePasswordFragment()) })
        col.addView(ui.listRow(
            c, optionText(c, c.getString(R.string.setting_forget_credentials), ""), trailing = null
        ) {
            MaterialAlertDialogBuilder(c)
                .setTitle(R.string.setting_forget_credentials)
                .setMessage(R.string.forget_credentials_confirm)
                .setPositiveButton(R.string.confirm) { _, _ ->
                    Prefs.clearAdmin(c)
                    Snackbar.make(requireView(), c.getString(R.string.credentials_cleared), Snackbar.LENGTH_SHORT).show()
                }
                .setNegativeButton(R.string.cancel, null)
                .show()
        })
        return groupCard(c, c.getString(R.string.group_security), col)
    }

    // ---- 诊断 ----

    private fun diagnosticsGroup(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(ui.listRow(
            c, optionText(c, c.getString(R.string.setting_logs), ""), trailing = chevron(c)
        ) { (activity as? MainActivity)?.openOverlay(LogsFragment()) })
        col.addView(ui.listRow(
            c,
            labeledText(c, c.getString(R.string.setting_clear_cache), c.getString(R.string.clear_cache_subtitle)),
            trailing = null
        ) { confirmClearCache(c) })
        return groupCard(c, c.getString(R.string.group_diagnostics), col)
    }

    private fun confirmClearCache(c: Context) {
        // 进入即异步统计 cacheDir 大小
        toast(c, c.getString(R.string.cache_computing))
        Thread {
            val size = dirSize(c.cacheDir)
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                MaterialAlertDialogBuilder(c)
                    .setTitle(R.string.setting_clear_cache)
                    .setMessage(c.getString(R.string.clear_cache_confirm, humanSize(size)))
                    .setPositiveButton(R.string.confirm) { _, _ ->
                        Thread {
                            val freed = dirSize(c.cacheDir)
                            c.cacheDir.listFiles()?.forEach { it.deleteRecursively() }
                            if (!isAdded) return@Thread
                            requireActivity().runOnUiThread {
                                Snackbar.make(requireView(),
                                    c.getString(R.string.cache_cleared, humanSize(freed)),
                                    Snackbar.LENGTH_SHORT).show()
                            }
                        }.start()
                    }
                    .setNegativeButton(R.string.cancel, null)
                    .show()
            }
        }.start()
    }

    private fun dirSize(dir: File?): Long {
        if (dir == null || !dir.exists()) return 0
        return dir.walkBottomUp().filter { it.isFile }.sumOf { it.length() }
    }

    private fun humanSize(bytes: Long): String = when {
        bytes >= 1 shl 20 -> "%.1f MB".format(bytes / 1048576.0)
        bytes >= 1 shl 10 -> "%.1f KB".format(bytes / 1024.0)
        else -> "$bytes B"
    }

    // ---- 构建小件 ----

    /** 分组卡：HIG 分组内缩列表（12dp 圆角描边卡 + 节标题）。 */
    private fun groupCard(c: Context, title: String, content: View): View {
        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(ui.groupHeader(c, title))
        wrap.addView(ui.card(c) { addView(content) })
        return wrap
    }

    private fun optionText(c: Context, title: String, value: String): LinearLayout {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        col.addView(TextView(c).apply {
            text = title
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        })
        if (value.isNotEmpty()) col.addView(TextView(c).apply {
            text = value
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            setPadding(0, c.dp(1), 0, 0)
        })
        return col
    }

    private fun labeledText(c: Context, title: String, sub: String): View =
        optionText(c, title, sub)

    private fun chevron(c: Context): View = TextView(c).apply {
        text = "›"
        textSize = 20f
        setTextColor(Ui.color(this, R.attr.nxTextTertiary))
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    private fun radio(c: Context, checked: Boolean): View = TextView(c).apply {
        text = if (checked) "●" else "○"
        textSize = 16f
        setTextColor(Ui.color(this, if (checked) R.attr.nxAccent else R.attr.nxTextTertiary))
        setPadding(0, 0, c.dp(12), 0)
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    companion object {
        private const val PREF_FILE = "nexport_prefs"
    }
}

/**
 * ChangePasswordFragment — 安全·修改管理员密码（二级 overlay 页）。
 * POST /admin/password（端点已核实 core/adminapi/server.go:98 + auth.go changePassword）；
 * 续 token 用存储凭据 AdminApi.login。成功后 Prefs.saveAdmin 更新加密凭据（首页
 * onResume/onHiddenChanged 刷新凭据卡）；失败行内显示服务端 error 原文；凭据解封失败
 * 引导手动登录面板（不静默失败）。
 */
class ChangePasswordFragment : Fragment() {

    private val ui get() = Ui
    private lateinit var statusText: TextView

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val col = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(16), c.dp(8), c.dp(16), c.dp(16))
        }
        col.addView(backHeader(c, c.getString(R.string.cp_title)) { (activity as? MainActivity)?.popOverlayOrFinish() })

        statusText = ui.hint(c, c.getString(R.string.cp_hint)).apply { setPadding(0, c.dp(8), 0, c.dp(8)) }
        col.addView(statusText)

        val old = passwordField(c, c.getString(R.string.cp_old))
        val new1 = passwordField(c, c.getString(R.string.cp_new))
        val new2 = passwordField(c, c.getString(R.string.cp_new2))
        col.addView(old); col.addView(new1); col.addView(new2)

        // 实时四规则（与首启一致，复用 Onboarding 密码规则）
        val rulesView = PasswordRules.buildRulesView(c, new1)
        col.addView(rulesView)

        col.addView(ui.button(c, c.getString(R.string.cp_submit)) { submit(c, old, new1, new2) }
            .apply { (layoutParams as? LinearLayout.LayoutParams)?.topMargin = c.dp(12) })
        statusText.tag = col

        val scroll = android.widget.ScrollView(c)
        scroll.addView(col)
        return scroll
    }

    private fun passwordField(c: Context, hint: String) = EditText(c).apply {
        this.hint = hint
        inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        transformationMethod = android.text.method.PasswordTransformationMethod.getInstance()
    }

    private fun submit(c: Context, old: EditText, new1: EditText, new2: EditText) {
        val o = old.text.toString()
        val n1 = new1.text.toString()
        val n2 = new2.text.toString()
        val unmet = PasswordRules.firstUnmet(c, n1)
        when {
            o.isEmpty() -> { statusText.text = c.getString(R.string.cp_old); return }
            unmet != null -> { statusText.text = unmet; return }
            n1 != n2 -> { statusText.text = c.getString(R.string.pwd_mismatch); return }
        }
        statusText.text = c.getString(R.string.loading)
        Thread {
            val r = AdminApi.changePassword(o, n1)
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                r.fold(onSuccess = {
                    Prefs.saveAdmin(c, Prefs.adminUser(c) ?: "", n1)
                    statusText.setTextColor(Ui.color(statusText, R.attr.nxStateOk))
                    statusText.text = c.getString(R.string.cp_success)
                }, onFailure = { e ->
                    statusText.setTextColor(Ui.color(statusText, R.attr.nxStateErr))
                    statusText.text = c.getString(R.string.cp_failed, e.message ?: "?")
                })
            }
        }.start()
    }
}

/**
 * LogsFragment — 诊断·运行日志（二级 overlay 页）：
 * 等宽自动滚底、复制全部、导出 .txt（SAF）、[tunnel]/[error] 过滤 chips、清屏；
 * 错误行错误色高亮。经 newInstance(初始过滤) 构造（FragmentManager 反射恢复需无参构造）。
 */
class LogsFragment : Fragment() {

    private val ui get() = Ui
    private var filter: String = "all"
    private lateinit var console: TextView
    private lateinit var scroll: androidx.core.widget.NestedScrollView
    private var listenerAttached = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        arguments?.getString(ARG_FILTER)?.let { filter = it }
    }

    private val exportLauncher = registerForActivityResult(
        ActivityResultContracts.CreateDocument("text/plain")
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        val c = requireContext()
        try {
            c.contentResolver.openOutputStream(uri)?.use {
                it.write(filteredLogs().toByteArray(Charsets.UTF_8))
            }
            toast(c, c.getString(R.string.logs_export_saved, "nexport-logs.txt"))
        } catch (t: Throwable) {
            toast(c, c.getString(R.string.logs_export_failed, t.message ?: "?"))
        }
    }

    private val logListener = object : CoreController.Listener {
        override fun onCoreLog(line: String) { if (isAdded) refresh() }
    }

    override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        val col = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
        }
        col.addView(backHeader(c, c.getString(R.string.logs_title)) { (activity as? MainActivity)?.popOverlayOrFinish() })

        // 实时指示行（logsSpec ④）：绿点 + 「实时监听中」（logListener 挂载于 onViewCreated）
        col.addView(LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(c.dp(16), c.dp(2), c.dp(16), c.dp(2))
            addView(View(c).also { dot ->
                dot.layoutParams = LinearLayout.LayoutParams(c.dp(8), c.dp(8)).apply { marginEnd = c.dp(6) }
                val dotBg = android.graphics.drawable.GradientDrawable().apply {
                    shape = android.graphics.drawable.GradientDrawable.OVAL
                    setColor(Ui.color(dot, R.attr.nxStateOk))
                }
                dot.background = dotBg
                dot.importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            })
            addView(TextView(c).apply {
                text = c.getString(R.string.logs_live)
                textSize = 12f
                setTextColor(Ui.color(this, R.attr.nxStateOk))
            })
        })

        // 动作行 + 过滤 chips
        val actions = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        actions.addView(ui.textButton(c, c.getString(R.string.logs_copy_all)) {
            copyText(c, filteredLogs())
        })
        actions.addView(ui.textButton(c, c.getString(R.string.logs_export)) {
            exportLauncher.launch("nexport-logs.txt")
        })
        actions.addView(ui.textButton(c, c.getString(R.string.logs_clear)) {
            CoreController.clearLogs(); refresh()
        })
        col.addView(actions)

        val chips = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        listOf("all" to R.string.logs_filter_all, "tunnel" to R.string.logs_filter_tunnel, "error" to R.string.logs_filter_error)
            .forEach { (key, res) ->
                val chip = com.google.android.material.button.MaterialButton(c, null,
                    com.google.android.material.R.attr.materialButtonOutlinedStyle).apply {
                    this.text = c.getString(res)
                    isAllCaps = false
                    textSize = 12f
                    minHeight = c.dp(36)
                    isCheckable = true
                    isChecked = key == filter
                }
                chip.setOnClickListener {
                    filter = key
                    chips.children().forEach { v -> (v as? com.google.android.material.button.MaterialButton)?.isChecked = v.tag == key }
                    refresh()
                }
                chip.tag = key
                chips.addView(chip, LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = c.dp(8) })
            }
        col.addView(chips.apply { setPadding(c.dp(16), 0, c.dp(16), c.dp(4)) })

        console = TextView(c).apply {
            textSize = 13f
            setTypeface(android.graphics.Typeface.MONOSPACE)
            setTextIsSelectable(true)
            setPadding(c.dp(16), c.dp(8), c.dp(16), c.dp(24))
        }
        scroll = androidx.core.widget.NestedScrollView(c).apply { addView(console) }
        col.addView(scroll, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        refresh()
        return col
    }

    private fun android.view.ViewGroup.children(): Sequence<View> =
        (0 until childCount).asSequence().map { getChildAt(it) }

    private fun filteredLogs(): String {
        val all = CoreController.snapshotLogs()
        if (filter == "all") return all
        val needle = "[$filter]"
        return all.split('\n').filter { it.contains(needle, ignoreCase = true) }.joinToString("\n")
    }

    private fun refresh() {
        if (!isAdded) return
        // 自动滚动仅当已在底部（logsSpec ④：现恒 fullScroll 会拽回用户）
        val atBottom = !scroll.canScrollVertically(1)
        val logs = filteredLogs()
        if (logs.isBlank()) {
            console.text = getString(R.string.logs_empty)
            console.setTextColor(Ui.color(console, R.attr.nxTextTertiary))
            return
        }
        // 错误行高亮（错误色 token），其余正文色
        val span = android.text.SpannableStringBuilder()
        val errColor = Ui.color(console, R.attr.nxStateErr)
        val okColor = Ui.color(console, R.attr.nxTextPrimary)
        logs.split('\n').forEachIndexed { i, line ->
            if (i > 0) span.append("\n")
            val start = span.length
            span.append(line)
            val isErr = line.contains("[error]", true) || line.contains("错误", true) ||
                line.contains("failed", true) || line.contains("ERR", true)
            span.setSpan(
                android.text.style.ForegroundColorSpan(if (isErr) errColor else okColor),
                start, span.length, android.text.Spannable.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
        console.text = span
        if (atBottom) scroll.post { scroll.fullScroll(View.FOCUS_DOWN) }
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        CoreController.addListener(logListener)
        listenerAttached = true
    }

    override fun onDestroyView() {
        if (listenerAttached) { CoreController.removeListener(logListener); listenerAttached = false }
        super.onDestroyView()
    }

    companion object {
        private const val ARG_FILTER = "filter"
        /** @param initialFilter 预置过滤：all / tunnel / error（tunnelUxSpec 日志入口）。 */
        fun newInstance(initialFilter: String = "all"): LogsFragment =
            LogsFragment().apply { arguments = Bundle().apply { putString(ARG_FILTER, initialFilter) } }
    }
}

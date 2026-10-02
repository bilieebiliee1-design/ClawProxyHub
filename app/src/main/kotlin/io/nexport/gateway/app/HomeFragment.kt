package io.nexport.gateway.app

import io.nexport.gateway.R

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Spannable
import android.text.SpannableStringBuilder
import android.text.style.ForegroundColorSpan
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.fragment.app.Fragment
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.progressindicator.LinearProgressIndicator
import org.json.JSONArray
import org.json.JSONObject

/**
 * HomeFragment — 首页页签（v1.4.0 改版）：
 *  ① 大标题改应用名 NexPort（tab_home 仍用于底部导航）；
 *  ② 网关地址卡增强：端口变更显著横幅（旧→新）、本地端点 http://127.0.0.1:<port> 与
 *     局域网端点 http://<局域网IP>:<port>（核心 LanEndpoint，真实网卡地址，点击复制）、
 *     兼容端点说明（/v1/chat/completions OpenAI 兼容、/v1/messages Anthropic 兼容、
 *     /v1/responses 等，说明后缀拼法）、API 密钥行（默认脱敏 key_mask、眼睛显隐走
 *     /admin/keys/{id}/reveal、一键复制，与面板「API 密钥」实时同步；面板无密钥时
 *     提示配置账号后自动生成）；
 *  ⑤ 开箱即用卡：账号配置完成（/admin/accounts 非空）后显示，端点+密钥+（隧道开启时）
 *     公网端点+模型原样映射说明，分组/路由/密钥已自动配置无需手动操作；
 *  ③ 供应商板块（快捷操作上方）：全部已安装插件（/admin/plugins）图标（无图标用生成
 *     字母头像）+ 名称，与面板插件列表实时同步（卸载/新增自动增减）；点击 → 面板深链
 *     /accounts?add=1&plugin=<name>（面板 handleDeepLink 约定）直达添加账号并预选插件；
 *  ④ 快捷操作六项改四项：一键赞赏（赞赏码大图页）、一键签到（POST /admin/tasks/checkin
 *     NDJSON 进度弹窗逐任务播报）、一键加群（关于页同款回退链）、一键测活（POST
 *     /admin/probe/run NDJSON 进度弹窗：连接/首字/出字速度与重试过程）；
 *  ⑥ 「面板登录信息」「临时隧道」两板块整体移至设置页。
 *
 * 数据节奏：核心状态变化立即刷新；可见期 4s 轻量轮询（本地回环小 JSON）实现面板侧
 * 增删插件/账号/密钥后的近实时同步。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
class HomeFragment : Fragment() {

    private val ui get() = Ui
    private val main = Handler(Looper.getMainLooper())
    private var listenerAttached = false

    // ---- 状态卡 ----
    private lateinit var stateDot: View
    private lateinit var stateText: TextView
    private lateinit var notRunningRow: LinearLayout
    private lateinit var btnCoreToggle: androidx.appcompat.widget.AppCompatButton
    private lateinit var btnPanel: androidx.appcompat.widget.AppCompatButton
    private lateinit var panelDesc: TextView
    private lateinit var portChangeBanner: LinearLayout
    private lateinit var portChangeText: TextView
    // 端点行（重设①：常驻显示 + 独立复制按钮，值带 /v1 后缀）
    private lateinit var localEndpointValue: TextView
    private lateinit var localEndpointCopy: View
    private lateinit var lanEndpointValue: TextView
    private lateinit var lanEndpointCopy: View
    private lateinit var tunnelEndpointRow: LinearLayout
    private lateinit var tunnelEndpointValue: TextView
    private lateinit var tunnelEndpointCopy: View

    // 「兼容端点与用法」折叠（重设①：默认收起，点击展开）
    private lateinit var compatToggleRow: LinearLayout
    private lateinit var compatToggleLabel: TextView
    private lateinit var compatBox: LinearLayout
    private var compatExpanded = false

    // API 密钥行（②）
    private lateinit var keyValue: TextView
    private lateinit var keyEye: View
    private lateinit var keyCopy: View
    private lateinit var keyHint: TextView

    // ---- 账号概览板块（v1.4.0 追加①，数据源 = 面板「渠道概览」同接口 /admin/stats/quota） ----
    private lateinit var overviewEmpty: TextView
    private lateinit var overviewList: LinearLayout
    private var overviewSignature = ""
    private var overviewRows: List<OverviewRow> = emptyList()
    // 面板「设置 → 网络」局域网监听开关的当前值（null=未知，合规复审 MEDIUM 配套）
    private var panelLanEnabled: Boolean? = null

    // ---- 供应商板块（③） ----
    private lateinit var providersEmpty: TextView
    private lateinit var providersGrid: LinearLayout

    // ---- 通知权限引导卡 ----
    private lateinit var notifBanner: View

    // ---- 轮询数据 ----
    private var plugins: JSONArray? = null
    private var keyId = 0L
    private var keyMask = ""
    private var keyName = ""
    @Volatile private var revealedKey: String? = null
    private var keyRevealed = false
    private var revealFailed = ""
    private val iconCache = HashMap<String, Bitmap>()
    private var gridSignature = ""

    private val notifPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        val c = requireContext()
        if (granted) {
            Prefs.setKeepForeground(c, true)
            GatewayService.start(c)
        } else {
            // 拒绝后不再打扰（notificationPermissionSpec ①），不改 keepForeground
            Prefs.setNotifBannerDismissed(c, true)
        }
        notifBanner.visibility = View.GONE
        updateAll()
    }

    private val listener = object : CoreController.Listener {
        override fun onCoreStateChanged(state: CoreController.State, error: String?) {
            if (!isAdded) return
            if (state == CoreController.State.RUNNING) {
                revealedKey = null; keyRevealed = false; revealFailed = ""
                gridSignature = ""
            }
            updateAll()
            refreshData()
        }
    }

    /** 可见期近实时轮询（③⑤②：面板增删插件/账号/密钥自动同步）。 */
    private val tick = object : Runnable {
        override fun run() {
            if (!isAdded) return
            refreshData()
            main.postDelayed(this, POLL_MS)
        }
    }

    override fun onCreateView(
        inflater: android.view.LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = requireContext()
        return ui.pageWithTitle(c, c.getString(R.string.app_name),
            coreCard(c), providersCard(c), quickActions(c), accountOverviewCard(c)).also { root ->
            notifBanner = notifBannerCard(c)
            val col = root.getChildAt(0) as LinearLayout
            // 状态卡下方（index：0=大标题，1=状态卡 → 引导卡插 index 2）
            col.addView(notifBanner, 2)
        }
    }

    // ==================== 卡片构建 ====================

    /**
     * 状态卡（v1.4.0 ②）：状态行 → 核心未运行提示 → 端口变更横幅 → 本地/局域网端点 →
     * 兼容端点说明 → API 密钥 → 版本/在跑插件 → 启动/重启/打开面板。
     */
    private fun coreCard(c: Context): View = ui.card(c) {
        val row = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        stateDot = ui.stateDot(c)
        stateText = ui.title(c, "")
        row.addView(stateDot)
        row.addView(stateText)
        addView(row)

        // 核心未运行内联提示行（替代纯 toast）
        notRunningRow = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, c.dp(8), 0, 0)
            addView(android.widget.ImageView(c).apply {
                setImageResource(R.drawable.ic_error)
                imageTintList = android.content.res.ColorStateList.valueOf(Ui.color(this, R.attr.nxStateWarn))
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            }, LinearLayout.LayoutParams(c.dp(18), c.dp(18)).apply { marginEnd = c.dp(6) })
            addView(TextView(c).apply {
                text = c.getString(R.string.core_not_running_inline)
                textSize = 13f
                setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(ui.textButton(c, c.getString(R.string.core_start_short)) {
                CoreController.start { ok, err ->
                    if (!ok && isAdded) toast(c, c.getString(R.string.core_action_failed, err ?: ""))
                    updateAll()
                }
            })
        }
        addView(notRunningRow)

        // 端口变更显著横幅（②：旧 → 新 + 核心原因描述，警告色）
        val bannerBg = GradientDrawable().apply { cornerRadius = c.dpF(8) }
        portChangeBanner = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            visibility = View.GONE
            setPadding(c.dp(12), c.dp(10), c.dp(12), c.dp(10))
            background = bannerBg
            addView(TextView(c).apply {
                textSize = 14f
                setTypeface(typeface, Typeface.BOLD)
                setTextColor(Ui.color(this, R.attr.nxStateWarn))
            }.also { portChangeText = it })
        }
        bannerBg.setColor((Ui.color(portChangeBanner, R.attr.nxStateWarn) and 0x00FFFFFF) or 0x22000000)
        (portChangeBanner.layoutParams as? LinearLayout.LayoutParams)?.topMargin = c.dp(8)
        addView(portChangeBanner)

        // 端点行（重设①：常驻显示、各带复制按钮，值一律带 /v1 后缀）
        val localRow = endpointRow(c, c.getString(R.string.endpoint_local_label))
        localEndpointValue = localRow.value
        localEndpointCopy = localRow.copyBtn
        addView(localRow.root)

        val lanRow = endpointRow(c, c.getString(R.string.endpoint_lan_label))
        lanEndpointValue = lanRow.value
        lanEndpointCopy = lanRow.copyBtn
        addView(lanRow.root)

        // 公网端点行（隧道建立时追加第三行）
        val tunnelRow = endpointRow(c, c.getString(R.string.endpoint_tunnel_label))
        tunnelEndpointValue = tunnelRow.value
        tunnelEndpointCopy = tunnelRow.copyBtn
        tunnelEndpointRow = tunnelRow.root
        tunnelEndpointRow.visibility = View.GONE
        addView(tunnelRow.root)

        // API 密钥行（重设①：常驻显示——脱敏/显隐/复制）
        val keyHeader = ui.rowAligned(
            ui.label(c, c.getString(R.string.apikey_label)),
            ui.iconButton(c, R.drawable.ic_eye, R.string.apikey_show, {}).also { keyEye = it },
            ui.iconButton(c, R.drawable.ic_copy, R.string.apikey_copy, {}).also { keyCopy = it }
        ).apply { setPadding(0, c.dp(10), 0, 0) }
        addView(keyHeader)
        keyValue = ui.secondary(c, "", mono = true).apply { setPadding(0, c.dp(2), 0, 0) }
        addView(keyValue)
        keyHint = ui.hint(c, "").apply { setPadding(0, c.dp(2), 0, 0) }
        addView(keyHint)

        // 兼容端点与用法（重设①：默认收起，点击展开/收起）
        compatToggleRow = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            isClickable = true
            isFocusable = true
            val tv = TypedValue()
            c.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
            setBackgroundResource(tv.resourceId)
            setPadding(0, c.dp(12), 0, c.dp(2))
            setOnClickListener { compatExpanded = !compatExpanded; updateAll() }
            compatToggleLabel = TextView(c).apply {
                textSize = 13f
                setTypeface(typeface, Typeface.BOLD)
                setTextColor(Ui.color(this, R.attr.nxAccent))
            }
            addView(compatToggleLabel)
        }
        addView(compatToggleRow)
        compatBox = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL; visibility = View.GONE }
        compatBox.addView(ui.hint(c, c.getString(R.string.endpoint_compat_text)).apply {
            setPadding(0, c.dp(4), 0, 0)
        })
        addView(compatBox)

        // 主按钮（重设①：重启核心已移至 设置 → 保活与权限）
        btnCoreToggle = ui.button(c, "") { toggleCore() }
        addView(btnCoreToggle.apply { setPadding(0, c.dp(12), 0, 0) })
        btnPanel = ui.button(c, c.getString(R.string.open_panel_tab)) { openPanel() }
        addView(btnPanel.apply { (layoutParams as? LinearLayout.LayoutParams)?.topMargin = c.dp(8) })
        panelDesc = ui.hint(c, "")
        addView(panelDesc.apply { setPadding(0, c.dp(4), 0, 0) })

        keyEye.setOnClickListener { toggleKeyReveal() }
        keyCopy.setOnClickListener { copyApiKey() }
    }

    /** 端点行：左侧 标签(12sp) + 等宽值(13sp)，右侧独立复制按钮（48dp 命中区）。 */
    private fun endpointRow(c: Context, label: String): EndpointRow {
        val copy = ui.iconButton(c, R.drawable.ic_copy, R.string.endpoint_copy_desc, {})
        val value = TextView(c).apply {
            textSize = 13f
            setTypeface(Typeface.MONOSPACE)
            setTextColor(Ui.color(this, R.attr.nxTextSecondary))
            setPadding(0, c.dp(2), 0, 0)
        }
        val left = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(c).apply {
                text = label
                textSize = 12f
                setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            })
            addView(value)
        }
        val root = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, c.dp(10), 0, 0)
            addView(left, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(copy)
        }
        return EndpointRow(root, value, copy)
    }

    private class EndpointRow(val root: LinearLayout, val value: TextView, val copyBtn: View)

    /** 供应商板块（③）：置于快捷操作上方；4 列网格，图标或字母头像 + 名称。 */
    private fun providersCard(c: Context): View {
        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(ui.groupHeader(c, c.getString(R.string.providers_section)))
        wrap.addView(ui.hint(c, c.getString(R.string.providers_subtitle)).apply { setPadding(0, 0, 0, c.dp(4)) })
        val card = ui.card(c) {
            providersEmpty = ui.hint(c, c.getString(R.string.providers_empty))
            addView(providersEmpty)
            providersGrid = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
            addView(providersGrid)
        }
        wrap.addView(card)
        return wrap
    }

    /**
     * 快捷操作区（v1.4.0 ④ 六改四）：一键赞赏 / 一键签到 / 一键加群 / 一键测活，
     * 2×2 网格。签到与测活弹 NDJSON 实时进度框（逐任务/逐目标播报过程与结果）。
     */
    private fun quickActions(c: Context): View {
        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(ui.groupHeader(c, c.getString(R.string.faq_section)))
        class Entry(val iconRes: Int, val labelRes: Int, val action: () -> Unit)
        val entries = listOf(
            Entry(R.drawable.ic_qa_donate, R.string.qa_donate) {
                (activity as? MainActivity)?.openOverlay(SupportPageFragment())
            },
            Entry(R.drawable.ic_qa_checkin, R.string.qa_checkin) { runCheckin() },
            Entry(R.drawable.ic_qa_group, R.string.qa_join) { joinQQGroup(this@HomeFragment) },
            Entry(R.drawable.ic_qa_probe, R.string.qa_probe) { runProbe() },
        )
        val card = ui.card(c) {
            for (rowIdx in 0 until 2) {
                val row = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
                for (colIdx in 0 until 2) {
                    val e = entries[rowIdx * 2 + colIdx]
                    row.addView(quickCell(c, e.iconRes, e.labelRes, e.action),
                        LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                }
                addView(row)
            }
        }
        wrap.addView(card)
        return wrap
    }

    /**
     * 单格：24dp 图标（nxAccent tint）+ 13sp 标签 + selectableItemBackground 涟漪。
     * 标签 gravity CENTER_HORIZONTAL（v1.3.0 ③ 同款对齐修复）。
     */
    private fun quickCell(c: Context, iconRes: Int, labelRes: Int, action: () -> Unit): View {
        return LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            isClickable = true
            isFocusable = true
            val tv = TypedValue()
            c.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
            setBackgroundResource(tv.resourceId)
            setPadding(0, c.dp(12), 0, c.dp(12))
            setOnClickListener { action() }
            addView(android.widget.ImageView(c).apply {
                setImageResource(iconRes)
                imageTintList = android.content.res.ColorStateList.valueOf(Ui.color(this, R.attr.nxAccent))
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            }, LinearLayout.LayoutParams(c.dp(24), c.dp(24)))
            addView(TextView(c).apply {
                text = c.getString(labelRes)
                textSize = 13f
                gravity = Gravity.CENTER_HORIZONTAL
                setTextColor(Ui.color(this, R.attr.nxTextPrimary))
                setPadding(0, c.dp(6), 0, 0)
            })
        }
    }

    /**
     * 账号概览板块（v1.4.0 追加①）：置于快捷操作下方。数据取自面板概览页「渠道概览」
     * 同一管理接口 GET /admin/stats/quota（tasks.go dashboardQuota：按 插件·实例 聚合
     * credits_json 快照 + profile.quota 兜底），字段与面板一致——label（品牌 · 实例）、
     * plugin、accounts、quota{credits/total_credits/used_credits}；状态（活跃/停用/过期）
     * 由 /admin/accounts 按插件汇总合并（plugin_id→name 经 /admin/plugins 映射）。
     * 进入首页/4s 轮询自动刷新，与面板保持一致。
     */
    private fun accountOverviewCard(c: Context): View {
        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(ui.groupHeader(c, c.getString(R.string.account_overview_section)))
        wrap.addView(ui.hint(c, c.getString(R.string.account_overview_subtitle)).apply {
            setPadding(0, 0, 0, c.dp(4))
        })
        val card = ui.card(c) {
            overviewEmpty = ui.hint(c, c.getString(R.string.account_overview_empty))
            addView(overviewEmpty)
            overviewList = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
            addView(overviewList)
        }
        wrap.addView(card)
        return wrap
    }

    /** 渠道概览行（面板「渠道概览」同字段）。 */
    private data class OverviewRow(
        val label: String,
        val plugin: String,
        val accounts: Int,
        val active: Int,
        val disabled: Int,
        val expired: Int,
        val remaining: String,
        val used: String,
        val total: String,
    )

    /**
     * 积分数值格式化（与面板 fmtThousands 同形态：千分位分组，小数至多 2 位）。
     * 渠道未采集积分（quota 缺字段）时 optDouble 返回 NaN → 面板同款显示「-」。
     */
    private fun fmtQuota(v: Double): String {
        if (v.isNaN()) return "-"
        return java.text.DecimalFormat("#,##0.##", java.text.DecimalFormatSymbols(java.util.Locale.US)).format(v)
    }

    /** 由 quota（渠道概览接口）+ accounts（状态）+ plugins（id→name 映射）构建行。 */
    private fun buildOverviewRows(quota: JSONArray?, accts: JSONArray?, pluginsArr: JSONArray?): List<OverviewRow> {
        if (quota == null || quota.length() == 0) return emptyList()
        // plugin_id → 插件名（accounts 行只有 plugin_id）
        val nameByID = HashMap<Long, String>()
        if (pluginsArr != null) {
            for (i in 0 until pluginsArr.length()) {
                val o = pluginsArr.optJSONObject(i) ?: continue
                nameByID[o.optLong("id")] = o.optString("name")
            }
        }
        // 按插件名汇总账号状态
        val activeBy = HashMap<String, Int>(); val disabledBy = HashMap<String, Int>(); val expiredBy = HashMap<String, Int>()
        if (accts != null) {
            for (i in 0 until accts.length()) {
                val o = accts.optJSONObject(i) ?: continue
                val pn = nameByID[o.optLong("plugin_id")] ?: continue
                when (o.optString("status")) {
                    "active" -> activeBy[pn] = (activeBy[pn] ?: 0) + 1
                    "expired" -> expiredBy[pn] = (expiredBy[pn] ?: 0) + 1
                    else -> disabledBy[pn] = (disabledBy[pn] ?: 0) + 1
                }
            }
        }
        val rows = mutableListOf<OverviewRow>()
        for (i in 0 until quota.length()) {
            val o = quota.optJSONObject(i) ?: continue
            val plugin = o.optString("plugin")
            val q = o.optJSONObject("quota") ?: JSONObject()
            rows += OverviewRow(
                label = o.optString("label").ifEmpty { plugin },
                plugin = plugin,
                accounts = o.optInt("accounts"),
                active = activeBy[plugin] ?: 0,
                disabled = disabledBy[plugin] ?: 0,
                expired = expiredBy[plugin] ?: 0,
                remaining = fmtQuota(q.optDouble("credits")),
                used = fmtQuota(q.optDouble("used_credits")),
                total = fmtQuota(q.optDouble("total_credits")),
            )
        }
        return rows
    }

    /** 账号概览渲染（签名去重：数据未变化不重建）。 */
    private fun renderOverview() {
        if (!isAdded || !::overviewList.isInitialized) return
        val c = requireContext()
        if (overviewRows.isEmpty()) {
            overviewSignature = ""
            overviewList.removeAllViews()
            overviewEmpty.visibility = View.VISIBLE
            return
        }
        overviewEmpty.visibility = View.GONE
        val sig = overviewRows.joinToString("|") { it.label + "#" + it.accounts + "#" + it.remaining + "#" + it.used + "#" + it.total }
        if (sig == overviewSignature) return
        overviewSignature = sig
        overviewList.removeAllViews()
        overviewRows.forEachIndexed { idx, r ->
            if (idx > 0) {
                overviewList.addView(View(c).apply {
                    layoutParams = LinearLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT, c.dp(1).coerceAtLeast(1))
                    setBackgroundColor(Ui.color(this, R.attr.nxSeparator))
                }.apply { setPadding(0, c.dp(6), 0, c.dp(6)) })
            }
            val row = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
            row.addView(ui.body(c, r.label))
            row.addView(ui.hint(c, c.getString(R.string.account_overview_status_fmt,
                r.accounts, r.active, r.disabled, r.expired)).apply { setPadding(0, c.dp(2), 0, 0) })
            row.addView(ui.secondary(c, c.getString(R.string.account_overview_credits_fmt,
                r.remaining, r.used, r.total), mono = true).apply { setPadding(0, c.dp(3), 0, 0) })
            overviewList.addView(row)
        }
    }

    /** 通知权限一次性内联引导卡（非系统弹窗；点「开启」才 launch 权限请求）。 */
    private fun notifBannerCard(c: Context): View = ui.card(c) {
        val row = LinearLayout(c).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        row.addView(android.widget.ImageView(c).apply {
            setImageResource(R.drawable.ic_notifications)
            imageTintList = android.content.res.ColorStateList.valueOf(Ui.color(this, R.attr.nxAccent))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(c.dp(24), c.dp(24)).apply { marginEnd = c.dp(10) })
        row.addView(TextView(c).apply {
            text = c.getString(R.string.notif_banner_title)
            textSize = 15f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        addView(row)
        val actions = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        actions.addView(ui.button(c, c.getString(R.string.notif_banner_enable)) {
            if (Build.VERSION.SDK_INT >= 33) {
                notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        })
        actions.addView(ui.textButton(c, c.getString(R.string.notif_banner_later)) {
            Prefs.setNotifBannerDismissed(c, true)
            notifBanner.visibility = View.GONE
        })
        addView(actions.apply { setPadding(0, c.dp(8), 0, 0) })
    }

    // ==================== 行为 ====================

    private fun toggleCore() {
        val c = requireContext()
        when (CoreController.state) {
            CoreController.State.RUNNING -> CoreController.stop { _, err ->
                if (err != null) toast(c, c.getString(R.string.core_action_failed, err))
                updateAll()
            }
            CoreController.State.NOT_STARTED, CoreController.State.FAILED -> CoreController.start { ok, err ->
                if (!ok) toast(c, c.getString(R.string.core_action_failed, err ?: ""))
                updateAll()
            }
            else -> {}
        }
    }

    private fun openPanel() {
        val act = activity as? MainActivity ?: return
        if (CoreController.state != CoreController.State.RUNNING) {
            toast(requireContext(), getString(R.string.panel_need_core))
            return
        }
        act.showTab(1)
    }

    /** 快捷入口：面板深链（供应商点击 → /accounts?add=1&plugin=<name>，面板约定）。 */
    private fun openRoute(path: String) {
        if (CoreController.state != CoreController.State.RUNNING) {
            toast(requireContext(), getString(R.string.panel_need_core))
            return
        }
        val frag = activity?.supportFragmentManager?.findFragmentByTag("tab_1") as? PanelFragment
        if (frag != null) {
            frag.openRoute(path)
        } else {
            (activity as? MainActivity)?.showTab(1)
        }
    }

    // ---- API 密钥显隐/复制（②⑤共用同一把密钥与 reveal 缓存） ----

    private fun toggleKeyReveal() {
        keyRevealed = !keyRevealed
        refreshKeyViews()
        if (keyRevealed && revealedKey == null && keyId > 0) fetchReveal { refreshKeyViews() }
    }

    private fun copyApiKey() {
        val c = context ?: return
        val cached = revealedKey
        if (keyRevealed && cached != null) {
            copyText(c, cached)
            return
        }
        if (keyId <= 0) return
        toast(c, c.getString(R.string.apikey_loading))
        fetchReveal {
            val k = revealedKey
            if (k != null) copyText(c, k)
            else refreshKeyViews()
        }
    }

    /** reveal 请求（工作线程）：409（存量哈希）/网络失败 → keyHint 明示，不静默。 */
    private fun fetchReveal(onDone: () -> Unit) {
        val c = context ?: return
        Thread {
            val r = AdminApi.revealKey(keyId)
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (!isAdded) return@runOnUiThread
                r.fold(
                    onSuccess = { k ->
                        revealedKey = if (k.isBlank()) null else k
                        if (k.isBlank()) revealFailed = c.getString(R.string.apikey_reveal_failed, "empty")
                        else revealFailed = ""
                    },
                    onFailure = { e ->
                        revealedKey = null
                        revealFailed = c.getString(R.string.apikey_reveal_failed, e.message ?: "?")
                    }
                )
                onDone()
            }
        }.start()
    }

    private fun refreshKeyViews() {
        // onHiddenChanged 可能在 onCreateView 之前触发（installTabs 的 hide 路径），须防 lateinit
        if (!isAdded || !::keyValue.isInitialized) return
        val c = requireContext()
        val eyeRes = if (keyRevealed) R.drawable.ic_eye_off else R.drawable.ic_eye
        val eyeDesc = if (keyRevealed) R.string.apikey_hide else R.string.apikey_show
        (keyEye as? android.widget.ImageButton)?.setImageResource(eyeRes)
        (keyEye as? android.widget.ImageButton)?.contentDescription = c.getString(eyeDesc)

        val hasKey = keyId > 0
        keyValue.text = when {
            !hasKey -> ""
            keyRevealed -> revealedKey ?: c.getString(R.string.apikey_loading)
            else -> keyMask.ifEmpty { "cph-••••" }
        }
        keyEye.visibility = if (hasKey) View.VISIBLE else View.GONE
        keyCopy.visibility = if (hasKey) View.VISIBLE else View.GONE

        val hint = when {
            !hasKey -> c.getString(R.string.apikey_none)
            revealFailed.isNotEmpty() -> revealFailed
            keyRevealed -> c.getString(R.string.apikey_sync_hint)
            else -> c.getString(R.string.apikey_sync_hint)
        }
        keyHint.text = hint
        keyHint.setTextColor(Ui.color(keyHint, if (revealFailed.isNotEmpty()) R.attr.nxStateErr else R.attr.nxTextTertiary))
    }

    // ---- 数据轮询（插件 / 账号数 / 密钥） ----

    private fun refreshData() {
        if (CoreController.state != CoreController.State.RUNNING) {
            plugins = null; keyId = 0; keyMask = ""; revealedKey = null; panelLanEnabled = null
            if (isAdded) {
                refreshKeyViews()
                renderProviders()
                overviewRows = emptyList()
                renderOverview()
            }
            return
        }
        Thread {
            val arr = AdminApi.pluginArray()
            val accts = AdminApi.accountArray()
            val keys = AdminApi.keyArray()
            val quota = AdminApi.quotaArray()
            val lanEnabled = AdminApi.systemLanEnabled()
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (!isAdded) return@runOnUiThread
                if (arr != null) plugins = arr
                overviewRows = buildOverviewRows(quota, accts, arr)
                panelLanEnabled = lanEnabled
                val k = keys?.optJSONObject(0)
                if (k != null) {
                    val newId = k.optLong("id")
                    if (newId != keyId) { revealedKey = null; keyRevealed = false; revealFailed = "" }
                    keyId = newId
                    keyMask = k.optString("key_mask")
                    keyName = k.optString("name")
                } else {
                    keyId = 0; keyMask = ""; keyName = ""; revealedKey = null
                }
                refreshKeyViews()
                renderProviders()
                updateAll()
            }
        }.start()
    }

    /** 供应商网格重绘（签名去重：列表未变化不重建，避免闪烁与图标重取）。 */
    private fun renderProviders() {
        if (!isAdded || !::providersGrid.isInitialized) return
        val c = requireContext()
        val arr = plugins
        if (arr == null || arr.length() == 0) {
            gridSignature = ""
            providersGrid.removeAllViews()
            providersEmpty.visibility = View.VISIBLE
            return
        }
        providersEmpty.visibility = View.GONE
        val sig = buildString {
            for (i in 0 until arr.length()) {
                val o = arr.optJSONObject(i) ?: continue
                append(o.optString("name")).append(',')
            }
        }
        if (sig == gridSignature) return
        gridSignature = sig
        providersGrid.removeAllViews()

        var row: LinearLayout? = null
        var col = 0
        for (i in 0 until arr.length()) {
            val o = arr.optJSONObject(i) ?: continue
            val name = o.optString("name")
            if (name.isEmpty()) continue
            val label = o.optString("label").ifEmpty { name }
            val iconUrl = o.optString("icon")
            if (col % 4 == 0) {
                row = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
                providersGrid.addView(row)
            }
            row!!.addView(providerCell(c, name, label, iconUrl),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            col++
        }
        // 末行补位（占位空格保持格子等宽）
        while (col % 4 != 0) {
            row!!.addView(View(c),
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            col++
        }
    }

    private fun providerCell(c: Context, name: String, label: String, iconUrl: String): View {
        val cell = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER
            isClickable = true
            isFocusable = true
            val tv = TypedValue()
            c.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
            setBackgroundResource(tv.resourceId)
            setPadding(c.dp(4), c.dp(12), c.dp(4), c.dp(12))
            setOnClickListener { openRoute("/accounts?add=1&plugin=$name") }
            contentDescription = c.getString(R.string.provider_add_account_fmt, label)
        }
        val cached = iconCache[name]
        val iconView: View = if (cached != null) bitmapIcon(c, cached) else letterAvatar(c, label, name)
        cell.addView(iconView, LinearLayout.LayoutParams(c.dp(40), c.dp(40)))
        cell.addView(TextView(c).apply {
            text = label
            textSize = 11f
            gravity = Gravity.CENTER_HORIZONTAL
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
            setPadding(0, c.dp(6), 0, 0)
        })
        if (cached == null && iconUrl.isNotEmpty()) loadIcon(name, iconUrl)
        return cell
    }

    /** 生成字母头像（③：无图标插件兜底）——品牌首字母 + 名称哈希定色的圆形底。 */
    private fun letterAvatar(c: Context, label: String, name: String): View {
        val palette = intArrayOf(0xFF2563EB.toInt(), 0xFF0D9488.toInt(), 0xFFD97706.toInt(),
            0xFFDC2626.toInt(), 0xFF7C3AED.toInt(), 0xFF0284C7.toInt(),
            0xFF059669.toInt(), 0xFFDB2777.toInt())
        val bg = GradientDrawable().apply {
            shape = GradientDrawable.OVAL
            setColor(palette[(((name.ifEmpty { label }).hashCode() % palette.size) + palette.size) % palette.size])
        }
        return TextView(c).apply {
            text = (label.firstOrNull() ?: '?').uppercaseChar().toString()
            textSize = 16f
            setTypeface(typeface, Typeface.BOLD)
            setTextColor(0xFFFFFFFF.toInt())
            gravity = Gravity.CENTER
            background = bg
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }
    }

    private fun bitmapIcon(c: Context, bmp: Bitmap): View {
        val bg = GradientDrawable().apply { shape = GradientDrawable.OVAL }
        return android.widget.ImageView(c).apply {
            setImageBitmap(bmp)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            bg.setColor(Ui.color(this, R.attr.nxCardSurface))
            background = bg
            clipToOutline = true
        }
    }

    /** 图标异步加载（icon 端点免鉴权：/assets/plugins/<name>/icon）。 */
    private fun loadIcon(name: String, iconUrl: String) {
        Thread {
            val bmp = try {
                val url = "http://127.0.0.1:${CoreController.gatewayPort}$iconUrl"
                java.net.URL(url).openStream().use { BitmapFactory.decodeStream(it) }
            } catch (_: Exception) { null }
            if (bmp == null) return@Thread
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (!isAdded) return@runOnUiThread
                iconCache[name] = bmp
                if (gridSignature.isNotEmpty()) { gridSignature = ""; renderProviders() }
            }
        }.start()
    }

    // ---- 一键签到（④）：NDJSON 实时进度弹窗 ----

    private fun runCheckin() {
        val c = requireContext()
        if (CoreController.state != CoreController.State.RUNNING) {
            toast(c, c.getString(R.string.panel_need_core))
            return
        }
        val d = BatchProgressDialog(c, c.getString(R.string.checkin_title))
        d.show()
        d.log(c.getString(R.string.checkin_start), Ui.color(d.logView, R.attr.nxTextSecondary))
        Thread {
            var total = -1
            var doneCount = 0
            var okCount = 0
            var failCount = 0
            var skipCount = 0
            // 验收③：插件对「仅 API 密钥/站点签到」类账号返回 success=true 但摘要如实
            // 说明无法自动签到——计数上区分「跳过」（ neutral ▷ 行），不再混入「成功」。
            // 判定用官方插件共享措辞（newapi account.go checkinManualOnly/checkinBySite），
            // 未命中时按原样计成功（优雅降级=旧行为）。
            fun isSkipped(summary: String): Boolean =
                summary.contains("无法自动签到") || summary.contains("需人工")
            val result = AdminApi.streamPost("/admin/tasks/checkin", null) { ev ->
                if (!isAdded) return@streamPost
                requireActivity().runOnUiThread {
                    if (!isAdded || !d.isShowing) return@runOnUiThread
                    when {
                        ev.has("done") && ev.optBoolean("done") -> {
                            okCount = ev.optInt("success")
                            failCount = ev.optInt("failed")
                            // 核心 done 事件现带结构化 skipped 计数（v1.4.8+ 核心向后兼容：旧核心为 0，
                            // 逐条启发式 isSkipped 的累计值保留）
                            if (ev.has("skipped")) skipCount = ev.optInt("skipped")
                            if (skipCount > 0) {
                                d.summary(c.getString(R.string.checkin_done_skip_fmt, okCount, failCount, skipCount))
                            } else {
                                d.summary(c.getString(R.string.checkin_done_fmt, okCount, failCount))
                            }
                            d.finishBatch()
                        }
                        ev.has("error") && !ev.optBoolean("done") -> {
                            d.log(c.getString(R.string.checkin_busy) +
                                "（${ev.optString("error")}）", Ui.color(d.logView, R.attr.nxStateErr))
                            d.finishBatch()
                        }
                        ev.has("total") && total < 0 -> {
                            total = ev.optInt("total")
                            if (total == 0) {
                                d.log(c.getString(R.string.checkin_no_tasks),
                                    Ui.color(d.logView, R.attr.nxStateWarn))
                                d.finishBatch()
                            } else {
                                d.summary(c.getString(R.string.checkin_running_fmt, 0, total))
                            }
                        }
                        ev.optBoolean("running") -> {
                            d.log("▶ ${ev.optString("plugin")} · ${ev.optString("capability")} · ${ev.optString("account")}",
                                Ui.color(d.logView, R.attr.nxTextPrimary))
                        }
                        ev.has("status") -> {
                            doneCount++
                            if (total > 0) d.summary(c.getString(R.string.checkin_running_fmt, doneCount.coerceAtMost(total), total))
                            val st = ev.optString("status")
                            if (st == "skipped" ||
                                (st == "success" && isSkipped(ev.optString("summary")))) {
                                skipCount++
                                d.log("▷ ${ev.optString("summary")}", Ui.color(d.logView, R.attr.nxStateWarn))
                            } else if (ev.optString("status") == "success") {
                                d.log("✔ ${ev.optString("summary")}", Ui.color(d.logView, R.attr.nxStateOk))
                            } else {
                                d.log("✘ ${ev.optString("summary").ifEmpty { "失败" }}${ev.optString("error").let { if (it.isNotEmpty()) "：$it" else "" }}",
                                    Ui.color(d.logView, R.attr.nxStateErr))
                            }
                        }
                    }
                }
            }
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (isAdded && d.isShowing && !d.finished) {
                    d.summary(c.getString(R.string.core_action_failed,
                        result.exceptionOrNull()?.message ?: "中断"))
                    d.finishBatch()
                }
            }
        }.start()
    }

    // ---- 一键测活（④）：NDJSON 实时进度弹窗 ----

    private fun runProbe() {
        val c = requireContext()
        if (CoreController.state != CoreController.State.RUNNING) {
            toast(c, c.getString(R.string.panel_need_core))
            return
        }
        val d = BatchProgressDialog(c, c.getString(R.string.probe_title))
        d.show()
        d.log(c.getString(R.string.probe_start), Ui.color(d.logView, R.attr.nxTextSecondary))
        Thread {
            var total = -1
            var doneCount = 0
            // ②结构化重排：按 供应商（插件）· 账号 分组（组头只打一次），每模型结果一行。
            // 核心事件已带标识字段（probe.go：target{plugin_label, account, model} +
            // result.attempts + retry.attempt），无需改核心补字段。
            val groupsSeen = HashSet<String>()
            fun groupKey(t: JSONObject): String =
                t.optString("plugin_label").ifEmpty { t.optString("plugin") } + " · " + t.optString("account")
            fun modelOf(t: JSONObject): String = t.optString("model").ifEmpty { "聚合目录" }
            val result = AdminApi.streamPost("/admin/probe/run?concurrency=4", null) { ev ->
                if (!isAdded) return@streamPost
                requireActivity().runOnUiThread {
                    if (!isAdded || !d.isShowing) return@runOnUiThread
                    val t = ev.optJSONObject("target")
                    when {
                        ev.has("done") && ev.optBoolean("done") -> {
                            d.summary(c.getString(R.string.probe_done_fmt,
                                ev.optInt("ok"), ev.optInt("failed")))
                            d.finishBatch()
                        }
                        ev.optString("phase") == "start" && t != null -> {
                            // 组头：供应商 · 账号（首次出现时打一行，并行交错去重）
                            val g = groupKey(t)
                            if (groupsSeen.add(g)) {
                                d.log(c.getString(R.string.probe_group_fmt,
                                    t.optString("plugin_label").ifEmpty { t.optString("plugin") },
                                    t.optString("account")),
                                    Ui.color(d.logView, R.attr.nxAccent))
                            }
                        }
                        ev.optString("phase") == "retry" && t != null -> {
                            d.log(c.getString(R.string.probe_retry_fmt,
                                modelOf(t), ev.optInt("attempt"), ev.optString("error")),
                                Ui.color(d.logView, R.attr.nxStateWarn))
                        }
                        ev.optString("phase") == "result" && t != null -> {
                            doneCount++
                            if (total > 0) d.summary(c.getString(R.string.probe_running_fmt, doneCount.coerceAtMost(total), total))
                            if (ev.optBoolean("ok")) {
                                val speed = String.format(java.util.Locale.US, "%.1f", ev.optDouble("tokens_per_sec"))
                                d.log(c.getString(R.string.probe_line_ok_fmt, modelOf(t),
                                    ev.optLong("connect_ms"), ev.optLong("first_token_ms"), speed),
                                    Ui.color(d.logView, R.attr.nxStateOk))
                            } else {
                                d.log(c.getString(R.string.probe_line_fail_fmt, modelOf(t),
                                    ev.optString("error"), ev.optInt("attempts")),
                                    Ui.color(d.logView, R.attr.nxStateErr))
                            }
                        }
                        ev.has("total") && total < 0 -> {
                            total = ev.optInt("total")
                            if (total == 0) {
                                d.log(c.getString(R.string.probe_no_targets),
                                    Ui.color(d.logView, R.attr.nxStateWarn))
                                d.finishBatch()
                            } else {
                                d.summary(c.getString(R.string.probe_running_fmt, 0, total))
                            }
                        }
                        ev.has("error") -> {
                            d.log(ev.optString("error"), Ui.color(d.logView, R.attr.nxStateErr))
                            d.finishBatch()
                        }
                    }
                }
            }
            if (!isAdded) return@Thread
            requireActivity().runOnUiThread {
                if (isAdded && d.isShowing && !d.finished) {
                    d.summary(c.getString(R.string.core_action_failed,
                        result.exceptionOrNull()?.message ?: "中断"))
                    d.finishBatch()
                }
            }
        }.start()
    }

    // ==================== 刷新 ====================

    private fun updateAll() {
        if (!isAdded || !::stateDot.isInitialized || !::compatToggleLabel.isInitialized ||
            !::tunnelEndpointRow.isInitialized) return
        val c = requireContext()

        // 核心卡
        val (stateStr, dotColorAttr) = when (CoreController.state) {
            CoreController.State.RUNNING -> c.getString(R.string.core_state_running) to R.attr.nxStateOk
            CoreController.State.STARTING -> c.getString(R.string.core_state_starting) to R.attr.nxStateWarn
            CoreController.State.FAILED -> c.getString(R.string.core_state_failed) to R.attr.nxStateErr
            else -> c.getString(R.string.core_state_not_started) to R.attr.nxTextTertiary
        }
        stateText.text = stateStr
        (stateDot.background as? GradientDrawable)?.setColor(Ui.color(stateDot, dotColorAttr))
        notRunningRow.visibility =
            if (CoreController.state == CoreController.State.RUNNING) View.GONE else View.VISIBLE

        // 端口变更横幅（②）
        val from = CoreController.portChangedFrom
        val to = CoreController.portChangedTo
        if (from > 0 && to > 0 && from != to) {
            portChangeText.text = c.getString(R.string.port_change_fmt, from, to)
            portChangeBanner.visibility = View.VISIBLE
        } else {
            portChangeBanner.visibility = View.GONE
        }

        // 端点行（重设①：常驻 + 独立复制按钮，值带 /v1 后缀——客户端直连 URL 形态）
        val port = CoreController.gatewayPort
        val local = if (port > 0) "http://127.0.0.1:$port/v1" else "-"
        localEndpointValue.text = local
        localEndpointCopy.setOnClickListener { if (port > 0) copyText(c, local) }
        localEndpointCopy.visibility = if (port > 0) View.VISIBLE else View.GONE
        // 局域网端点（修复③）：核心已监听（Config.LanIP 注入后应非空）→ 原样显示；
        // 核心仍未监听时用宿主 NetworkInterface 枚举的站点内 IPv4 兜底拼
        // http://<局域网IP>:<网关端口>（两者都不可得才显示「未获取」）。
        // 局域网端点（修复③ + 合规复审 MEDIUM）：核心已监听 → 原样显示；面板已关
        // （system/info lan_enabled=false）→ 明示关闭（不再用本机检测兜底拼接端点，
        // 否则开关形同虚设）；面板开启/未知而核心未监听时才用宿主 NetworkInterface
        // 枚举的站点内 IPv4 兜底拼 http://<局域网IP>:<网关端口>。
        // 面板开关为关闭态时，忽略核心启动时缓存的核心端点（监听已热停，缓存值失真）
        val lan = if (panelLanEnabled == false) ""
        else CoreController.lanEndpoint.ifEmpty {
            val ip = if (port > 0) LanDetect.bestLanIPv4() else null
            ip?.let { "http://$it:$port" } ?: ""
        }
        if (lan.isNotEmpty()) {
            lanEndpointValue.text = "$lan/v1"
            lanEndpointCopy.visibility = View.VISIBLE
            lanEndpointCopy.setOnClickListener { copyText(c, "$lan/v1") }
        } else {
            lanEndpointValue.text = c.getString(
                if (panelLanEnabled == false) R.string.endpoint_lan_off_panel else R.string.endpoint_lan_off)
            lanEndpointValue.setTextColor(Ui.color(lanEndpointValue, R.attr.nxTextTertiary))
            lanEndpointCopy.visibility = View.GONE
            lanEndpointCopy.setOnClickListener(null)
        }

        // 公网端点行（重设①：隧道建立时追加 https://<域名>/v1）
        val tunnelOn = CoreController.tunnelState == "active" && CoreController.tunnelUrl.isNotEmpty()
        if (tunnelOn) {
            val pub = CoreController.tunnelUrl.trimEnd('/') + "/v1"
            tunnelEndpointValue.text = pub
            tunnelEndpointRow.visibility = View.VISIBLE
            tunnelEndpointCopy.setOnClickListener { copyText(c, pub) }
        } else {
            tunnelEndpointRow.visibility = View.GONE
        }

        refreshKeyViews()

        // 兼容端点与用法（重设①：默认收起，点击展开/收起）
        compatBox.visibility = if (compatExpanded) View.VISIBLE else View.GONE
        compatToggleLabel.text = c.getString(R.string.compat_toggle) +
            if (compatExpanded) " ▾" else " ▸"

        btnCoreToggle.setText(
            when (CoreController.state) {
                CoreController.State.RUNNING -> R.string.core_stop
                CoreController.State.STARTING -> R.string.loading
                else -> R.string.core_start
            })
        btnCoreToggle.isEnabled = CoreController.state != CoreController.State.STARTING
        btnPanel.isEnabled = CoreController.state == CoreController.State.RUNNING
        panelDesc.text = if (CoreController.state == CoreController.State.RUNNING)
            c.getString(R.string.open_panel_tab_desc)
        else
            c.getString(R.string.open_panel_need_core)

        renderOverview()
    }

    private fun maybeShowNotifBanner() {
        val c = context ?: return
        val granted = ContextCompat.checkSelfPermission(
            c, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
        val show = (Build.VERSION.SDK_INT >= 33) && (!granted) &&
            (!Prefs.notifBannerDismissed(c)) && Prefs.onboardingDone(c) && Prefs.eulaAccepted(c)
        notifBanner.visibility = if (show) View.VISIBLE else View.GONE
    }

    // ---- 生命周期 ----

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        super.onViewCreated(view, savedInstanceState)
        CoreController.addListener(listener)
        listenerAttached = true
        // 状态卡 liveRegion=polite（accessibilitySpec）
        ViewCompat.setAccessibilityLiveRegion(stateText, View.ACCESSIBILITY_LIVE_REGION_POLITE)
    }

    override fun onResume() {
        super.onResume()
        updateAll()
        refreshData()
        maybeShowNotifBanner()
        main.postDelayed(tick, POLL_MS)
    }

    override fun onPause() {
        super.onPause()
        if (listenerAttached) { CoreController.removeListener(listener); listenerAttached = false }
        main.removeCallbacks(tick)
        // 离开自动恢复脱敏（homeSpec 安全约束）
        keyRevealed = false
        if (isAdded) refreshKeyViews()
    }

    /** 页签 show/hide 联动（MainActivity 不触发 onResume，须走 onHiddenChanged）。 */
    override fun onHiddenChanged(hidden: Boolean) {
        super.onHiddenChanged(hidden)
        if (hidden) {
            main.removeCallbacks(tick)
            if (listenerAttached) { CoreController.removeListener(listener); listenerAttached = false }
            keyRevealed = false
            if (isAdded) refreshKeyViews()
        } else {
            CoreController.addListener(listener)
            listenerAttached = true
            updateAll()
            refreshData()
            maybeShowNotifBanner()
            main.postDelayed(tick, POLL_MS)
        }
    }

    private fun appVersion(c: Context): String = runCatching {
        c.packageManager.getPackageInfo(c.packageName, 0).versionName ?: "1.4.0"
    }.getOrDefault("1.4.0")

    companion object {
        /** 面板侧增删插件/账号/密钥的近实时同步节奏（本地回环小 JSON，开销可忽略）。 */
        private const val POLL_MS = 4000L
    }
}

/**
 * BatchProgressDialog — 一键签到/一键测活共用的实时进度弹窗（④）：
 * 摘要行 + 滚动日志区（逐任务/逐目标带色前缀行）+「关闭」；关闭即 dismiss →
 * streamPost 连接随之断开（核心 ctx 取消语义：签到立即停止后续任务）。
 * finishBatch 后仅可关闭（不再追加/改摘要）。
 */
class BatchProgressDialog(c: Context, title: String) {

    private val dialog: android.app.Dialog
    private val summaryView: TextView
    private val progress: LinearProgressIndicator
    val logView: TextView
    private val scrollView: ScrollView
    /** 收尾标记：true 后不再追加/改摘要。 */
    @Volatile var finished = false
        private set
    val isShowing: Boolean get() = dialog.isShowing

    init {
        val col = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(24), c.dp(20), c.dp(24), c.dp(12))
        }
        summaryView = TextView(c).apply {
            textSize = 14f
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
        }
        col.addView(summaryView)
        progress = LinearProgressIndicator(c).apply {
            isIndeterminate = true
            trackCornerRadius = c.dp(1)
            setPadding(0, c.dp(12), 0, c.dp(4))
        }
        col.addView(progress)
        logView = TextView(c).apply {
            textSize = 12f
            setTypeface(Typeface.MONOSPACE)
            setTextColor(Ui.color(this, R.attr.nxTextPrimary))
            setPadding(0, c.dp(8), 0, c.dp(8))
        }
        scrollView = ScrollView(c).apply { addView(logView) }
        col.addView(scrollView, LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, c.dp(280)))
        col.addView(TextView(c).apply {
            text = c.getString(R.string.progress_cancel_hint)
            textSize = 12f
            setTextColor(Ui.color(this, R.attr.nxTextTertiary))
            setPadding(0, c.dp(4), 0, 0)
        })

        dialog = MaterialAlertDialogBuilder(c)
            .setTitle(title)
            .setView(col)
            .setPositiveButton(c.getString(R.string.progress_close)) { _, _ -> }
            .create()
        dialog.setCanceledOnTouchOutside(false)
    }

    fun show() { dialog.show() }

    fun summary(text: String) {
        if (finished) return
        summaryView.text = text
    }

    fun log(line: String, color: Int) {
        if (finished) return
        val span = logView.text as? SpannableStringBuilder ?: SpannableStringBuilder(logView.text)
        if (span.isNotEmpty()) span.append("\n")
        val start = span.length
        span.append(line)
        span.setSpan(ForegroundColorSpan(color), start, span.length, Spannable.SPAN_EXCLUSIVE_EXCLUSIVE)
        logView.text = span
        scrollView.post { scrollView.fullScroll(View.FOCUS_DOWN) }
    }

    /** 收尾：停进度条（批量结束、错误收尾、断流）。 */
    fun finishBatch() {
        finished = true
        progress.visibility = View.GONE
    }
}

/** 版本串缓存（Bridge.version() 需核心已加载；兜底值为核心 version.Core）。 */
object BridgeVersion {
    @Volatile private var cached: String? = null
    fun current(): String {
        if (cached == null) {
            cached = try {
                io.nexport.gateway.bridge.Bridge.version()
            } catch (_: Throwable) {
                "1.3.0"
            }
        }
        return cached ?: "1.3.0"
    }

    /**
     * 工作线程预热（perf 修复轮）：CoreController.start 在 Bridge.start 返回后调用，
     * 把首次 gobind 同步调用挪出主线程——原实现首次 current() 落在 RUNNING 分发后的
     * 主线程回调（关于页 onCreateView/coreListener），恰逢插件 spawn 竞争峰值。
     * 预热失败不缓存兜底值（留给首调 current() 再试一次），成功结果与 current() 共用
     * 同一份进程内缓存（版本串随二进制固定，核心重启无需失效）。
     */
    fun prewarm() {
        if (cached != null) return
        try {
            cached = io.nexport.gateway.bridge.Bridge.version()
        } catch (_: Throwable) {
        }
    }
}

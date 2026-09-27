package io.nexport.gateway.app

import io.nexport.gateway.R

import android.content.Context
import android.graphics.Typeface
import android.os.Bundle
import android.view.Gravity
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.core.content.ContextCompat
import androidx.fragment.app.Fragment

/**
 * 引导公共基座：标题 + 步骤指示 + 内容容器（单 Activity 分步可视化引导，可回退）。
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
abstract class StepFragment : Fragment() {
    abstract fun stepIndex(): Int // 0..3
    abstract fun buildContent(ctx: Context): View

    protected fun ctx(): Context = requireContext()
    private val ui get() = Ui

    final override fun onCreateView(
        inflater: LayoutInflater, container: ViewGroup?, savedInstanceState: Bundle?
    ): View {
        val c = ctx()
        val root = LinearLayout(c).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(c.dp(16), c.dp(16), c.dp(16), c.dp(16))
        }
        root.addView(ui.title(c, titleText()))
        root.addView(ui.hint(c, c.getString(R.string.step_fmt, stepIndex() + 1, 4)).apply {
            setPadding(0, c.dp(2), 0, c.dp(10))
        })
        root.addView(buildContent(c),
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        return root
    }

    open fun titleText(): String = ""

    fun page(vararg views: View): ScrollView = ui.page(ctx(), *views)

    fun headerButtonRow(vararg views: View): LinearLayout = ui.row(*views).apply {
        setPadding(0, ctx().dp(8), 0, 0)
    }
}

/**
 * PasswordRules — 密码四规则（≥10 位/大写/小写/数字），首启引导与「修改管理员密码」
 * 共用（settingsSpec security_change_password：壳侧四规则照旧强制，规则复用 Onboarding）。
 */
object PasswordRules {

    fun rules(c: Context): List<Pair<String, (String) -> Boolean>> = listOf(
        c.getString(R.string.pwd_rule_len) to { p: String -> p.length >= 10 },
        c.getString(R.string.pwd_rule_upper) to { p: String -> p.any { it in 'A'..'Z' } },
        c.getString(R.string.pwd_rule_lower) to { p: String -> p.any { it in 'a'..'z' } },
        c.getString(R.string.pwd_rule_digit) to { p: String -> p.any { it in '0'..'9' } }
    )

    fun firstUnmet(c: Context, p: String): String? =
        rules(c).firstOrNull { !it.second(p) }?.first

    /**
     * 实时四规则视图：返回附着于 password 输入框的规则列表（每行 ✓/✗ + 规则名），
     * 颜色一律 token（nxStateOk/nxStateErr，替换旧硬编码 0x33FFFFFF 强度条）。
     */
    fun buildRulesView(c: Context, pass: EditText): LinearLayout {
        val bar = LinearLayout(c).apply { orientation = LinearLayout.HORIZONTAL }
        val segs = (0 until 4).map {
            val seg = View(c).apply {
                layoutParams = LinearLayout.LayoutParams(0, c.dp(6), 1f).apply { marginEnd = c.dp(4) }
            }
            val bg = android.graphics.drawable.GradientDrawable().apply {
                cornerRadius = c.dpF(3)
                setColor(Ui.color(seg, R.attr.nxSeparator))
            }
            seg.background = bg
            seg
        }
        segs.forEach { bar.addView(it) }
        bar.setPadding(0, c.dp(8), 0, 0)

        val rules = rules(c).map { (label, fn) ->
            val tv = TextView(c).apply {
                textSize = 13f
                tag = label to fn
                setPadding(0, c.dp(3), 0, c.dp(3))
            }
            tv
        }

        fun refresh() {
            val p = pass.text.toString()
            var metCount = 0
            rules.forEach { r ->
                val (label, fn) = r.tag as Pair<String, (String) -> Boolean>
                val ok = fn(p)
                if (ok) metCount++
                r.text = (if (ok) "✓ " else "✗ ") + label
                r.setTextColor(
                    if (ok) ContextCompat.getColor(c, R.color.nx_state_ok)
                    else ContextCompat.getColor(c, R.color.nx_state_err))
            }
            segs.forEachIndexed { i, seg ->
                (seg.background as android.graphics.drawable.GradientDrawable).setColor(
                    when {
                        i < metCount && metCount <= 1 -> ContextCompat.getColor(c, R.color.nx_state_err)
                        i < metCount && metCount <= 3 -> ContextCompat.getColor(c, R.color.nx_state_warn)
                        i < metCount -> ContextCompat.getColor(c, R.color.nx_state_ok)
                        else -> Ui.color(seg, R.attr.nxSeparator)
                    })
            }
        }
        pass.addTextChangedListener(object : android.text.TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, d: Int) {}
            override fun onTextChanged(s: CharSequence?, a: Int, b: Int, d: Int) {}
            override fun afterTextChanged(s: android.text.Editable?) { refresh() }
        })
        refresh()

        val wrap = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        wrap.addView(bar)
        rules.forEach { wrap.addView(it) }
        return wrap
    }
}

/**
 * Step0 — 免责声明/EULA（必勾选；拒绝即退出）。
 * v1.4.0 ⑦：免责声明与使用条款分两段清晰呈现——先使用条款后免责声明，各自小标题
 * （onboard_section_terms / onboard_section_disclaimer，Ui.title 17sp 级）；加入
 * 「有任何疑问请加QQ群：1124936153 联系」；移除该步的「查看 LICENSE」「打开上游仓库」
 * 按钮（保留在关于页）。勾选门与即时反馈（提示行 + shake + announce）保留。
 */
class EulaFragment : StepFragment() {
    override fun stepIndex() = 0
    override fun titleText() = ctx().getString(R.string.onboard_title_eula)

    override fun buildContent(c: Context): View {
        val col = LinearLayout(c).apply { orientation = LinearLayout.VERTICAL }
        // 小标题一：使用条款（v1.4.0 ⑦）
        col.addView(ui.title(c, c.getString(R.string.onboard_section_terms)).apply {
            setPadding(0, c.dp(4), 0, c.dp(4))
        })
        // 不设 selectable：textIsSelectable 的 TextView 会吞掉外层 ScrollView 的拖动手势
        // （模拟器 API 34 实测不滚动），条款全文查看/复制仍可经关于页对应入口
        val eulaText = TextView(c)
        NexMarkdown.render(c, eulaText, c.getString(R.string.eula_markdown))
        col.addView(eulaText)
        // 小标题二：免责声明
        col.addView(ui.title(c, c.getString(R.string.onboard_section_disclaimer)).apply {
            setPadding(0, c.dp(16), 0, c.dp(4))
        })
        val disclaimerText = TextView(c)
        NexMarkdown.render(c, disclaimerText, c.getString(R.string.disclaimer_markdown))
        col.addView(disclaimerText)
        // QQ 群联系方式（可点击复制群号）
        col.addView(TextView(c).apply {
            text = c.getString(R.string.onboard_qq_help, c.getString(R.string.about_qq_number))
            textSize = 14f
            setTextColor(Ui.color(this, R.attr.nxAccent))
            setPadding(0, c.dp(16), 0, c.dp(4))
            setOnClickListener { copyText(c, c.getString(R.string.about_qq_number)) }
        })

        val agree = CheckBox(c).apply { text = getString(R.string.eula_agree) }
        // ①未勾选点击 → 勾选行下方即时提示行（nxStateWarn 13sp）④勾选后消失
        val warnHint = TextView(c).apply {
            text = c.getString(R.string.eula_must_agree)
            textSize = 13f
            setTextColor(Ui.color(this, R.attr.nxStateWarn))
            visibility = View.GONE
        }
        val btnReject = ui.button(c, getString(R.string.eula_reject)) {
            requireActivity().finishAffinity()
        }
        // 按钮常 enabled（onboardingSpec：禁用+点击无反应改为即时反馈）
        val btnAccept = ui.button(c, getString(R.string.eula_accept)) {
            if (!agree.isChecked) {
                warnHint.visibility = View.VISIBLE
                shake(agree) // ②勾选行 2 次 8dp 水平 shake
                // ③无障碍同步播报（对齐 MainActivity tab 切换实践）
                agree.announceForAccessibility(c.getString(R.string.eula_must_agree))
            } else {
                Prefs.setEulaAccepted(c, true)
                (activity as? MainActivity)?.replace(AccountFragment(), addToStack = false)
            }
        }
        agree.setOnCheckedChangeListener { _, checked -> if (checked) warnHint.visibility = View.GONE }
        // 单层滚动（v1.4.0 ⑦）：原先节内容再包一层 ScrollView 会形成双层滚动，
        // 内层吞掉手势使勾选行/按钮在长文下不可达（模拟器实测）；page() 自带滚动。
        return page(
            col,
            agree,
            warnHint,
            headerButtonRow(btnReject, btnAccept)
        )
    }

    /** 2 次 8dp 水平 shake（onboardingSpec 第1步②）。 */
    private fun shake(v: View) {
        val d = v.context.dp(8).toFloat()
        android.animation.ObjectAnimator.ofFloat(
            v, android.view.View.TRANSLATION_X, 0f, -d, d, -d, d, 0f).apply {
            duration = 320
            start()
        }
    }

    private val ui get() = Ui
}

/**
 * 引导期间传递的账号凭据（内存持有；创建成功后才经 Keystore 加密持久化）。
 */
object OnboardingState {
    var user: String? = null
    var pass: String? = null

    fun clear() { user = null; pass = null }
}

/**
 * Step1 — 创建管理员账号：用户名 + 强密码（≥10 位含大小写/数字，实时强度条）。
 * 服务端（fork auth.go validateCredentials）同步同强度校验。
 */
class AccountFragment : StepFragment() {
    override fun stepIndex() = 1
    override fun titleText() = ctx().getString(R.string.onboard_title_account)

    private val ui get() = Ui

    override fun buildContent(c: Context): View {
        val accountHint = ui.hint(c, c.getString(R.string.account_hint))
        val user = EditText(c).apply {
            this.hint = c.getString(R.string.account_username)
            isSingleLine = true
        }
        val pass = EditText(c).apply {
            this.hint = c.getString(R.string.account_password)
            inputType = android.text.InputType.TYPE_CLASS_TEXT or
                android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD
            transformationMethod = android.text.method.PasswordTransformationMethod.getInstance()
        }
        val pass2 = EditText(c).apply {
            this.hint = c.getString(R.string.account_password2)
            inputType = android.text.InputType.TYPE_CLASS_TEXT or
                android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD
            transformationMethod = android.text.method.PasswordTransformationMethod.getInstance()
        }

        val rulesView = PasswordRules.buildRulesView(c, pass)

        val btnPrev = ui.button(c, getString(R.string.prev)) {
            (activity as? MainActivity)?.replace(EulaFragment(), addToStack = false)
        }
        val btnNext = ui.button(c, getString(R.string.next)) {
            val u = user.text.toString().trim()
            val p1 = pass.text.toString()
            val p2 = pass2.text.toString()
            val firstUnmet = PasswordRules.firstUnmet(c, p1)
            when {
                u.isEmpty() || u.length > 32 -> toast(c, c.getString(R.string.username_invalid))
                firstUnmet != null -> toast(c, firstUnmet)
                p1 != p2 -> toast(c, c.getString(R.string.pwd_mismatch))
                else -> {
                    OnboardingState.user = u
                    OnboardingState.pass = p1
                    (activity as? MainActivity)?.replace(StartCoreFragment(), addToStack = false)
                }
            }
        }
        return page(
            accountHint,
            user, pass, pass2,
            rulesView,
            headerButtonRow(btnPrev, btnNext)
        )
    }
}

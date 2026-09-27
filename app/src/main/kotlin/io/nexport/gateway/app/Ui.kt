package io.nexport.gateway.app

import io.nexport.gateway.R

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.browser.customtabs.CustomTabsIntent
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import com.google.android.material.color.MaterialColors

// ---- 顶层扩展（同包自动可见：ctx.dp(...) 等） ----

fun Context.dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
fun Context.dpF(v: Int): Float = v * resources.displayMetrics.density

fun toast(ctx: Context, msg: String) =
    Toast.makeText(ctx, msg, Toast.LENGTH_SHORT).show()

fun copyText(ctx: Context, text: String, showToast: Boolean = true) {
    val cm = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    cm.setPrimaryClip(ClipData.newPlainText("NexPort", text))
    if (showToast) toast(ctx, ctx.getString(R.string.copied))
}

/** 外部链接一律走 Custom Tabs（WebView 只承载 127.0.0.1 面板）。 */
fun openExternal(ctx: Context, url: String) {
    try {
        CustomTabsIntent.Builder()
            .setShowTitle(true)
            .build()
            .launchUrl(ctx, Uri.parse(url))
    } catch (_: Exception) {
        runCatching {
            ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
        }
    }
}

/**
 * Ui — 视图构建小工具集（全部界面代码化，无 XML 布局）。
 *
 * v1.1.0 设计体系（designSystem）：
 *  - 颜色一律经自定义 attr（nxTextPrimary/nxTextSecondary/…）由
 *    MaterialColors.getColor(view, attr) 解析——深浅双色板由 values(-night)/colors.xml
 *    提供，代码内零硬编码色；
 *  - 卡片 = 12dp 圆角、elevation 0、1dp 描边（HIG 分组内缩列表扁平风）；
 *  - 字阶（sp）：大标题 34/bold、节标题 22/bold、卡题 17/semibold、正文 15、
 *    辅助 13、标签 12、等宽 13 monospace、底部导航 12；
 *  - 间距 4pt 网格：屏幕边距 16dp、卡内边距 16dp、卡间距 12dp、行最小高 48dp；
 *  - hint 废弃 alpha=0.75，改用专属三级色 token。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
object Ui {

    // ---- attr 解析 ----

    fun color(view: View, attr: Int): Int = MaterialColors.getColor(view, attr)

    // ---- 卡片（扁平描边风） ----

    fun card(ctx: Context, content: LinearLayout.() -> Unit): MaterialCardView {
        val c = MaterialCardView(ctx)
        c.radius = ctx.dpF(12)
        c.cardElevation = 0f
        c.strokeWidth = ctx.dp(1)
        c.strokeColor = color(c, R.attr.nxSeparator)
        c.setCardBackgroundColor(color(c, R.attr.nxCardSurface))
        c.setContentPadding(ctx.dp(16), ctx.dp(16), ctx.dp(16), ctx.dp(16))
        c.useCompatPadding = true
        val col = LinearLayout(ctx)
        col.orientation = LinearLayout.VERTICAL
        col.content()
        c.addView(col)
        return c
    }

    // ---- 字阶 ----

    /** 大标题 34/bold（首页顶，HIG Large Title）。 */
    fun bigTitle(ctx: Context, text: String): TextView = TextView(ctx).apply {
        this.text = text
        textSize = 34f
        setTypeface(typeface, Typeface.BOLD)
        setTextColor(color(this, R.attr.nxTextPrimary))
    }

    /** 节标题 22/bold（设置/关于分组头）。 */
    fun sectionTitle(ctx: Context, text: String): TextView = TextView(ctx).apply {
        this.text = text
        textSize = 22f
        setTypeface(typeface, Typeface.BOLD)
        setTextColor(color(this, R.attr.nxTextPrimary))
    }

    /** 卡题 17/semibold（HIG Headline）。 */
    fun title(ctx: Context, text: String): TextView = TextView(ctx).apply {
        this.text = text
        textSize = 17f
        setTypeface(Typeface.create("sans-serif", Typeface.NORMAL), Typeface.BOLD)
        setTextColor(color(this, R.attr.nxTextPrimary))
    }

    /** 正文 15。 */
    fun body(ctx: Context, text: String, mono: Boolean = false): TextView = TextView(ctx).apply {
        this.text = text
        textSize = if (mono) 13f else 15f
        setTextColor(color(this, R.attr.nxTextPrimary))
        if (mono) setTypeface(Typeface.MONOSPACE)
    }

    /** 正文次 15（次级说明行）。 */
    fun secondary(ctx: Context, text: String, mono: Boolean = false): TextView = TextView(ctx).apply {
        this.text = text
        textSize = if (mono) 13f else 15f
        setTextColor(color(this, R.attr.nxTextSecondary))
        if (mono) setTypeface(Typeface.MONOSPACE)
    }

    /** 辅助 13（三级提示色，替代旧 alpha=0.75 方案）。 */
    fun hint(ctx: Context, text: String): TextView = TextView(ctx).apply {
        this.text = text
        textSize = 13f
        setTextColor(color(this, R.attr.nxTextTertiary))
    }

    /** 标签 12。 */
    fun label(ctx: Context, text: String): TextView = TextView(ctx).apply {
        this.text = text
        textSize = 12f
        setTextColor(color(this, R.attr.nxTextTertiary))
    }

    // ---- 按钮三级（最小高 48dp） ----

    /** 主按钮：实底强调色。 */
    fun button(ctx: Context, text: String, action: () -> Unit): MaterialButton =
        MaterialButton(ctx).apply {
            this.text = text
            minHeight = ctx.dp(48)
            minimumHeight = ctx.dp(48)
            isAllCaps = false
            backgroundTintList = android.content.res.ColorStateList.valueOf(color(this, R.attr.nxBtnPrimaryBg))
            setTextColor(color(this, R.attr.nxBtnPrimaryFg))
            setOnClickListener { action() }
        }

    /** 次按钮：描边。 */
    fun subButton(ctx: Context, text: String, action: () -> Unit): MaterialButton =
        MaterialButton(ctx).apply {
            this.text = text
            minHeight = ctx.dp(48)
            minimumHeight = ctx.dp(48)
            isAllCaps = false
            strokeWidth = ctx.dp(1)
            strokeColor = android.content.res.ColorStateList.valueOf(color(this, R.attr.nxSeparator))
            setBackgroundColor(android.graphics.Color.TRANSPARENT)
            setTextColor(color(this, R.attr.nxTextPrimary))
            setOnClickListener { action() }
        }

    /** 文字按钮（列表项/行内动作，无底色）。 */
    fun textButton(ctx: Context, text: String, action: () -> Unit): MaterialButton =
        MaterialButton(ctx, null, com.google.android.material.R.attr.borderlessButtonStyle).apply {
            this.text = text
            minHeight = ctx.dp(48)
            minimumHeight = ctx.dp(48)
            isAllCaps = false
            gravity = Gravity.START or Gravity.CENTER_VERTICAL
            setTextColor(color(this, R.attr.nxAccent))
            setOnClickListener { action() }
        }

    /** 48dp 命中区图标按钮（眼睛/复制等）。 */
    fun iconButton(ctx: Context, iconRes: Int, descRes: Int, action: () -> Unit): android.widget.ImageButton =
        android.widget.ImageButton(ctx).apply {
            setImageResource(iconRes)
            contentDescription = ctx.getString(descRes)
            // 主题属性 selectableItemBackgroundBorderless 解析为背景（触控反馈）
            val tv = TypedValue()
            ctx.theme.resolveAttribute(android.R.attr.selectableItemBackgroundBorderless, tv, true)
            setBackgroundResource(tv.resourceId)
            imageTintList = android.content.res.ColorStateList.valueOf(color(this, R.attr.nxAccent))
            val s = ctx.dp(48)
            layoutParams = LinearLayout.LayoutParams(s, s)
            setOnClickListener { action() }
        }

    // ---- 布局 ----

    fun row(vararg views: View): LinearLayout = LinearLayout(views[0].context).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        views.forEach {
            addView(it, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginEnd = it.context.dp(8)
            })
        }
    }

    /** 行内无伸缩排布（信息 + 图标按钮）。 */
    fun rowAligned(vararg views: View): LinearLayout = LinearLayout(views[0].context).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        views.forEachIndexed { i, v ->
            val lp = if (i == 0)
                LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            else
                LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            if (i != views.size - 1) lp.marginEnd = v.context.dp(8)
            addView(v, lp)
        }
    }

    fun spaced(vararg views: View): LinearLayout = LinearLayout(views[0].context).apply {
        orientation = LinearLayout.VERTICAL
        views.forEachIndexed { i, v ->
            addView(v)
            if (i != views.size - 1) {
                (v.layoutParams as? ViewGroup.MarginLayoutParams)?.bottomMargin = v.context.dp(8)
            }
        }
    }

    /** 页面：16dp 屏幕边距 + 卡间距 12dp。 */
    fun page(ctx: Context, vararg cards: View): ScrollView {
        val col = LinearLayout(ctx)
        col.orientation = LinearLayout.VERTICAL
        col.setPadding(ctx.dp(16), ctx.dp(8), ctx.dp(16), ctx.dp(24))
        cards.forEachIndexed { i, v ->
            col.addView(v)
            if (i != cards.size - 1) {
                (v.layoutParams as? ViewGroup.MarginLayoutParams)?.bottomMargin = ctx.dp(12)
            }
        }
        val sv = ScrollView(ctx)
        sv.addView(col)
        return sv
    }

    /** 页面顶大标题。 */
    fun pageWithTitle(ctx: Context, title: String, vararg cards: View): ScrollView {
        val big = bigTitle(ctx, title).apply { setPadding(0, ctx.dp(8), 0, ctx.dp(8)) }
        return page(ctx, big, *cards)
    }

    /** 分组头（节标题 22/bold，组与组之间留白）。 */
    fun groupHeader(ctx: Context, text: String): TextView = sectionTitle(ctx, text).apply {
        setPadding(0, ctx.dp(16), 0, ctx.dp(4))
    }

    /** HIG 分组内缩列表行：最小高 48dp，可带图标/文字/尾部视图。 */
    fun listRow(ctx: Context, main: View, trailing: View? = null, onClick: (() -> Unit)? = null): LinearLayout {
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = ctx.dp(48)
            setPadding(0, ctx.dp(6), 0, ctx.dp(6))
            addView(main, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            trailing?.let {
                addView(it, LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                    marginStart = ctx.dp(8)
                })
            }
            if (onClick != null) {
                isClickable = true
                isFocusable = true
                setOnClickListener { onClick() }
            }
        }
        return row
    }

    /** 分组分隔线（左缩进 16dp 由卡片内边距承担，此处通栏 1dp 描边色）。 */
    fun separator(ctx: Context): View = View(ctx).apply {
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ctx.dp(1).coerceAtLeast(1))
        setBackgroundColor(color(this, R.attr.nxSeparator))
    }

    /** 空/错态：居中图标 + 主文 + 次文 + 动作。 */
    fun stateView(
        ctx: Context,
        iconRes: Int?,
        main: String,
        sub: String,
        actions: List<View> = emptyList()
    ): LinearLayout = LinearLayout(ctx).apply {
        orientation = LinearLayout.VERTICAL
        gravity = Gravity.CENTER
        val pad = ctx.dp(24)
        setPadding(pad, pad, pad, pad)
        iconRes?.let {
            addView(android.widget.ImageView(ctx).apply {
                setImageResource(it)
                imageTintList = android.content.res.ColorStateList.valueOf(color(this, R.attr.nxTextTertiary))
                importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            }, LinearLayout.LayoutParams(ctx.dp(48), ctx.dp(48)).apply { gravity = Gravity.CENTER })
        }
        addView(TextView(ctx).apply {
            text = main
            textSize = 17f
            setTypeface(typeface, Typeface.BOLD)
            setTextColor(color(this, R.attr.nxTextPrimary))
            gravity = Gravity.CENTER
            setPadding(0, ctx.dp(12), 0, 0)
        })
        if (sub.isNotEmpty()) addView(TextView(ctx).apply {
            text = sub
            textSize = 13f
            setTextColor(color(this, R.attr.nxTextTertiary))
            gravity = Gravity.CENTER
            setPadding(0, ctx.dp(6), 0, 0)
        })
        actions.forEach {
            addView(it, LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = ctx.dp(12)
            })
        }
    }

    /** 状态点（12dp 圆点；颜色由调用方经 token 设置）。 */
    fun stateDot(ctx: Context): View = View(ctx).apply {
        layoutParams = LinearLayout.LayoutParams(ctx.dp(12), ctx.dp(12)).apply { marginEnd = ctx.dp(8) }
        background = GradientDrawable().apply { shape = GradientDrawable.OVAL }
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }
}

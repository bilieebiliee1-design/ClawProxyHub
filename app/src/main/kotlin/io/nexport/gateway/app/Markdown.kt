package io.nexport.gateway.app

import io.noties.markwon.AbstractMarkwonPlugin
import io.noties.markwon.Markwon
import io.noties.markwon.MarkwonConfiguration
import io.noties.markwon.MarkwonSpansFactory
import io.noties.markwon.RenderProps
import io.noties.markwon.SpanFactory
import io.noties.markwon.core.CorePlugin
import io.noties.markwon.core.MarkwonTheme
import io.noties.markwon.ext.tables.TablePlugin
import io.noties.markwon.ext.tables.TableTheme

import io.nexport.gateway.R

import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Typeface
import android.text.Layout
import android.text.style.LeadingMarginSpan
import android.util.TypedValue
import android.widget.TextView
import org.commonmark.node.FencedCodeBlock
import org.commonmark.node.IndentedCodeBlock

/**
 * NexMarkdown — 应用内 Markdown 渲染（markdownSpec）：Markwon 4.6.2（io.noties.markwon:core
 * + ext-tables，Apache-2.0，已登记两份 THIRD_PARTY_NOTICES），纯 TextView span 渲染、
 * 无 WebView、只引 core/ext-tables 工件不启远程图片插件（安全基线：无 addJavascriptInterface）。
 *
 * 样式全走 nx* token（亮暗自动跟随 values(-night)/colors.xml；主题/语言切换经 Activity
 * 重建 → onCreateView 重新构建本渲染器 → attr 色重解析；字号随 BaseActivity fontScale
 * 的 sp 自然缩放）：
 *  - H2/H3 = nxTextPrimary 22/17sp semibold（headingTextSizeMultipliers 以 TextView 15sp
 *    为基：1.47×≈22sp、1.13×≈17sp，字号粗细对齐 Ui.title）；
 *  - 正文 = nxTextSecondary 15sp 行距 1.3（render 设定）；
 *  - 行内代码/代码块 = MONOSPACE 13sp + nxCardSurface 底；代码块另加 nxSeparator 1dp 描边
 *    （BorderedCodeBlockSpan 继承 CodeBlockSpan 补 stroke；多行换行处按行绘制为已知形制）；
 *  - 链接 = nxAccent + 下划线，LinkResolver → openExternal Custom Tabs（Ui.kt 同路）；
 *  - 引用块 = 4dp accent 左线；hr = nxSeparator 1dp；
 *  - 表格（v1.3.0 ②）：ext-tables TablePlugin——GFM 表格此前按普通段落渲染成原始
 *    竖线文本；现按 nxSeparator 1dp 网格线渲染，表头底 nxCardSurface、偶数行
 *    nxCardSurface@40% 斑马纹，亮暗主题自动跟随。
 *
 * 有序列表编号：协议/声明文内无 ordered list（条款号为 H2 标题字面文本，编号修复在
 * strings 源侧——拆分后各自连续 1..3 / 1..2）；Markwon 有序列表序号由 commonmark 解析
 * 的 start 值渲染（OrderedListItemSpan），渲染器无需改。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 */
object NexMarkdown {

    /** 构建渲染器：attr 色在调用时解析（每次 onCreateView 重建，主题切换自动跟随）。 */
    fun markwon(c: Context): Markwon {
        val accent = attrColor(c, R.attr.nxAccent)
        val separator = attrColor(c, R.attr.nxSeparator)
        val cardSurface = attrColor(c, R.attr.nxCardSurface)
        val textSecondary = attrColor(c, R.attr.nxTextSecondary)
        val sp13 = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_SP, 13f, c.resources.displayMetrics).toInt()
        val dp4 = c.dp(4)
        val dp6 = c.dp(6)
        val dp1 = c.dp(1).coerceAtLeast(1)

        return Markwon.builder(c)
            .usePlugin(CorePlugin.create())
            .usePlugin(TablePlugin.create { builder: TableTheme.Builder ->
                builder
                    .tableBorderColor(separator)
                    .tableBorderWidth(dp1)
                    .tableCellPadding(dp6)
                    .tableHeaderRowBackgroundColor(cardSurface)
                    .tableEvenRowBackgroundColor(
                        (cardSurface and 0x00FFFFFF) or 0x66000000)
            })
            .usePlugin(object : AbstractMarkwonPlugin() {
                override fun configureTheme(builder: MarkwonTheme.Builder) {
                    builder
                        .headingTypeface(Typeface.create("sans-serif", Typeface.BOLD))
                        // 基准 TextView 15sp：H1 30 / H2 22 / H3 17 / H4 15 …
                        .headingTextSizeMultipliers(floatArrayOf(2.0f, 1.47f, 1.13f, 1f, .83f, .67f))
                        .linkColor(accent)
                        .isLinkUnderlined(true)
                        // 行内代码
                        .codeTypeface(Typeface.MONOSPACE)
                        .codeTextColor(textSecondary)
                        .codeBackgroundColor(cardSurface)
                        .codeTextSize(sp13)
                        // 代码块
                        .codeBlockTypeface(Typeface.MONOSPACE)
                        .codeBlockTextColor(textSecondary)
                        .codeBlockBackgroundColor(cardSurface)
                        .codeBlockTextSize(sp13)
                        // 引用块：4dp accent 左线
                        .blockQuoteColor(accent)
                        .blockQuoteWidth(dp4)
                        // hr：nxSeparator 1dp
                        .thematicBreakColor(separator)
                        .thematicBreakHeight(dp1)
                }

                override fun configureConfiguration(builder: MarkwonConfiguration.Builder) {
                    // 链接一律 Custom Tabs（WebView 只承载 127.0.0.1 面板）
                    builder.linkResolver { view, link -> openExternal(view.context, link) }
                }

                override fun configureSpansFactory(builder: MarkwonSpansFactory.Builder) {
                    // 代码块补 1dp nxSeparator 描边：包装默认工厂追加边框 span
                    // （SpannableBuilder.setSpansInternal 递归应用数组——Markwon 4.6.2 源码已核）
                    val fenced = builder.getFactory(FencedCodeBlock::class.java)
                    val indented = builder.getFactory(IndentedCodeBlock::class.java)
                    builder.setFactory(FencedCodeBlock::class.java,
                        borderedCodeFactory(fenced, separator, dp1))
                    builder.setFactory(IndentedCodeBlock::class.java,
                        borderedCodeFactory(indented, separator, dp1))
                }
            })
            .build()
    }

    private fun borderedCodeFactory(
        origin: SpanFactory?,
        separator: Int,
        strokeWidthPx: Int
    ): SpanFactory = SpanFactory { configuration, props ->
        val border = CodeBlockBorderSpan(separator, strokeWidthPx)
        val spans = origin?.getSpans(configuration, props)
        if (spans == null) border else arrayOf(spans, border)
    }

    /** 渲染进 TextView：正文 15sp nxTextSecondary 行距 1.3（markdownSpec）。 */
    fun render(c: Context, tv: TextView, markdown: String) {
        tv.textSize = 15f
        tv.setTextColor(Ui.color(tv, R.attr.nxTextSecondary))
        tv.setLineSpacing(0f, 1.3f)
        markwon(c).setMarkdown(tv, markdown)
    }

    private fun attrColor(c: Context, attr: Int): Int {
        val tv = TypedValue()
        c.theme.resolveAttribute(attr, tv, true)
        return tv.data
    }

    /** 代码块描边 span：与 CodeBlockSpan 的 LeadingMargin 同矩形补 1dp stroke。 */
    private class CodeBlockBorderSpan(
        private val borderColor: Int,
        private val strokeWidthPx: Int
    ) : LeadingMarginSpan {

        private val paint = Paint(Paint.ANTI_ALIAS_FLAG)

        override fun getLeadingMargin(first: Boolean): Int = 0

        override fun drawLeadingMargin(
            c: Canvas, p: Paint, x: Int, dir: Int, top: Int, baseline: Int, bottom: Int,
            text: CharSequence, start: Int, end: Int, first: Boolean, layout: Layout?
        ) {
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = strokeWidthPx.toFloat()
            paint.color = borderColor
            val left: Int
            val right: Int
            if (dir > 0) {
                left = x
                right = c.width
            } else {
                left = x - c.width
                right = x
            }
            c.drawRect(left.toFloat(), top.toFloat(), right.toFloat(), bottom.toFloat(), paint)
        }
    }
}

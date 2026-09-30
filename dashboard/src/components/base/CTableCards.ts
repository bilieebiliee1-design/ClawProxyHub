// CTableCards.ts — CTable 的窄屏卡片形态：每行一张卡，列变「标签：值」堆叠，360px 无横向滚动。
// 单元格渲染与 t-table 完全同源（具名插槽 → 列 cell 函数 → 字段原文），视图不为卡片单独写渲染；
// 列描述可选标注：mobileHide（卡片隐藏低优先级列）、mobileTitle（作卡片标题列，默认取第一个未隐藏列）、
// mobileFull（值过长时整行铺满，如摘要/地址）、mobileFoldable（默认收起，卡底「更多 (N)」展开——
// 低优先级但并非无用的列，如日志的 instance/ua/stream，不再永久不可见）。op 列固定渲染为卡片底部操作区。
import { defineComponent, getCurrentInstance, h, ref, useSlots, type PropType, type VNode } from 'vue'
import { Empty as TEmpty, Loading as TLoading } from 'tdesign-vue-next'

export interface CardColumn {
  colKey: string
  title?: unknown // 字符串或渲染函数（与 TDesign 列描述一致）
  cell?: unknown // 插槽名字符串或渲染函数
  mobileHide?: boolean // 卡片形态隐藏该列
  mobileTitle?: boolean // 卡片形态作为标题列
  mobileFull?: boolean // 卡片形态该字段独占一行（长文本）
  mobileFoldable?: boolean // 卡片形态默认折叠，展开后可见（卡底「更多 (N)」/「收起」）
  [key: string]: unknown
}

type CellFn = (h: unknown, ctx: unknown) => VNode | string

export default defineComponent({
  name: 'CTableCards',
  props: {
    columns: { type: Array as PropType<CardColumn[]>, default: () => [] },
    data: { type: Array as PropType<Record<string, unknown>[]>, default: () => [] },
    rowKey: { type: String, default: '' },
    loading: { type: Boolean, default: false },
    maxHeight: { type: String, default: '' }, // 由 max-height attr 透传（如 Dashboard 45vh）
    fill: { type: Boolean, default: false }, // 由 height attr 透传：撑满父容器并内部滚动
    hasRowClick: { type: Boolean, default: false },
  },
  emits: ['row-click'],
  setup(props, { emit }) {
    const slots = useSlots()
    // 文案走全局 $t（面板 main.ts 已 app.use(i18n) 且 globalInjection:true）。
    // 不用 useI18n()：它要求组件树内有 i18n 上下文，否则抛
    // 「Need to install with app.use function」——SSR 冒烟（qa/cards-ssr.mjs）等
    // 未装插件的宿主会直接失败。无 $t 时回退键名，组件仍可渲染。
    const inst = getCurrentInstance()
    const t = (key: string, named?: Record<string, unknown>): string => {
      const fn = inst?.appContext.config.globalProperties.$t as
        | ((k: string, n?: Record<string, unknown>) => string)
        | undefined
      return fn ? fn(key, named) : key
    }

    // 折叠展开态（按行记）：mobileFoldable 列默认收起，点卡底「更多 (N)」逐卡展开
    const unfolded = ref<Set<string | number>>(new Set())
    function toggleFold(key: string | number) {
      const next = new Set(unfolded.value)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      unfolded.value = next
    }

    // 单元格渲染优先级与 t-table 一致：cell 指定/同名插槽 → cell 渲染函数 → 字段原文
    function renderCell(col: CardColumn, row: Record<string, unknown>, rowIndex: number): VNode | string {
      const ctx = { row, col, rowIndex, type: 'cell' }
      const slotName = typeof col.cell === 'string' ? col.cell : col.colKey
      const slot = slots[slotName]
      if (slot) {
        const nodes = slot(ctx)
        return (nodes as VNode[])[0] ?? ''
      }
      if (typeof col.cell === 'function') return (col.cell as CellFn)(h, ctx)
      const v = row[col.colKey]
      return v === undefined || v === null || v === '' ? '-' : String(v)
    }

    // 列标题：字符串直出，渲染函数按 TDesign 约定传 (h, { col })
    function renderTitle(col: CardColumn): VNode | string {
      if (typeof col.title === 'function') return (col.title as CellFn)(h, { col })
      return (col.title as string) ?? col.colKey
    }

    function keyOf(row: Record<string, unknown>, i: number): string | number {
      return props.rowKey && row[props.rowKey] !== undefined ? String(row[props.rowKey]) : i
    }

    return () => {
      // 卡片可见列：去掉 mobileHide；标题列不进键值区；op 走底部操作区；
      // mobileFoldable 列默认收起（卡底「更多 (N)」展开），保持首屏信息密度
      const visible = props.columns.filter((c) => !c.mobileHide && c.colKey !== 'op')
      const titleCol = visible.find((c) => c.mobileTitle) ?? visible[0]
      const rest = titleCol ? visible.filter((c) => c !== titleCol) : visible
      const foldCols = rest.filter((c) => c.mobileFoldable)
      const bodyCols = rest.filter((c) => !c.mobileFoldable)
      const opCol = props.columns.find((c) => c.colKey === 'op')

      const cards = props.data.map((row, ri) => {
        const key = keyOf(row, ri)
        const isOpen = unfolded.value.has(key)
        const shownCols = isOpen ? bodyCols.concat(foldCols) : bodyCols
        return h('div', { key, class: { 'c-card': true, 'is-clickable': props.hasRowClick }, onClick: () => props.hasRowClick && emit('row-click', { row }) }, [
          // 卡头：标题列（品牌名/名称/状态徽章等第一眼信息）
          titleCol ? h('div', { class: 'c-card__head' }, [h('div', { class: 'c-card__title' }, [renderCell(titleCol, row, ri)])]) : null,
          // 卡身：键值行（mobileFull 的长文本独占一行不压标签）
          shownCols.length
            ? h(
                'div',
                { class: 'c-card__body' },
                shownCols.map((col) =>
                  h('div', { key: col.colKey, class: { 'c-card__field': true, 'is-full': !!col.mobileFull } }, [
                    h('span', { class: 'c-card__label' }, [renderTitle(col)]),
                    h('span', { class: 'c-card__value' }, [renderCell(col, row, ri)]),
                  ]),
                ),
              )
            : null,
          // 卡底：折叠开关（左）+ 操作区（编辑/删除等链接，右）
          opCol || foldCols.length
            ? h('div', { class: 'c-card__foot' }, [
                foldCols.length
                  ? h(
                      'button',
                      {
                        type: 'button',
                        class: 'c-card__more',
                        'aria-expanded': isOpen ? 'true' : 'false',
                        onClick: (e: Event) => {
                          e.stopPropagation() // 行可点击时不顺带触发行点击
                          toggleFold(key)
                        },
                      },
                      isOpen ? t('common.collapse') : t('common.moreItems', { n: foldCols.length }),
                    )
                  : null,
                opCol ? h('div', { class: 'c-card__op' }, [renderCell(opCol, row, ri)]) : null,
              ])
            : null,
        ])
      })

      // 空态与 t-table 默认空态一致；否则渲染卡片列表
      const content: VNode = !props.loading && !props.data.length ? h(TEmpty) : h('div', { class: 'c-cards-list' }, cards)

      // 滚动策略：fill（height="100%" 链）或 maxHeight（如 45vh）都让卡片列表内部滚动，
      // 其余自然高度交给页面滚动；loading 时包 TLoading 转圈遮罩
      const scrollStyle: Record<string, string> = {}
      if (props.maxHeight) {
        scrollStyle.maxHeight = props.maxHeight
        scrollStyle.overflowY = 'auto'
      }
      return h(
        TLoading,
        { loading: props.loading, showOverlay: true, class: { 'c-table-cards': true, 'is-fill': props.fill }, style: scrollStyle },
        { default: () => [content] },
      )
    }
  },
})

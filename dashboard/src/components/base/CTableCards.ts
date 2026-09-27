// CTableCards.ts — CTable 的窄屏卡片形态：每行一张卡，列变「标签：值」堆叠，360px 无横向滚动。
// 单元格渲染与 t-table 完全同源（具名插槽 → 列 cell 函数 → 字段原文），视图不为卡片单独写渲染；
// 列描述可选标注：mobileHide（卡片隐藏低优先级列）、mobileTitle（作卡片标题列，默认取第一个未隐藏列）、
// mobileFull（值过长时整行铺满，如摘要/地址）。op 列固定渲染为卡片底部操作区。
import { defineComponent, h, useSlots, type PropType, type VNode } from 'vue'
import { Empty as TEmpty, Loading as TLoading } from 'tdesign-vue-next'

export interface CardColumn {
  colKey: string
  title?: unknown // 字符串或渲染函数（与 TDesign 列描述一致）
  cell?: unknown // 插槽名字符串或渲染函数
  mobileHide?: boolean // 卡片形态隐藏该列
  mobileTitle?: boolean // 卡片形态作为标题列
  mobileFull?: boolean // 卡片形态该字段独占一行（长文本）
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
      // 卡片可见列：去掉 mobileHide；标题列不进键值区；op 走底部操作区
      const visible = props.columns.filter((c) => !c.mobileHide && c.colKey !== 'op')
      const titleCol = visible.find((c) => c.mobileTitle) ?? visible[0]
      const bodyCols = titleCol ? visible.filter((c) => c !== titleCol) : visible
      const opCol = props.columns.find((c) => c.colKey === 'op')

      const cards = props.data.map((row, ri) =>
        h('div', { key: keyOf(row, ri), class: { 'c-card': true, 'is-clickable': props.hasRowClick }, onClick: () => props.hasRowClick && emit('row-click', { row }) }, [
          // 卡头：标题列（品牌名/名称/状态徽章等第一眼信息）
          titleCol ? h('div', { class: 'c-card__head' }, [h('div', { class: 'c-card__title' }, [renderCell(titleCol, row, ri)])]) : null,
          // 卡身：键值行（mobileFull 的长文本独占一行不压标签）
          bodyCols.length
            ? h(
                'div',
                { class: 'c-card__body' },
                bodyCols.map((col) =>
                  h('div', { key: col.colKey, class: { 'c-card__field': true, 'is-full': !!col.mobileFull } }, [
                    h('span', { class: 'c-card__label' }, [renderTitle(col)]),
                    h('span', { class: 'c-card__value' }, [renderCell(col, row, ri)]),
                  ]),
                ),
              )
            : null,
          // 卡底：操作区（编辑/删除等链接横向排布）
          opCol ? h('div', { class: 'c-card__foot' }, [renderCell(opCol, row, ri)]) : null,
        ]),
      )

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

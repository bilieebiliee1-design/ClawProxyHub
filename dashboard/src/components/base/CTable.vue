<!-- CTable — 统一二次封装表格：行悬停背景提亮 + 斑马纹关闭，集中管理边框/圆角。
     窄屏（≤768px，与 mobile.css 断点一致）自动切卡片形态（CTableCards）：
     单元格渲染与 t-table 同源（同名插槽 / cell 函数），视图零成本获得 360px 无横向滚动体验；
     列描述可标注 mobileHide（卡片隐藏）/ mobileTitle（卡片标题列）/ mobileFull（长文本整行）。 -->
<template>
  <t-table v-if="!isNarrow" v-bind="$attrs" class="c-table">
    <template v-for="(_, name) in $slots" #[name]="slotProps">
      <slot :name="name" v-bind="slotProps ?? {}" />
    </template>
  </t-table>
  <c-table-cards
    v-else
    :columns="columns"
    :data="data"
    :row-key="rowKey"
    :loading="loading"
    :max-height="maxHeight"
    :fill="fill"
    :has-row-click="!!onRowClick"
    @row-click="(ctx: { row: Record<string, unknown> }) => onRowClick?.(ctx)"
  >
    <template v-for="(_, name) in $slots" #[name]="slotProps">
      <slot :name="name" v-bind="slotProps ?? {}" />
    </template>
  </c-table-cards>
</template>

<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { useMediaQuery } from '../../composables'
import CTableCards, { type CardColumn } from './CTableCards'

defineOptions({ inheritAttrs: false })

const attrs = useAttrs() as Record<string, any>
const { matches: isNarrow } = useMediaQuery()

// 透传给卡片形态的关键 attrs（t-table 分支仍走 v-bind 原样透传）
const columns = computed<CardColumn[]>(() => (Array.isArray(attrs.columns) ? (attrs.columns as CardColumn[]) : []))
const data = computed<Record<string, unknown>[]>(() => (Array.isArray(attrs.data) ? (attrs.data as Record<string, unknown>[]) : []))
// row-key 可能是字符串，也可能是函数（如账号详情动态表格按行号取 key）——后者卡片按行号当 key
const rowKey = computed(() => {
  const v = attrs['row-key'] ?? attrs.rowKey
  return typeof v === 'string' ? v : ''
})
const loading = computed(() => !!attrs.loading)
const maxHeight = computed(() => (attrs['max-height'] ?? attrs.maxHeight ?? '') as string)
// height="100%"（Tasks/Logs 的高度链）：卡片列表撑满父容器并内部滚动
const fill = computed(() => (attrs.height ?? '') === '100%')
const onRowClick = computed(() => attrs.onRowClick as ((ctx: { row: Record<string, unknown> }) => void) | undefined)
</script>

<style>
/* 统一表格质感：单元格边框降为弱描边，行悬停仅背景提亮 */
.c-table .t-table__cell {
  transition: background-color 0.2s ease-out;
}

/* ---------- 窄屏卡片形态（CTableCards） ---------- */
.c-table-cards.is-fill {
  height: 100%;
  overflow-y: auto;
}
.c-cards-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 2px;
}
.c-card {
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: var(--td-bg-color-container);
  padding: 12px 14px;
}
.c-card.is-clickable {
  cursor: pointer;
}
.c-card.is-clickable:active {
  background: var(--td-bg-color-container-hover);
}
.c-card__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
.c-card__title {
  font-weight: 600;
  font-size: 14px;
  color: var(--td-text-color-primary);
  word-break: break-word;
  min-width: 0;
}
/* 键值行：标签左、值右对齐；长值允许换行，绝不横向溢出 */
.c-card__field {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 5px 0;
  font-size: 13px;
  line-height: 1.5;
}
.c-card__field + .c-card__field {
  border-top: 1px dashed var(--td-component-stroke);
}
.c-card__label {
  flex: none;
  color: var(--td-text-color-placeholder);
  max-width: 40%;
}
.c-card__value {
  flex: 1;
  min-width: 0;
  text-align: right;
  color: var(--td-text-color-primary);
  word-break: break-all;
  white-space: normal;
}
/* 长文本字段整行铺满：标签在上、值在下，摘要/地址类不挤压 */
.c-card__field.is-full {
  flex-direction: column;
  gap: 4px;
}
.c-card__field.is-full .c-card__label {
  max-width: none;
}
.c-card__field.is-full .c-card__value {
  text-align: left;
}
.c-card__foot {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--td-component-stroke);
  display: flex;
  align-items: center;
  /* 操作区恒靠右；折叠开关用 margin-right:auto 靠左，无开关时行为与从前一致 */
  justify-content: flex-end;
  gap: 8px;
  min-height: 40px; /* 触控热区 */
}
/* 折叠开关（mobileFoldable 列）：收起时「更多 N 项」、展开后「收起」。
   视觉轻量（品牌色文字、无边框），40px 高兜住触控下限。 */
.c-card__more {
  flex: none;
  display: inline-flex;
  align-items: center;
  height: 40px;
  padding: 0 2px;
  margin-right: auto; /* 靠左，操作链接保持靠右 */
  border: none;
  background: none;
  cursor: pointer;
  font-size: 13px;
  color: var(--td-brand-color);
  font-family: inherit;
  touch-action: manipulation;
}
.c-card__more:active {
  opacity: 0.6;
}
.c-card__op {
  display: flex;
  justify-content: flex-end;
  min-width: 0;
}
/* 操作区/值内的 t-space 允许换行，链接热区放大 */
.c-card .t-link {
  padding: 8px 6px;
}
.c-card .t-space {
  flex-wrap: wrap;
  row-gap: 2px;
}
.c-card .t-tag {
  word-break: break-all;
}
</style>

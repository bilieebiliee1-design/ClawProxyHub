<!-- FilterBar — 筛选条：flex-wrap 自动换行 + min-width:0 允许收缩，窄视口不溢出。
     子项用 w-* 工具类给定宽（theme.css），≤1024px 转弹性等分（由本组件样式覆盖），
     避免逐个视图写死内联宽度后无法整体调节（B7）。移动端筛选走各页面的底部抽屉 + MobileFab。 -->
<template>
  <div class="filter-bar">
    <slot />
  </div>
</template>

<style scoped>
.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  align-items: center;
  /* 压住 min-width:auto：允许收缩触发内部折行 */
  min-width: 0;
  flex: 1;
}
/* 子项默认不允许横向溢出（定宽控件在窄视口内收窄） */
.filter-bar > :deep(.t-input),
.filter-bar > :deep(.t-select),
.filter-bar > :deep(.t-date-range-picker) {
  max-width: 100%;
}
/* 窄桌面/平板横屏：定宽控件转弹性，一行放不下时等分而非逐个独占行 */
@media (max-width: 1024px) {
  .filter-bar > .w-xs,
  .filter-bar > .w-sm,
  .filter-bar > .w-md,
  .filter-bar > .w-lg,
  .filter-bar > .w-xl {
    width: auto !important;
    flex: 1 1 180px;
    min-width: 0;
  }
}
</style>

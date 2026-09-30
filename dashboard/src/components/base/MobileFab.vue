<!-- MobileFab — 手机端右下角悬浮操作区：统一固定定位，避开分页条与底部安全区。
     默认 slot 放按钮（按顺序自上而下排列，可放筛选 + 新建两个）。 -->
<template>
  <div class="mobile-fab">
    <slot />
  </div>
</template>

<style scoped>
.mobile-fab {
  position: fixed;
  right: max(16px, env(safe-area-inset-right));
  bottom: calc(72px + env(safe-area-inset-bottom));
  z-index: 100; /* 低于抽屉/弹层（TDesign 1500+），不遮挡浮层 */
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.mobile-fab :deep(.t-button) {
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.18);
}
</style>

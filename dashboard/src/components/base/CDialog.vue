<!-- CDialog — 统一二次封装弹窗：圆角/头部字重集中管理。 -->
<template>
  <t-dialog v-bind="$attrs" class="c-dialog">
    <template v-for="(_, name) in $slots" #[name]="slotProps">
      <slot :name="name" v-bind="slotProps ?? {}" />
    </template>
  </t-dialog>
</template>

<script setup lang="ts">
defineOptions({ inheritAttrs: false })
</script>

<style>
/* 统一弹窗质感：中圆角 + 头部 600 字重 */
.c-dialog .t-dialog {
  border-radius: 12px;
}
/* 窄屏收敛（验收②）：width="680px" 等固定宽弹窗在 411dp 视口横向溢出
   （添加账号向导 10 张客户端卡需横向滑动才可见）——此处统一钳到 94vw，
   各视图固定宽立即生效，无需逐个改 width */
.c-dialog .t-dialog {
  max-width: 94vw;
}
.c-dialog .t-dialog__header {
  font-weight: 600;
}
</style>

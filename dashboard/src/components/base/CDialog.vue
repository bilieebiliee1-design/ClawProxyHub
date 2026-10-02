<!-- CDialog — 统一二次封装弹窗：居中弹性模型，圆角/头部字重集中管理。
     attach="body"：TDesign 默认把弹层渲染在组件所在位置——头部区 .t-layout__header 有
     backdrop-filter（theme.css），会为后代建立包含块，弹窗的 position:fixed 以头部为参照
     → 定位错位/被裁切（上游 aa49795 同因修复）。统一挂到 body 一处覆盖全部 CDialog 弹窗；
     主题变量挂在 documentElement 上，挂 body 不丢明暗主题。
     placement="center"：弹层脱离 TDesign 默认 top 位——top 位带 padding-top:20vh
     （dist/tdesign.css .t-dialog--top，680px 视口=136px 顶部偏移），与旧移动端 100vh
     全屏化叠加导致 footer 初开在折叠线下；居中后移动端走 mobile.css 的弹性限高模型
     （整卡 max-height:100% + body 内滚，上游 5bc957d 同模型）。 -->
<template>
  <t-dialog v-bind="$attrs" placement="center" attach="body" class="c-dialog">
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

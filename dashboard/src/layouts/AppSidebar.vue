<!-- AppSidebar — 侧栏：logo + 菜单 + 收起脚。
     桌面端保持常驻侧栏（可收起）；移动端（mobile=true）改为左侧抽屉，由头部汉堡按钮唤起，
     选中菜单/路由变化/点抽屉以外区域（遮罩）即关闭。 -->
<template>
  <template v-if="!mobile">
    <t-aside :width="collapsed ? '64px' : '200px'" class="aside">
      <div class="logo" @click="router.push('/dashboard')">
        <img class="logo-badge" :src="brandLogo" :alt="branding.name" />
        <span v-if="!collapsed" class="logo-text" :class="{ custom: brandCustom }" :title="branding.name">
          <template v-if="brandCustom">{{ branding.name }}</template>
          <template v-else>Nex<span>Port</span></template>
        </span>
      </div>
      <t-menu
        :value="route.path"
        :collapsed="collapsed"
        :width="collapsed ? '64px' : '200px'"
        class="aside-menu"
        @change="(v: string) => router.push(v)"
      >
        <t-menu-item v-for="item in items" :key="item.value" :value="item.value">
          <template #icon><component :is="item.icon" /></template>{{ $t(item.label) }}
        </t-menu-item>
      </t-menu>
      <div class="aside-footer" @click="collapsed = !collapsed">
        <template v-if="!collapsed">
          <chevron-left-icon />
          <span class="aside-footer-text">{{ $t('common.collapse') }}</span>
        </template>
        <chevron-right-icon v-else />
      </div>
    </t-aside>
  </template>

  <!-- 移动端抽屉侧栏。
       visible 必须走 v-model 双向绑定：TDesign Drawer 点遮罩/Esc 关闭只 emit
       update:visible，单向 :visible + 不存在的 @visible-change 会吞掉该事件，
       内部 isVisible 与 props.visible 失步 → 点侧边栏以外区域（遮罩）收不起来。
       closeOnOverlayClick 默认 true（全局配置），显式声明以免全局配置改动后回退。 -->
  <t-drawer
    v-else
    v-model:visible="open"
    placement="left"
    size="280px"
    :footer="false"
    :header="false"
    :close-on-overlay-click="true"
    class="aside-drawer"
  >
    <div class="drawer-inner">
      <div class="logo" @click="go('/dashboard')">
        <img class="logo-badge" :src="brandLogo" :alt="branding.name" />
        <span class="logo-text" :class="{ custom: brandCustom }" :title="branding.name">
          <template v-if="brandCustom">{{ branding.name }}</template>
          <template v-else>Nex<span>Port</span></template>
        </span>
      </div>
      <t-menu
        :value="route.path"
        :width="'280px'"
        class="drawer-menu"
        @change="(v: string) => go(v)"
      >
        <t-menu-item v-for="item in items" :key="item.value" :value="item.value">
          <template #icon><component :is="item.icon" /></template>{{ $t(item.label) }}
        </t-menu-item>
      </t-menu>
      <!-- 抽屉脚：从头部移下来的功能入口（B4）。头部在 360px 下放不下这些图标，
           移到这里既保住功能可达性，又把标题宽度还给头部。 -->
      <div class="drawer-foot">
        <notif-bell />
        <lang-switch />
        <theme-switch />
      </div>
    </div>
  </t-drawer>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChevronLeftIcon, ChevronRightIcon } from 'tdesign-icons-vue-next'
import { branding, brandLogo, brandCustom } from '../utils/branding'
import { NotifBell, LangSwitch, ThemeSwitch } from './header'
import type { MenuItem } from './types'

defineProps<{
  items: MenuItem[]
  /** 移动端：侧栏收进抽屉 */
  mobile?: boolean
}>()

const open = defineModel<boolean>('open', { default: false })

const route = useRoute()
const router = useRouter()

// 收起状态持久化（仅桌面侧栏）
const collapsed = ref(localStorage.getItem('cph-sidebar') === 'collapsed')

// 抽屉模式：选中即关（路由随 @change push）
function go(path: string) {
  open.value = false
  router.push(path)
}

// 路由变化兜底关抽屉（浏览器前进/后退等）
watch(() => route.fullPath, () => { if (open.value) open.value = false })
</script>

<style scoped>
.aside {
  flex-shrink: 0; /* TDesign sider 默认参与收缩，会把 220px 挤没 */
  display: flex;
  flex-direction: column;
  transition: width 0.25s;
  overflow: hidden;
}
.logo {
  height: 64px;
  min-width: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  cursor: pointer;
  color: var(--td-brand-color);
  font-size: 18px;
  font-weight: 700;
  flex-shrink: 0;
}
.logo-badge {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  border: 1px solid var(--td-brand-color-3); /* 品牌色描边 */
  flex-shrink: 0;
  object-fit: cover;
}
.logo-text {
  font-weight: 700;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 140px;
}
/* 自定义品牌名：品牌色高亮字（不用渐变） */
.logo-text.custom {
  font-size: 16px;
  color: var(--td-brand-color);
}
.logo-text span {
  font-weight: 300;
  opacity: 0.8;
  margin-left: 2px;
}
.aside-menu {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
}
.aside-footer {
  height: 40px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  cursor: pointer;
  color: var(--td-text-color-placeholder);
  border-top: 1px solid var(--td-component-border);
  flex-shrink: 0;
}
.aside-footer:hover {
  color: var(--td-brand-color);
}
.aside-footer-text {
  font-size: 12px;
  white-space: nowrap;
}
/* 抽屉内布局：logo + 可滚菜单占满 */
.drawer-inner {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.drawer-menu {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
}
/* 抽屉脚：功能图标行（通知/语言/主题），底部安全区避让 */
.drawer-foot {
  display: flex;
  align-items: center;
  justify-content: space-around;
  gap: 4px;
  padding: 4px 8px calc(4px + env(safe-area-inset-bottom));
  border-top: 1px solid var(--td-component-border);
  flex-shrink: 0;
}
</style>

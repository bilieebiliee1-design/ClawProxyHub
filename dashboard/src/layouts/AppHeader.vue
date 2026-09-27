<!-- AppHeader — 头部：左页面标题/描述 + 右功能区（版本/通知/语言/主题/用户）。
     移动端（mobile=true）：左侧汉堡按钮唤起抽屉侧栏；版本/GitHub 徽标隐藏（触屏无 hover 意义，窄屏让位）。 -->
<template>
  <t-header class="header">
    <div class="header-left">
      <t-button
        v-if="mobile"
        variant="text"
        shape="square"
        theme="default"
        class="menu-btn"
        :aria-label="$t('common.menu')"
        @click="$emit('toggleMenu')"
      >
        <view-list-icon />
      </t-button>
      <div class="header-title-wrap">
        <div class="header-title">{{ $t(currentPage.label) }}</div>
        <div class="header-desc">{{ $t(currentPage.desc) }}</div>
      </div>
    </div>
    <div class="header-right">
      <version-chip v-if="!mobile" />
      <github-icon-btn v-if="!mobile" />
      <notif-bell />
      <lang-switch />
      <theme-switch />
      <user-menu :username="username" :role="role" />
    </div>
  </t-header>
</template>

<script setup lang="ts">
import { ViewListIcon } from 'tdesign-icons-vue-next'
import { GithubIconBtn, NotifBell, LangSwitch, ThemeSwitch, UserMenu, VersionChip } from './header'
import type { MenuItem } from './types'

defineProps<{
  username: string
  role: string
  currentPage: MenuItem
  /** 移动端：出汉堡按钮，隐藏版本/GitHub 徽标 */
  mobile?: boolean
}>()

defineEmits<{ (e: 'toggleMenu'): void }>()
</script>

<style scoped>
/* 状态头：左右结构，固定不滚动 */
.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 64px;
  padding: 0 24px;
  flex-shrink: 0;
}
.header-left {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.header-title-wrap {
  min-width: 0;
}
.header-title {
  font-size: 16px;
  font-weight: 700;
  line-height: 1.3;
}
.header-desc {
  font-size: 12px;
  color: var(--td-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.header-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
</style>

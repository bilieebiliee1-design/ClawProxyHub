<!-- GithubIconBtn — 头部 GitHub 链接按钮。 -->
<template>
  <t-tooltip :content="$t('common.github')">
    <t-button variant="text" shape="square" theme="default" @click="openGithub">
      <logo-github-icon />
    </t-button>
  </t-tooltip>
</template>

<script setup lang="ts">
import { LogoGithubIcon } from 'tdesign-icons-vue-next'

// 项目主页：上游项目（NexPort 基于 ClawProxyHub 衍生，AGPL-3.0 署名与源码链路）。
function openGithub() {
  window.open('https://github.com/ShadowSmallBaby/ClawProxyHub', '_blank')
}
</script>

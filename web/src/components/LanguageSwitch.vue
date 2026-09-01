<script setup lang="ts">
import { useLocale } from '../i18n'

const { locale, setLocale, text } = useLocale()
</script>

<template>
  <div class="language-switch" role="group" :aria-label="text('界面语言', 'Interface language')">
    <button type="button" :class="{ active: locale === 'zh-CN' }" :aria-pressed="locale === 'zh-CN'" @click="setLocale('zh-CN')">中</button>
    <button type="button" :class="{ active: locale === 'en-US' }" :aria-pressed="locale === 'en-US'" @click="setLocale('en-US')">EN</button>
  </div>
</template>

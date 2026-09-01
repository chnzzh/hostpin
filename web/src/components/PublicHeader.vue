<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useLocale } from '../i18n'
import LanguageSwitch from './LanguageSwitch.vue'

const session = useSessionStore()
const appearance = ref(localStorage.getItem('appearance') ?? 'system')
const siteName = computed(() => session.site?.name ?? 'HOSTPIN')
const { text } = useLocale()

function cycleAppearance() {
  const values = ['system', 'dark', 'light']
  const next = values[(values.indexOf(appearance.value) + 1) % values.length] ?? 'system'
  appearance.value = next
  localStorage.setItem('appearance', next)
  document.documentElement.dataset.appearance = next
}
</script>

<template>
  <header class="public-header">
    <RouterLink class="wordmark" to="/" :aria-label="text('返回监控首页', 'Hostpin overview')">
      <img class="wordmark-icon" src="/hostpin-icon.svg?v=2" alt="" aria-hidden="true" />
      <span>{{ siteName }}</span>
    </RouterLink>
    <nav class="header-actions" :aria-label="text('主导航', 'Global navigation')">
      <div class="header-primary-nav">
        <RouterLink class="public-nav-link" to="/">{{ text('节点', 'Nodes') }}</RouterLink>
        <RouterLink class="public-nav-link" to="/latency">{{ text('延迟', 'Latency') }}</RouterLink>
      </div>
      <span class="header-clock"><i /> {{ text('实时', 'Live') }}</span>
      <LanguageSwitch />
      <button class="icon-button" type="button" :aria-label="text(`外观：${appearance}`, `Appearance: ${appearance}`)" @click="cycleAppearance">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v2m0 14v2M3 12h2m14 0h2M5.6 5.6 7 7m10 10 1.4 1.4M18.4 5.6 17 7M7 17l-1.4 1.4M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z" /></svg>
      </button>
      <RouterLink class="text-link" :to="session.loggedIn ? '/admin' : '/login'">
        {{ session.loggedIn ? text('管理', 'Admin') : text('登录', 'Sign in') }} <span>↗</span>
      </RouterLink>
    </nav>
  </header>
</template>

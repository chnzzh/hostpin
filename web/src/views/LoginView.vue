<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { useLocale } from '../i18n'
import LanguageSwitch from '../components/LanguageSwitch.vue'

const session = useSessionStore()
const route = useRoute()
const router = useRouter()
const username = ref('admin')
const password = ref('')
const totp = ref('')
const busy = ref(false)
const error = ref('')
const { text } = useLocale()

async function submit() {
  busy.value = true
  error.value = ''
  try {
    await session.login(username.value, password.value, totp.value)
    await router.push(typeof route.query.next === 'string' ? route.query.next : '/admin')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('登录失败，请检查账号信息。', 'Login failed. Check your credentials.')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="auth-layout">
    <section class="auth-brand"><RouterLink class="wordmark" to="/"><img class="wordmark-icon" src="/hostpin-icon.svg?v=2" alt="" aria-hidden="true" /><span>{{ session.site?.name || 'HOSTPIN' }}</span></RouterLink><div><span class="eyebrow">{{ text('管理后台', 'Administration') }}</span><h1>{{ text('登录 Hostpin', 'Sign in to Hostpin') }}</h1><p>{{ text('管理节点、告警、探测任务、备份和站点设置。', 'Manage nodes, alerts, probes, backups, and site settings.') }}</p></div><footer>{{ text('安全会话 · CSRF 防护', 'SECURE SESSION · CSRF PROTECTED') }}</footer></section>
    <section class="auth-form-wrap">
      <div class="auth-utility"><LanguageSwitch /></div>
      <form class="auth-form" @submit.prevent="submit">
        <header><h2>{{ text('管理员登录', 'Administrator sign in') }}</h2><p>{{ text('使用初始化时设置的管理员账号。', 'Use the administrator account configured during setup.') }}</p></header>
        <label><span>{{ text('用户名', 'Username') }}</span><input v-model="username" autocomplete="username" required autofocus /></label>
        <label><span>{{ text('密码', 'Password') }}</span><input v-model="password" type="password" autocomplete="current-password" required /></label>
        <label><span>{{ text('两步验证码或恢复码', 'TOTP or recovery code') }} <small>{{ text('可选', 'Optional') }}</small></span><input v-model="totp" inputmode="numeric" autocomplete="one-time-code" placeholder="000000" /></label>
        <div v-if="error" class="form-error">{{ error }}</div>
        <button class="primary-action" :disabled="busy">{{ busy ? text('正在登录…', 'Signing in…') : text('登录', 'Sign in') }} <span>→</span></button>
        <RouterLink class="back-link" to="/">← {{ text('返回监控首页', 'Return to public status') }}</RouterLink>
      </form>
    </section>
  </main>
</template>

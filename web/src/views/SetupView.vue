<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { useSessionStore } from '../stores/session'
import { useLocale } from '../i18n'
import LanguageSwitch from '../components/LanguageSwitch.vue'

const router = useRouter()
const session = useSessionStore()
const username = ref('admin')
const password = ref('')
const confirmation = ref('')
const pin = ref('')
const siteName = ref('Hostpin')
const description = ref('')
const busy = ref(false)
const error = ref('')
const passwordValid = computed(() => password.value.length >= 12 && password.value === confirmation.value)
const pinValid = computed(() => pin.value.length >= 6)
const { text } = useLocale()

async function submit() {
  if (!passwordValid.value || !pinValid.value) return
  busy.value = true
  error.value = ''
  try {
    await api('/api/v1/setup', { method: 'POST', body: JSON.stringify({ username: username.value, password: password.value, enrollment_pin: pin.value, site_name: siteName.value, site_description: description.value }) })
    await session.refresh()
    await router.push('/admin')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('初始化失败，请重试。', 'Setup failed. Please try again.')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="setup-layout">
    <aside class="setup-rail"><div class="wordmark"><img class="wordmark-icon" src="/hostpin-icon.svg?v=2" alt="" aria-hidden="true" /><span>HOSTPIN</span></div><ol><li class="active"><b>01</b><span>{{ text('站点', 'Site') }}</span></li><li><b>02</b><span>{{ text('管理员', 'Admin') }}</span></li><li><b>03</b><span>{{ text('注册 PIN', 'Enrollment PIN') }}</span></li></ol><p>{{ text('首次设置', 'First-time setup') }}</p></aside>
    <section class="setup-content">
      <div class="setup-utility"><LanguageSwitch /></div>
      <header><span class="eyebrow">{{ text('首次设置', 'First-time setup') }}</span><h1>{{ text('初始化 Hostpin', 'Set up Hostpin') }}</h1><p>{{ text('设置站点信息、管理员账号和探针注册 PIN。完成后即可添加节点。', 'Configure the site, administrator account, and Agent enrollment PIN.') }}</p></header>
      <form class="setup-form" @submit.prevent="submit">
        <fieldset><legend><b>01</b> {{ text('站点信息', 'Site') }}</legend><div class="form-grid"><label><span>{{ text('站点名称', 'Site name') }}</span><input v-model="siteName" required maxlength="128" /></label><label class="wide"><span>{{ text('站点说明（可选）', 'Description (optional)') }}</span><input v-model="description" maxlength="512" :placeholder="text('例如：我的服务器监控', 'For example: My server monitor')" /></label></div></fieldset>
        <fieldset><legend><b>02</b> {{ text('管理员账号', 'Administrator') }}</legend><div class="form-grid"><label><span>{{ text('用户名', 'Username') }}</span><input v-model="username" autocomplete="username" minlength="3" required /></label><label><span>{{ text('密码', 'Password') }} <small>{{ text('至少 12 位', '12+ characters') }}</small></span><input v-model="password" type="password" autocomplete="new-password" minlength="12" required /></label><label><span>{{ text('确认密码', 'Confirm password') }}</span><input v-model="confirmation" type="password" autocomplete="new-password" required /></label></div></fieldset>
        <fieldset><legend><b>03</b> {{ text('探针注册', 'Agent enrollment') }}</legend><div class="pin-block"><label><span>{{ text('注册 PIN', 'Enrollment PIN') }}</span><input v-model="pin" type="password" autocomplete="off" minlength="6" maxlength="64" required :placeholder="text('6–64 位数字或字符', '6–64 digits or characters')" /></label><p>{{ text('PIN 仅用于新探针注册。注册成功后，每个节点会使用自己的独立凭据。建议使用 12 位以上随机字符。', 'The PIN is used only to enroll new Agents. Each node receives an independent credential. Use 12 or more random characters when possible.') }}</p></div></fieldset>
        <div v-if="error" class="form-error">{{ error }}</div>
        <button class="primary-action" :disabled="busy || !passwordValid || !pinValid">{{ busy ? text('正在初始化…', 'Setting up…') : text('完成设置', 'Complete setup') }} <span>→</span></button>
      </form>
    </section>
  </main>
</template>

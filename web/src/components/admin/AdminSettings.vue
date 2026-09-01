<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { dateTime } from '../../format'
import { useLocale } from '../../i18n'
import { useSessionStore } from '../../stores/session'
import type { SiteSettings, TemporaryEnrollmentPIN } from '../../types'

const session = useSessionStore()
const settings = ref<SiteSettings | null>(null)
const newPIN = ref('')
const status = ref('')
const error = ref('')
const sources = ref('')
const temporaryPIN = ref<TemporaryEnrollmentPIN | null>(null)
const temporaryPINPlain = ref('')
const temporaryMinutes = ref(30)
const temporaryBusy = ref(false)
const temporaryCopied = ref(false)
const clock = ref(Date.now())
const { text } = useLocale()
let clockTimer: number | undefined
let statusTimer: number | undefined

const effectiveTemporaryStatus = computed(() => {
  if (!temporaryPIN.value) return ''
  if (temporaryPIN.value.status === 'active' && new Date(temporaryPIN.value.expires_at).getTime() <= clock.value) return 'expired'
  return temporaryPIN.value.status
})

const temporaryStatusLabel = computed(() => {
  const labels = {
    active: text('可使用', 'Active'),
    used: text('已使用', 'Used'),
    expired: text('已过期', 'Expired'),
    revoked: text('已撤销', 'Revoked'),
  }
  return labels[effectiveTemporaryStatus.value as keyof typeof labels] ?? text('未创建', 'Not created')
})

const temporaryRemaining = computed(() => {
  if (!temporaryPIN.value || effectiveTemporaryStatus.value !== 'active') return ''
  const seconds = Math.max(0, Math.ceil((new Date(temporaryPIN.value.expires_at).getTime() - clock.value) / 1000))
  if (seconds < 60) return text('不足 1 分钟', 'less than 1 minute')
  const minutes = Math.ceil(seconds / 60)
  if (minutes < 60) return text(`剩余 ${minutes} 分钟`, `${minutes} minutes left`)
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return text(`剩余 ${hours} 小时 ${rest} 分钟`, `${hours}h ${rest}m left`)
})

const canRevokeTemporaryPIN = computed(() => ['active', 'used'].includes(effectiveTemporaryStatus.value))

function clearMessages() {
  error.value = ''
  status.value = ''
}

async function loadTemporaryPIN(silent = false) {
  try {
    const latest = unwrap(await api<{ data: TemporaryEnrollmentPIN | null }>('/api/v1/admin/enrollment/temporary-pin'))
    if (latest?.id !== temporaryPIN.value?.id) temporaryPINPlain.value = ''
    temporaryPIN.value = latest
  } catch (reason) {
    if (!silent) error.value = reason instanceof Error ? reason.message : text('临时 PIN 状态加载失败。', 'Could not load temporary PIN status.')
  }
}

async function load() {
  clearMessages()
  try {
    const [site] = await Promise.all([
      api<{ data: SiteSettings }>('/api/v1/admin/settings'),
      loadTemporaryPIN(),
    ])
    settings.value = unwrap(site)
    sources.value = settings.value.theme_market_sources?.join('\n') ?? ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('设置加载失败。', 'Could not load settings.')
  }
}

async function save() {
  if (!settings.value) return
  clearMessages()
  settings.value.theme_market_sources = sources.value.split('\n').map((value) => value.trim()).filter(Boolean)
  try {
    settings.value = unwrap(await api<{ data: SiteSettings }>('/api/v1/admin/settings', {
      method: 'PUT',
      body: JSON.stringify(settings.value),
    }))
    await session.refresh()
    status.value = text('设置已保存。', 'Settings saved.')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('保存失败。', 'Save failed.')
  }
}

async function rotatePIN() {
  clearMessages()
  try {
    const result = await api<{ weak_pin: boolean }>('/api/v1/admin/enrollment/pin', {
      method: 'PUT',
      body: JSON.stringify({ pin: newPIN.value }),
    })
    if (settings.value) settings.value.enrollment_pin_weak = result.weak_pin
    status.value = result.weak_pin
      ? text('PIN 已更换，但当前 PIN 强度较弱。', 'PIN changed, but it is considered weak.')
      : text('注册 PIN 已更换。', 'Enrollment PIN changed.')
    newPIN.value = ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('PIN 更换失败。', 'PIN change failed.')
  }
}

async function createTemporaryPIN() {
  clearMessages()
  temporaryBusy.value = true
  temporaryCopied.value = false
  try {
    const created = unwrap(await api<{ data: TemporaryEnrollmentPIN }>('/api/v1/admin/enrollment/temporary-pin', {
      method: 'POST',
      body: JSON.stringify({ expires_in_minutes: temporaryMinutes.value }),
    }))
    temporaryPIN.value = created
    temporaryPINPlain.value = created.pin ?? ''
    clock.value = Date.now()
    status.value = text('临时 PIN 已生成，明文仅在本页显示一次。', 'Temporary PIN generated; its plaintext is shown only on this page.')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('临时 PIN 生成失败。', 'Could not generate temporary PIN.')
  } finally {
    temporaryBusy.value = false
  }
}

async function copyTemporaryPIN() {
  if (!temporaryPINPlain.value) return
  try {
    await navigator.clipboard.writeText(temporaryPINPlain.value)
    temporaryCopied.value = true
    window.setTimeout(() => { temporaryCopied.value = false }, 1800)
  } catch {
    error.value = text('浏览器不允许复制，请手动选择 PIN。', 'Clipboard access was denied; select the PIN manually.')
  }
}

async function revokeTemporaryPIN() {
  if (!temporaryPIN.value) return
  clearMessages()
  temporaryBusy.value = true
  try {
    const revoked = unwrap(await api<{ data: TemporaryEnrollmentPIN | null }>('/api/v1/admin/enrollment/temporary-pin', { method: 'DELETE' }))
    temporaryPIN.value = revoked
    temporaryPINPlain.value = ''
    status.value = text('临时 PIN 已撤销。', 'Temporary PIN revoked.')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('临时 PIN 撤销失败。', 'Could not revoke temporary PIN.')
  } finally {
    temporaryBusy.value = false
  }
}

onMounted(() => {
  void load()
  clockTimer = window.setInterval(() => { clock.value = Date.now() }, 1000)
  statusTimer = window.setInterval(() => { void loadTemporaryPIN(true) }, 15_000)
})

onBeforeUnmount(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
  if (statusTimer !== undefined) window.clearInterval(statusTimer)
})
</script>

<template>
  <section class="admin-page">
    <header class="page-title">
      <div>
        <span class="eyebrow">{{ text('站点设置', 'Site settings') }}</span>
        <h1>{{ text('系统设置', 'System settings') }}</h1>
        <p>{{ text('管理站点、可见性、历史保留和节点注册。', 'Manage the site, visibility, retention, and enrollment.') }}</p>
      </div>
      <button class="primary-action compact-action" @click="save">{{ text('保存设置', 'Save settings') }}</button>
    </header>

    <div v-if="status" class="notice success"><b>{{ text('已保存', 'Saved') }}</b><span>{{ status }}</span></div>
    <div v-if="error" class="notice error"><b>{{ text('操作失败', 'Failed') }}</b><span>{{ error }}</span></div>
    <div v-if="settings?.enrollment_pin_weak" class="notice warning">
      <b>{{ text('注册 PIN 强度较弱', 'Weak enrollment PIN') }}</b>
      <span>{{ text('建议改用 12 位以上随机字符，或限制可注册的来源网段。', 'Use 12 or more random characters, or restrict enrollment source networks.') }}</span>
    </div>

    <template v-if="settings">
      <div class="settings-layout">
        <nav class="settings-index">
          <a href="#site">01 / {{ text('站点', 'Site') }}</a>
          <a href="#privacy">02 / {{ text('可见性', 'Privacy') }}</a>
          <a href="#retention">03 / {{ text('历史', 'Retention') }}</a>
          <a href="#location">04 / {{ text('位置', 'Location') }}</a>
          <a href="#enrollment">05 / {{ text('注册', 'Enrollment') }}</a>
        </nav>

        <div class="settings-sections">
          <section id="site" class="settings-section">
            <header><b>01</b><div><h2>{{ text('站点信息', 'Site identity') }}</h2><p>{{ text('公开显示的名称、说明和自定义 HTML。', 'Public name, description, and optional custom HTML.') }}</p></div></header>
            <div class="form-grid">
              <label><span>{{ text('站点名称', 'Site name') }}</span><input v-model="settings.name" /></label>
              <label class="wide"><span>{{ text('站点说明', 'Description') }}</span><textarea v-model="settings.description" rows="3" /></label>
              <label class="wide"><span>{{ text('自定义 HEAD HTML', 'Custom head HTML') }}</span><textarea v-model="settings.custom_head" rows="4" spellcheck="false" /></label>
              <label class="wide"><span>{{ text('自定义 BODY HTML', 'Custom body HTML') }}</span><textarea v-model="settings.custom_body" rows="4" spellcheck="false" /></label>
            </div>
          </section>

          <section id="privacy" class="settings-section">
            <header><b>02</b><div><h2>{{ text('可见性', 'Visibility') }}</h2><p>{{ text('设置公开访问和历史记录。', 'Configure public access and history recording.') }}</p></div></header>
            <div class="toggle-stack">
              <label><input v-model="settings.private" type="checkbox" /><span><b>{{ text('私有站点', 'Private site') }}</b><small>{{ text('查看状态前必须登录管理员账号。', 'Require administrator authentication for status pages.') }}</small></span></label>
              <label><input v-model="settings.record_enabled" type="checkbox" /><span><b>{{ text('保存历史指标', 'Persistent history') }}</b><small>{{ text('按保留策略存储降采样指标。', 'Store downsampled metrics according to retention.') }}</small></span></label>
              <label><input v-model="settings.enrollment_enabled" type="checkbox" /><span><b>{{ text('允许 PIN 注册', 'PIN enrollment enabled') }}</b><small>{{ text('关闭后，已注册节点仍可继续上报。', 'Existing Agents continue reporting when disabled.') }}</small></span></label>
            </div>
          </section>

          <section id="retention" class="settings-section">
            <header><b>03</b><div><h2>{{ text('历史保留', 'Retention') }}</h2><p>{{ text('延长保留期不会补回已经清理的数据。', 'Increasing retention cannot restore removed data.') }}</p></div></header>
            <div class="retention-grid">
              <label><span>{{ text('原始数据（小时）', 'Raw data (hours)') }}</span><input v-model.number="settings.raw_retention_hours" type="number" min="24" /><small>{{ text('60 秒数据点', '60-second points') }}</small></label>
              <label><span>{{ text('5 分钟聚合（小时）', '5-minute data (hours)') }}</span><input v-model.number="settings.five_minute_retention_hours" type="number" min="24" /><small>{{ text('默认 90 天', 'Default 90 days') }}</small></label>
              <label><span>{{ text('1 小时聚合（小时）', 'Hourly data (hours)') }}</span><input v-model.number="settings.hourly_retention_hours" type="number" min="24" /><small>{{ text('默认 365 天', 'Default 365 days') }}</small></label>
            </div>
          </section>

          <section id="location" class="settings-section">
            <header><b>04</b><div><h2>{{ text('位置与主题源', 'Location and theme sources') }}</h2><p>{{ text('手动设置的节点位置始终优先。', 'Manual node location always takes priority.') }}</p></div></header>
            <div class="form-grid">
              <label class="switch-line"><input v-model="settings.geoip_enabled" type="checkbox" /><span>{{ text('注册或公网 IP 变化时查询位置', 'Resolve location on enrollment or public IP change') }}</span></label>
              <label class="wide"><span>{{ text('位置服务 URL（使用 {ip}）', 'Provider URL (use {ip})') }}</span><input v-model="settings.geoip_provider" /></label>
              <label class="wide"><span>{{ text('主题市场源（每行一个 URL）', 'Theme market sources (one URL per line)') }}</span><textarea v-model="sources" rows="3" /></label>
            </div>
          </section>

          <section id="enrollment" class="settings-section enrollment-settings">
            <header><b>05</b><div><h2>{{ text('节点注册', 'Node enrollment') }}</h2><p>{{ text('长期 PIN 与一次性临时 PIN 互不影响。', 'The permanent PIN and one-use temporary PIN work independently.') }}</p></div></header>
            <div class="enrollment-pin-tools">
              <section class="permanent-pin-block">
                <header><div><b>{{ text('长期 PIN', 'Permanent PIN') }}</b><small>{{ text('更换后不会影响已经签发的节点凭据。', 'Rotation does not affect existing node credentials.') }}</small></div><span>PRIMARY</span></header>
                <form class="inline-pin-form" @submit.prevent="rotatePIN">
                  <label><span>{{ text('新 PIN（6–64 位）', 'New PIN (6–64 characters)') }}</span><input v-model="newPIN" type="password" minlength="6" maxlength="64" required /></label>
                  <button class="secondary-action">{{ text('更换 PIN', 'Rotate PIN') }}</button>
                </form>
              </section>

              <section class="temporary-pin-block">
                <header><div><b>{{ text('临时 PIN', 'Temporary PIN') }}</b><small>{{ text('成功注册 1 台节点后失效，明文只显示一次。', 'Expires after one successful enrollment; plaintext is shown once.') }}</small></div><span>ONE USE</span></header>
                <div class="temporary-pin-controls">
                  <label><span>{{ text('有效时间', 'Validity') }}</span><select v-model.number="temporaryMinutes"><option :value="10">10 {{ text('分钟', 'minutes') }}</option><option :value="30">30 {{ text('分钟', 'minutes') }}</option><option :value="60">1 {{ text('小时', 'hour') }}</option><option :value="360">6 {{ text('小时', 'hours') }}</option><option :value="1440">24 {{ text('小时', 'hours') }}</option></select></label>
                  <button type="button" class="primary-action compact-action" :disabled="temporaryBusy" @click="createTemporaryPIN">{{ temporaryBusy ? text('处理中…', 'Working…') : temporaryPIN ? text('重新生成', 'Generate new') : text('生成临时 PIN', 'Generate temporary PIN') }}</button>
                </div>

                <article v-if="temporaryPIN" class="temporary-pin-status" :data-status="effectiveTemporaryStatus">
                  <header><span>{{ text('当前临时凭据', 'Current temporary credential') }}</span><b>{{ temporaryStatusLabel }}</b></header>
                  <div v-if="temporaryPINPlain" class="temporary-pin-secret">
                    <code data-testid="temporary-pin-value">{{ temporaryPINPlain }}</code>
                    <button type="button" @click="copyTemporaryPIN">{{ temporaryCopied ? text('已复制', 'Copied') : text('复制', 'Copy') }}</button>
                  </div>
                  <p v-else-if="effectiveTemporaryStatus === 'active'">{{ text('明文已离开当前页面，不能再次查看；需要时请重新生成。', 'The plaintext is no longer available; generate a new PIN if needed.') }}</p>
                  <dl><div><dt>{{ text('状态', 'Status') }}</dt><dd>{{ temporaryStatusLabel }}<small v-if="temporaryRemaining">{{ temporaryRemaining }}</small></dd></div><div><dt>{{ text('到期时间', 'Expires') }}</dt><dd>{{ dateTime(temporaryPIN.expires_at) }}</dd></div><div><dt>{{ text('用途', 'Use') }}</dt><dd>{{ text('仅注册 1 台新节点', 'One new node only') }}</dd></div></dl>
                  <footer><small>{{ text('ID', 'ID') }} {{ temporaryPIN.id.slice(0, 8) }}</small><button v-if="canRevokeTemporaryPIN" type="button" :disabled="temporaryBusy" @click="revokeTemporaryPIN">{{ text('立即撤销', 'Revoke now') }}</button></footer>
                </article>
                <div v-else class="temporary-pin-empty">{{ text('当前没有临时 PIN。', 'No temporary PIN exists.') }}</div>
              </section>
            </div>
          </section>
        </div>
      </div>
    </template>
  </section>
</template>

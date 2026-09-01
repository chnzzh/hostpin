<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { clone } from '../../clone'
import { localized } from '../../format'
import { useSessionStore } from '../../stores/session'
import type { Theme } from '../../types'
import { useLocale } from '../../i18n'

interface ManagedItem {
  key: string
  name?: string | Record<string, string>
  help?: string | Record<string, string>
  type: string
  options?: string
  default?: unknown
  required?: boolean
}

interface MarketTheme {
  name: string | Record<string, string>
  short: string
  description?: string | Record<string, string>
  version?: string
  author?: string | Record<string, string>
  download: string
  sha256: string
}

const session = useSessionStore()
const themes = ref<Theme[]>([])
const upload = ref<File | null>(null)
const checksum = ref('')
const sourceURL = ref('')
const sourceSHA = ref('')
const error = ref('')
const status = ref('')
const busy = ref(false)
const configuring = ref<Theme | null>(null)
const managedItems = ref<ManagedItem[]>([])
const managedSettings = ref<Record<string, any>>({})
const selectorText = ref<Record<string, string>>({})
const rawPanel = ref('')
const marketOpen = ref(false)
const marketSearch = ref('')
const market = ref<MarketTheme[]>([])
const { text } = useLocale()

const filteredMarket = computed(() => {
  const query = marketSearch.value.trim().toLowerCase()
  if (!query) return market.value
  return market.value.filter((item) => [localized(item.name), localized(item.description), localized(item.author), item.short].join(' ').toLowerCase().includes(query))
})

async function load() {
  themes.value = unwrap(await api<{ data: Theme[] }>('/api/v1/admin/themes'))
  await session.refresh()
}

async function uploadTheme() {
  if (!upload.value) return
  busy.value = true; error.value = ''; status.value = ''
  const form = new FormData()
  form.append('theme', upload.value)
  if (checksum.value) form.append('sha256', checksum.value)
  try {
    await api('/api/v1/admin/themes/upload', { method: 'POST', body: form })
    upload.value = null; checksum.value = ''; status.value = text('主题包已验证并安装。', 'Theme package validated and installed.')
    await load()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('上传失败。', 'Upload failed.') }
  finally { busy.value = false }
}

async function installURL() {
  busy.value = true; error.value = ''; status.value = ''
  try {
    await api('/api/v1/admin/themes/install', { method: 'POST', body: JSON.stringify({ url: sourceURL.value, sha256: sourceSHA.value }) })
    sourceURL.value = ''; sourceSHA.value = ''; status.value = text('主题已验证并安装。', 'Verified release installed.')
    await load()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('安装失败。', 'Install failed.') }
  finally { busy.value = false }
}

async function activate(short: string) {
  await api(`/api/v1/admin/themes/${short}/activate`, { method: 'POST' })
  await session.refresh()
}

async function remove(theme: Theme) {
  if (confirm(text(`删除主题“${localized(theme.manifest.name)}”？`, `Delete theme “${localized(theme.manifest.name)}”?`))) {
    await api(`/api/v1/admin/themes/${theme.manifest.short}`, { method: 'DELETE' })
    await load()
  }
}

function openConfiguration(theme: Theme) {
  const configuration = theme.manifest.configuration
  const kind = configuration?.type || 'managed'
  if (kind === 'redirect' && typeof configuration?.data === 'string') {
    window.location.assign(new URL(configuration.data, `${window.location.origin}/`).toString())
    return
  }
  configuring.value = theme
  if (kind === 'raw') {
    rawPanel.value = typeof configuration?.data === 'string' ? configuration.data : ''
    managedItems.value = []
    return
  }
  const items = Array.isArray(configuration?.data) ? configuration.data as ManagedItem[] : []
  managedItems.value = items
  managedSettings.value = {}
  selectorText.value = {}
  for (const item of items) {
    if (!item.key || item.type === 'title' || item.type === 'textbox') continue
    let value = item.default
    if (value === undefined) value = item.type === 'switch' ? false : item.type === 'number' ? 0 : item.type === 'select' ? options(item)[0] ?? '' : ''
    if (Object.prototype.hasOwnProperty.call(theme.settings, item.key)) value = clone(theme.settings[item.key])
    if (item.type === 'nodes' || item.type === 'pingtasks') {
      selectorText.value[item.key] = Array.isArray(value) ? JSON.stringify(value) : String(value ?? '')
    } else {
      managedSettings.value[item.key] = value
    }
  }
}

function options(item: ManagedItem): string[] {
  return (item.options ?? '').split(',').map((value) => value.trim()).filter(Boolean)
}

function itemName(item: ManagedItem): string {
  return localized(item.name) || item.key
}

function parseSelector(item: ManagedItem): unknown[] {
  const raw = selectorText.value[item.key]?.trim() ?? ''
  if (!raw) return []
  try {
    const value = JSON.parse(raw)
    if (Array.isArray(value)) return value
  } catch { /* comma-separated fallback below */ }
  const values = raw.split(',').map((value) => value.trim()).filter(Boolean)
  return item.type === 'pingtasks' ? values.map(Number).filter(Number.isFinite) : values
}

async function saveConfiguration() {
  if (!configuring.value) return
  busy.value = true; error.value = ''; status.value = ''
  try {
    for (const item of managedItems.value) {
      if (item.type === 'nodes' || item.type === 'pingtasks') managedSettings.value[item.key] = parseSelector(item)
    }
    const response = await api<{ data: Theme }>(`/api/v1/admin/themes/${configuring.value.manifest.short}/settings`, {
      method: 'PUT', body: JSON.stringify(managedSettings.value),
    })
    const updated = unwrap(response)
    themes.value = themes.value.map((theme) => theme.manifest.short === updated.manifest.short ? updated : theme)
    configuring.value = null
    status.value = text('主题设置已保存。', 'Theme settings saved.')
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('无法保存主题设置。', 'Theme settings could not be saved.') }
  finally { busy.value = false }
}

async function openMarket() {
  marketOpen.value = true; error.value = ''
  if (market.value.length) return
  busy.value = true
  try {
    const catalog = await api<{ themes?: MarketTheme[] }>('/api/v1/admin/themes/market')
    market.value = Array.isArray(catalog.themes) ? catalog.themes : []
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('无法加载主题市场。', 'Theme market could not be loaded.') }
  finally { busy.value = false }
}

function installedVersion(short: string): string {
  return themes.value.find((theme) => theme.manifest.short === short)?.manifest.version ?? ''
}

async function installMarket(theme: MarketTheme) {
  busy.value = true; error.value = ''; status.value = ''
  try {
    await api('/api/v1/admin/themes/install', { method: 'POST', body: JSON.stringify({ url: theme.download, sha256: theme.sha256 }) })
    status.value = text(`${localized(theme.name)} ${theme.version ?? ''} 已安装。`, `${localized(theme.name)} ${theme.version ?? ''} installed.`)
    await load()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('主题市场安装失败。', 'Market install failed.') }
  finally { busy.value = false }
}

onMounted(load)
</script>

<template>
  <section class="admin-page">
    <header class="page-title">
      <div><span class="eyebrow">{{ text('界面主题', 'Interface themes') }}</span><h1>{{ text('主题', 'Themes') }}</h1><p>{{ text('安装和切换兼容 Komari 的公开页面主题。', 'Install and switch Komari-compatible public themes.') }}</p></div>
      <div class="title-actions"><span class="page-code">{{ text('当前', 'Active') }} / {{ session.site?.theme || 'default' }}</span><button class="primary-action compact-action" @click="openMarket">{{ text('主题市场', 'Theme market') }}</button></div>
    </header>
    <div v-if="status" class="notice success"><b>{{ text('主题已更新', 'Theme updated') }}</b><span>{{ status }}</span></div>
    <div v-if="error" class="notice error"><b>{{ text('主题操作失败', 'Theme rejected') }}</b><span>{{ error }}</span></div>
    <div class="theme-grid">
      <article class="theme-card default-theme">
        <div class="theme-preview"><div class="preview-rails"><i /><i /><i /></div><b>HP</b></div>
        <div><span>{{ text('内置', 'Built-in') }}</span><h3>{{ text('Hostpin 默认主题', 'Hostpin default') }}</h3><p>{{ text('适配桌面和移动端的原生界面。', 'Native interface for desktop and mobile.') }}</p></div>
        <footer><span v-if="session.site?.theme === 'default'" class="tag">{{ text('使用中', 'Active') }}</span><button v-else @click="activate('default')">{{ text('启用', 'Activate') }}</button></footer>
      </article>
      <article v-for="theme in themes" :key="theme.manifest.short" class="theme-card">
        <div class="theme-preview"><img v-if="theme.manifest.preview" :src="`/themes/${theme.manifest.short}/${theme.manifest.preview}`" alt="" /><b v-else>{{ theme.manifest.short.slice(0, 2).toUpperCase() }}</b></div>
        <div><span>{{ theme.manifest.version || text('无版本', 'Unversioned') }} / {{ localized(theme.manifest.author) }}</span><h3>{{ localized(theme.manifest.name) }}</h3><p>{{ localized(theme.manifest.description) || theme.manifest.short }}</p></div>
        <footer>
          <span v-if="session.site?.theme === theme.manifest.short" class="tag">{{ text('使用中', 'Active') }}</span><button v-else @click="activate(theme.manifest.short)">{{ text('启用', 'Activate') }}</button>
          <button v-if="theme.manifest.configuration" @click="openConfiguration(theme)">{{ text('配置', 'Configure') }}</button>
          <a :href="`/?preview_theme=${theme.manifest.short}`" target="_blank">{{ text('预览', 'Preview') }}</a>
          <button v-if="session.site?.theme !== theme.manifest.short" class="danger" @click="remove(theme)">{{ text('删除', 'Delete') }}</button>
        </footer>
      </article>
    </div>
    <div class="admin-columns install-columns">
      <form class="admin-panel" @submit.prevent="uploadTheme">
        <div class="panel-label"><span>{{ text('上传 ZIP', 'Upload ZIP') }}</span><b>{{ text('本地主题包', 'Local package') }}</b></div>
        <label class="file-drop"><input type="file" accept=".zip,application/zip" required @change="upload = ($event.target as HTMLInputElement).files?.[0] ?? null" /><span>{{ upload?.name || text('选择 Komari 主题 ZIP', 'Select Komari theme ZIP') }}</span><small>{{ text('压缩后最大 25 MiB，解压后最大 100 MiB', '25 MiB compressed / 100 MiB expanded maximum') }}</small></label>
        <label><span>SHA-256 <small>{{ text('可选', 'Optional') }}</small></span><input v-model="checksum" minlength="64" maxlength="64" /></label>
        <button class="primary-action" :disabled="busy || !upload">{{ text('验证并安装', 'Validate and install') }}</button>
      </form>
      <form class="admin-panel" @submit.prevent="installURL">
        <div class="panel-label"><span>{{ text('从 URL 安装', 'Install from URL') }}</span><b>{{ text('需校验', 'Verified URL') }}</b></div>
        <label><span>ZIP URL</span><input v-model="sourceURL" type="url" required /></label>
        <label><span>SHA-256 <small>{{ text('必填', 'Required') }}</small></span><input v-model="sourceSHA" required minlength="64" maxlength="64" /></label>
        <button class="primary-action" :disabled="busy">{{ text('下载并安装', 'Fetch and install') }}</button>
      </form>
    </div>
    <section class="security-note"><b>{{ text('ZIP 安全检查', 'ZIP safety checks') }}</b><span>{{ text('路径穿越 · 符号链接 · 重复路径 · 压缩比 · 文件数 · 清单 · SHA-256', 'Traversal · symlink · duplicate path · expansion ratio · file count · manifest · SHA-256') }}</span><i>{{ text('已启用', 'Enforced') }}</i></section>

    <div v-if="configuring" class="modal-backdrop" @click.self="configuring = null">
      <form class="drawer theme-config-drawer" @submit.prevent="saveConfiguration">
        <header><div><span>{{ text('主题配置', 'Theme configuration') }}</span><h2>{{ localized(configuring.manifest.configuration?.name as string | Record<string, string>) || localized(configuring.manifest.name) }}</h2></div><button type="button" @click="configuring = null">×</button></header>
        <div class="drawer-body">
          <iframe v-if="configuring.manifest.configuration?.type === 'raw'" class="raw-theme-panel" sandbox="allow-scripts allow-forms" :srcdoc="rawPanel" title="Theme configuration" />
          <div v-else class="theme-managed-form">
            <template v-for="(item, index) in managedItems" :key="item.key || index">
              <div v-if="item.type === 'title'" class="panel-label"><span>{{ itemName(item) }}</span><b>{{ String(index + 1).padStart(2, '0') }}</b></div>
              <p v-else-if="item.type === 'textbox'" class="managed-help">{{ itemName(item) }}</p>
              <label v-else-if="item.type === 'switch'" class="switch-line"><input v-model="managedSettings[item.key]" type="checkbox" /><span>{{ itemName(item) }}</span></label>
              <label v-else-if="item.type === 'select'"><span>{{ itemName(item) }}</span><select v-model="managedSettings[item.key]" :required="item.required"><option v-for="option in options(item)" :key="option">{{ option }}</option></select><small>{{ localized(item.help) }}</small></label>
              <label v-else-if="item.type === 'number'"><span>{{ itemName(item) }}</span><input v-model.number="managedSettings[item.key]" type="number" :required="item.required" /><small>{{ localized(item.help) }}</small></label>
              <label v-else-if="item.type === 'richtext'"><span>{{ itemName(item) }}</span><textarea v-model="managedSettings[item.key]" rows="6" :required="item.required" /><small>{{ localized(item.help) }}</small></label>
              <label v-else-if="item.type === 'nodes' || item.type === 'pingtasks'"><span>{{ itemName(item) }} / JSON OR CSV</span><textarea v-model="selectorText[item.key]" rows="3" /><small>{{ localized(item.help) }}</small></label>
              <label v-else><span>{{ itemName(item) }}</span><input v-model="managedSettings[item.key]" :required="item.required" /><small>{{ localized(item.help) }}</small></label>
            </template>
            <div v-if="!managedItems.length" class="empty-state admin-empty"><span>◐</span><h3>{{ text('没有可配置项', 'No configurable fields') }}</h3></div>
          </div>
        </div>
        <footer><button type="button" @click="configuring = null">{{ text('关闭', 'Close') }}</button><button v-if="configuring.manifest.configuration?.type !== 'raw'" class="primary-action" :disabled="busy">{{ text('保存设置', 'Save settings') }}</button></footer>
      </form>
    </div>

    <div v-if="marketOpen" class="modal-backdrop" @click.self="marketOpen = false">
      <section class="drawer market-drawer">
        <header><div><span>{{ text('已验证目录', 'Verified catalog') }}</span><h2>{{ text('主题市场', 'Theme market') }}</h2></div><button type="button" @click="marketOpen = false">×</button></header>
        <div class="drawer-body"><label><span>{{ text('搜索主题', 'Search market') }}</span><input v-model="marketSearch" :placeholder="text('名称 / 作者 / 标识', 'Name / author / short')" /></label><div class="market-list"><article v-for="theme in filteredMarket" :key="theme.short"><div><span>{{ theme.version || text('无版本', 'Unversioned') }} / {{ localized(theme.author) }}</span><h3>{{ localized(theme.name) }}</h3><p>{{ localized(theme.description) || theme.short }}</p></div><button class="secondary-action" :disabled="busy" @click="installMarket(theme)">{{ installedVersion(theme.short) ? (installedVersion(theme.short) === theme.version ? text('重新安装', 'Reinstall') : text('更新', 'Update')) : text('安装', 'Install') }}</button></article></div></div>
        <footer><span>{{ text(`${filteredMarket.length} 个主题包`, `${filteredMarket.length} verified packages`) }}</span><button @click="marketOpen = false">{{ text('关闭', 'Close') }}</button></footer>
      </section>
    </div>
  </section>
</template>

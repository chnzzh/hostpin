<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import AdminOverview from '../components/admin/AdminOverview.vue'
import AdminNodes from '../components/admin/AdminNodes.vue'
import AdminRules from '../components/admin/AdminRules.vue'
import AdminProbes from '../components/admin/AdminProbes.vue'
import AdminLatency from '../components/admin/AdminLatency.vue'
import AdminNotifications from '../components/admin/AdminNotifications.vue'
import AdminThemes from '../components/admin/AdminThemes.vue'
import AdminSettings from '../components/admin/AdminSettings.vue'
import AdminBackups from '../components/admin/AdminBackups.vue'
import AdminAudit from '../components/admin/AdminAudit.vue'
import AdminSecurity from '../components/admin/AdminSecurity.vue'
import LanguageSwitch from '../components/LanguageSwitch.vue'
import { useLocale } from '../i18n'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const section = computed(() => String(route.params.section || 'overview'))
const views: Record<string, unknown> = { overview: AdminOverview, nodes: AdminNodes, latency: AdminLatency, alerts: AdminRules, probes: AdminProbes, notifications: AdminNotifications, themes: AdminThemes, settings: AdminSettings, backups: AdminBackups, security: AdminSecurity, audit: AdminAudit }
const current = computed(() => views[section.value] ?? AdminOverview)
const { text } = useLocale()
const nav = computed(() => [
  ['overview', text('概览', 'Overview'), '⌁'], ['nodes', text('节点', 'Nodes'), '▦'], ['latency', text('延迟节点', 'Latency nodes'), '⌁'], ['alerts', text('告警规则', 'Alert rules'), '△'],
  ['probes', text('服务探测', 'Service probes'), '⌁'], ['notifications', text('通知渠道', 'Notifications'), '◇'], ['themes', text('主题', 'Themes'), '◐'],
  ['settings', text('站点设置', 'Site settings'), '⚙'], ['backups', text('备份与恢复', 'Backup & restore'), '⇄'], ['security', text('访问安全', 'Access security'), '⌾'], ['audit', text('审计日志', 'Audit log'), '≡'],
])
const currentLabel = computed(() => nav.value.find((item) => item[0] === section.value)?.[1] ?? text('概览', 'Overview'))

async function logout() {
  await session.logout()
  await router.push('/')
}
</script>

<template>
  <div class="admin-layout km-admin-layout">
    <aside class="admin-sidebar">
      <RouterLink class="wordmark admin-wordmark" to="/"><img class="wordmark-icon" src="/hostpin-icon.svg?v=2" alt="" aria-hidden="true" /><span>{{ session.site?.name || 'HOSTPIN' }}</span></RouterLink>
      <div class="admin-context"><span>{{ text('管理后台', 'Administration') }}</span><b><i /> {{ text('系统正常', 'Operational') }}</b></div>
      <nav :aria-label="text('后台导航', 'Administration')">
        <RouterLink v-for="item in nav" :key="item[0]" :to="`/admin/${item[0]}`" :class="{ active: section === item[0] }"><i>{{ item[2] }}</i><span>{{ item[1] }}</span><b>→</b></RouterLink>
      </nav>
      <footer><div><span>{{ text('管理员', 'Administrator') }}</span><b>{{ session.username }}</b></div><button @click="logout">{{ text('退出登录', 'Sign out') }} ↗</button><small>HOSTPIN {{ session.version }}</small></footer>
    </aside>
    <main class="admin-main km-main">
      <header class="admin-topbar"><div><span>HOSTPIN / {{ text('管理', 'ADMIN') }}</span><b>{{ currentLabel }}</b></div><div><LanguageSwitch /><RouterLink to="/">{{ text('监控首页', 'Public status') }} ↗</RouterLink><span class="utc-clock">UTC {{ new Date().toISOString().slice(11, 19) }}</span></div></header>
      <component :is="current" />
    </main>
  </div>
</template>

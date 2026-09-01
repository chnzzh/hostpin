<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { bytes, dateTime } from '../../format'
import type { AlertEvent, AdminNode } from '../../types'
import { useLocale } from '../../i18n'

const nodes = ref<AdminNode[]>([])
const events = ref<AlertEvent[]>([])
const copied = ref(false)
const installCommand = computed(() => `curl -fsSL ${location.origin}/install.sh | sh`)
const windowsInstaller = `${location.origin}/install.ps1`
const online = computed(() => nodes.value.filter((node) => node.last_seen_at && Date.now() - new Date(node.last_seen_at).getTime() < 90_000).length)
const capacity = computed(() => nodes.value.reduce((sum, node) => sum + (node.traffic_limit || 0), 0))
const { text } = useLocale()

async function load() {
  const [nodeResponse, eventResponse] = await Promise.all([
    api<{ data: AdminNode[] }>('/api/v1/admin/nodes'),
    api<{ data: AlertEvent[] }>('/api/v1/admin/alerts/events?limit=8'),
  ])
  nodes.value = unwrap(nodeResponse)
  events.value = unwrap(eventResponse)
}
async function copyCommand() {
  await navigator.clipboard.writeText(installCommand.value)
  copied.value = true
  window.setTimeout(() => { copied.value = false }, 1500)
}
onMounted(load)
</script>

<template>
  <section class="admin-page km-page-admin-dashboard">
    <header class="page-title"><div><span class="eyebrow">{{ text('系统概览', 'System overview') }}</span><h1>{{ text('概览', 'Overview') }}</h1><p>{{ text('查看节点、告警和注册状态。', 'Review nodes, alerts, and enrollment status.') }}</p></div></header>
    <div class="admin-stat-grid"><div><span>{{ text('已注册节点', 'Enrolled nodes') }}</span><strong>{{ nodes.length }}</strong><small>{{ text(`${online} 个在线`, `${online} currently online`) }}</small></div><div><span>{{ text('活动告警', 'Active incidents') }}</span><strong>{{ events.filter((event) => event.status === 'firing').length }}</strong><small>{{ text(`最近 ${events.length} 条事件`, `${events.length} recent transitions`) }}</small></div><div><span>{{ text('流量配额', 'Traffic quota') }}</span><strong>{{ capacity ? bytes(capacity) : text('未设置', 'Unbound') }}</strong><small>{{ text('所有节点声明的配额', 'Declared fleet capacity') }}</small></div><div><span>{{ text('节点注册', 'Enrollment') }}</span><strong class="word">PIN</strong><small>{{ text('注册后使用独立节点凭据', 'Independent credentials after enrollment') }}</small></div></div>
    <div class="admin-columns">
      <section class="admin-panel enroll-panel"><div class="panel-label"><span>{{ text('添加节点', 'Enroll a node') }}</span><b>01</b></div><p>{{ text('在 Linux、OpenWrt、NAS、macOS 或 FreeBSD 上运行。安装程序会在终端中询问注册 PIN 和节点信息。', 'Run on Linux, OpenWrt, NAS, macOS, or FreeBSD. The installer asks for the PIN and node metadata in the terminal.') }}</p><div class="command-line"><code>{{ installCommand }}</code><button @click="copyCommand">{{ copied ? text('已复制', 'Copied') : text('复制', 'Copy') }}</button></div><small>{{ text('Windows：下载', 'Windows: download') }} <b>{{ windowsInstaller }}</b> {{ text('后在 PowerShell 中执行。', 'and run it in PowerShell.') }}</small></section>
      <section class="admin-panel event-panel"><div class="panel-label"><span>{{ text('最近告警', 'Recent alerts') }}</span><RouterLink to="/admin/alerts">{{ text('查看规则', 'View rules') }} →</RouterLink></div><div v-if="!events.length" class="panel-empty">{{ text('暂无告警事件', 'No alert events') }}</div><div v-for="event in events" :key="event.event_id" class="event-row"><i :class="[event.status, event.severity]" /><div><b>{{ event.node.name }}</b><span>{{ event.message }}</span></div><time>{{ dateTime(event.occurred_at) }}</time></div></section>
    </div>
    <section class="admin-panel"><div class="panel-label"><span>{{ text('节点列表', 'Nodes') }}</span><RouterLink to="/admin/nodes">{{ text('管理节点', 'Manage nodes') }} →</RouterLink></div><div class="mini-node-list"><div v-for="node in nodes.slice(0, 12)" :key="node.id"><i :class="node.last_seen_at && Date.now() - new Date(node.last_seen_at).getTime() < 90_000 ? 'online' : 'offline'" /><b>{{ node.name }}</b><span>{{ node.group || text('未分组', 'Ungrouped') }}</span><small>{{ node.os || text('未知系统', 'Unknown') }}</small></div></div></section>
  </section>
</template>

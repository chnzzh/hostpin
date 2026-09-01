<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { clone } from '../../clone'
import { dateTime } from '../../format'
import type { AdminNode, AlertEvent, AlertRule } from '../../types'
import { useLocale } from '../../i18n'

const rules = ref<AlertRule[]>([])
const events = ref<AlertEvent[]>([])
const nodes = ref<AdminNode[]>([])
const editing = ref<AlertRule | null>(null)
const groupScope = ref('')
const error = ref('')
const metrics = ['online', 'cpu', 'memory', 'swap', 'load1', 'load5', 'load15', 'disk', 'temperature', 'gpu', 'traffic_sum', 'traffic_up', 'traffic_down', 'probe_success', 'probe_latency', 'probe_loss']
const { text } = useLocale()
const metricNames: Record<string, string> = { online: '在线状态', cpu: 'CPU', memory: '内存', swap: 'Swap', load1: '1 分钟负载', load5: '5 分钟负载', load15: '15 分钟负载', disk: '磁盘', temperature: '温度', gpu: 'GPU', traffic_sum: '总流量', traffic_up: '上传流量', traffic_down: '下载流量', probe_success: '探测成功率', probe_latency: '探测延迟', probe_loss: '探测丢包' }
function metricLabel(metric: string) { return text(metricNames[metric] ?? metric, metric) }

function blank(): AlertRule { return { id: 0, name: '', metric: 'cpu', operator: '>', threshold: 90, recovery_threshold: 80, duration_seconds: 300, cooldown_seconds: 1800, severity: 'warning', scope: { groups: [], node_ids: [], excluded_node_ids: [] }, enabled: true } }
async function load() {
  const [ruleResponse, eventResponse, nodeResponse] = await Promise.all([api<{ data: AlertRule[] }>('/api/v1/admin/alerts/rules'), api<{ data: AlertEvent[] }>('/api/v1/admin/alerts/events?limit=40'), api<{ data: AdminNode[] }>('/api/v1/admin/nodes')])
  rules.value = unwrap(ruleResponse); events.value = unwrap(eventResponse); nodes.value = unwrap(nodeResponse)
}
function edit(rule: AlertRule) {
  editing.value = clone(rule)
  editing.value.scope.groups ??= []
  editing.value.scope.node_ids ??= []
  editing.value.scope.excluded_node_ids ??= []
  groupScope.value = rule.scope.groups?.join(', ') ?? ''
}
function create() { editing.value = blank(); groupScope.value = '' }
function scopeLabel(rule: AlertRule) { return rule.scope.groups?.length || rule.scope.node_ids?.length || rule.scope.excluded_node_ids?.length ? text('已筛选', 'Filtered') : text('全局', 'Global') }
async function save() {
  if (!editing.value) return
  error.value = ''
  editing.value.scope.groups = groupScope.value.split(',').map((value) => value.trim()).filter(Boolean)
  try {
    const path = editing.value.id ? `/api/v1/admin/alerts/rules/${editing.value.id}` : '/api/v1/admin/alerts/rules'
    const response = await api<{ data: AlertRule }>(path, { method: editing.value.id ? 'PUT' : 'POST', body: JSON.stringify(editing.value) })
    const saved = unwrap(response)
    const index = rules.value.findIndex((rule) => rule.id === saved.id)
    if (index >= 0) rules.value[index] = saved; else rules.value.push(saved)
    editing.value = null
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('无法保存告警规则。', 'Could not save rule.') }
}
async function remove(rule: AlertRule) { if (confirm(text(`删除规则“${rule.name}”？`, `Delete rule “${rule.name}”?`))) { await api(`/api/v1/admin/alerts/rules/${rule.id}`, { method: 'DELETE' }); rules.value = rules.value.filter((item) => item.id !== rule.id) } }
onMounted(load)
</script>

<template>
  <section class="admin-page">
    <header class="page-title"><div><span class="eyebrow">{{ text('告警策略', 'Alert policy') }}</span><h1>{{ text('告警规则', 'Alert rules') }}</h1><p>{{ text('设置触发阈值、持续时间、恢复阈值和冷却时间。', 'Configure thresholds, duration, recovery, and cooldown.') }}</p></div><button class="primary-action compact-action" @click="create">+ {{ text('新建规则', 'New rule') }}</button></header>
    <div class="rule-grid"><article v-for="rule in rules" :key="rule.id" class="rule-card"><header><span class="severity-mark" :class="rule.severity" /> <b>{{ rule.name }}</b><span class="tag" :class="{ muted: !rule.enabled }">{{ rule.enabled ? text('启用', 'Enabled') : text('暂停', 'Paused') }}</span></header><div class="rule-expression"><span>{{ metricLabel(rule.metric) }}</span><strong>{{ rule.operator }} {{ rule.threshold }}</strong><small>{{ text('恢复值', 'Recover') }} {{ rule.recovery_threshold }}</small></div><dl><div><dt>{{ text('持续', 'For') }}</dt><dd>{{ rule.duration_seconds }}s</dd></div><div><dt>{{ text('冷却', 'Cooldown') }}</dt><dd>{{ rule.cooldown_seconds }}s</dd></div><div><dt>{{ text('范围', 'Scope') }}</dt><dd>{{ scopeLabel(rule) }}</dd></div></dl><footer><button @click="edit(rule)">{{ text('编辑', 'Edit') }}</button><button class="danger" @click="remove(rule)">{{ text('删除', 'Delete') }}</button></footer></article></div>
    <section class="admin-panel event-history"><div class="panel-label"><span>{{ text('告警事件', 'Alert events') }}</span><b>{{ text(`${events.length} 条`, `${events.length} loaded`) }}</b></div><div class="event-table"><div v-for="event in events" :key="event.event_id" class="event-row"><i :class="[event.status, event.severity]" /><div><b>{{ event.node.name }} / {{ event.type }}</b><span>{{ event.message }}</span></div><strong :class="event.status">{{ event.status === 'firing' ? text('触发', 'Firing') : text('恢复', 'Resolved') }}</strong><time>{{ dateTime(event.occurred_at) }}</time></div><div v-if="!events.length" class="panel-empty">{{ text('暂无告警事件', 'No events recorded') }}</div></div></section>
    <div v-if="editing" class="modal-backdrop" @click.self="editing = null"><form class="drawer" @submit.prevent="save"><header><div><span>{{ text('告警规则', 'Alert rule') }}</span><h2>{{ editing.id ? text('编辑规则', 'Edit rule') : text('新建规则', 'New rule') }}</h2></div><button type="button" @click="editing = null">×</button></header><div class="drawer-body"><div class="form-grid"><label class="wide"><span>{{ text('规则名称', 'Rule name') }}</span><input v-model="editing.name" required /></label><label><span>{{ text('指标', 'Metric') }}</span><select v-model="editing.metric"><option v-for="metric in metrics" :key="metric" :value="metric">{{ metricLabel(metric) }}</option></select></label><label><span>{{ text('运算符', 'Operator') }}</span><select v-model="editing.operator"><option>&gt;</option><option>&gt;=</option><option>&lt;</option><option>&lt;=</option><option>==</option></select></label><label><span>{{ text('触发阈值', 'Threshold') }}</span><input v-model.number="editing.threshold" type="number" step="0.01" /></label><label><span>{{ text('恢复阈值', 'Recovery threshold') }}</span><input v-model.number="editing.recovery_threshold" type="number" step="0.01" /></label><label><span>{{ text('持续时间（秒）', 'Sustain for (seconds)') }}</span><input v-model.number="editing.duration_seconds" type="number" min="0" /></label><label><span>{{ text('冷却时间（秒）', 'Cooldown (seconds)') }}</span><input v-model.number="editing.cooldown_seconds" type="number" min="0" /></label><label><span>{{ text('级别', 'Severity') }}</span><select v-model="editing.severity"><option>info</option><option>warning</option><option>critical</option></select></label><label class="wide"><span>{{ text('分组范围（逗号分隔）', 'Group scope (comma separated)') }}</span><input v-model="groupScope" :placeholder="text('留空表示全局', 'Blank means global')" /></label><label class="wide"><span>{{ text('包含节点（可选）', 'Include nodes (optional)') }}</span><select v-model="editing.scope.node_ids" multiple><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }} / {{ node.group || text('未分组', 'Ungrouped') }}</option></select><small>{{ text('分组与包含节点会合并；两者都留空表示全局规则。', 'Groups and included nodes are combined; leave both blank for a global rule.') }}</small></label><label class="wide"><span>{{ text('排除节点', 'Exclude nodes') }}</span><select v-model="editing.scope.excluded_node_ids" multiple><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }} / {{ node.group || text('未分组', 'Ungrouped') }}</option></select><small>{{ text('排除项优先于全局、分组和包含节点。', 'Exclusions override global, group, and node inclusion.') }}</small></label></div><label class="switch-line"><input v-model="editing.enabled" type="checkbox" /><span>{{ text('启用规则', 'Rule enabled') }}</span></label><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="editing = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action">{{ text('保存规则', 'Save rule') }}</button></footer></form></div>
  </section>
</template>

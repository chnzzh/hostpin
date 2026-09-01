<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { clone } from '../../clone'
import { bytes, dateTime } from '../../format'
import { maximumTrafficLimitGiB, suggestedTrafficUnit, trafficBytesFrom, trafficCorrectionUnits, trafficLimitBytesFromGiB, trafficLimitInGiB, trafficValueInUnit } from '../../traffic'
import type { TrafficCorrectionUnit } from '../../traffic'
import type { AdminNode, AgentConfig, TrafficCorrectionStatus } from '../../types'
import { useLocale } from '../../i18n'

const nodes = ref<AdminNode[]>([])
const editing = ref<AdminNode | null>(null)
const agentConfig = ref<AgentConfig | null>(null)
const tags = ref('')
const expires = ref('')
const includeNICs = ref('')
const excludeNICs = ref('')
const includeMounts = ref('')
const trafficCorrection = ref<TrafficCorrectionStatus | null>(null)
const correctionRX = ref(0)
const correctionTX = ref(0)
const correctionUnit = ref<TrafficCorrectionUnit>('GiB')
const correctionUnits = Object.keys(trafficCorrectionUnits) as TrafficCorrectionUnit[]
const correctionBusy = ref(false)
const correctionNotice = ref('')
const trafficLimitGiB = ref(0)
const busy = ref(false)
const error = ref('')
const uninstallCopied = ref(false)
const uninstallCommand = computed(() => `curl -fsSL ${location.origin}/uninstall.sh | sh`)
const windowsUninstallCommand = computed(() => `irm ${location.origin}/uninstall.ps1 | iex`)
const { text } = useLocale()

async function load() { nodes.value = unwrap(await api<{ data: AdminNode[] }>('/api/v1/admin/nodes')) }
async function edit(node: AdminNode) {
  editing.value = clone(node); tags.value = node.tags.join(', '); expires.value = node.expires_at?.slice(0, 10) ?? ''; trafficLimitGiB.value = trafficLimitInGiB(node.traffic_limit)
  error.value = ''; correctionNotice.value = ''; agentConfig.value = null; trafficCorrection.value = null
  try {
    const [configResponse, correctionResponse] = await Promise.all([
      api<{ data: AgentConfig }>(`/api/v1/admin/nodes/${node.id}/agent-config`),
      api<{ data: TrafficCorrectionStatus }>(`/api/v1/admin/nodes/${node.id}/traffic-correction`),
    ])
    agentConfig.value = unwrap(configResponse)
    trafficCorrection.value = unwrap(correctionResponse)
    includeNICs.value = agentConfig.value.include_nics?.join(', ') ?? ''
    excludeNICs.value = agentConfig.value.exclude_nics?.join(', ') ?? ''
    includeMounts.value = agentConfig.value.include_mountpoints?.join(', ') ?? ''
    correctionUnit.value = suggestedTrafficUnit(trafficCorrection.value.rx_bytes, trafficCorrection.value.tx_bytes)
    syncCorrectionInputs()
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('无法加载探针配置。', 'Agent configuration could not be loaded.') }
}
function syncCorrectionInputs() {
  if (!trafficCorrection.value) return
  correctionRX.value = trafficValueInUnit(trafficCorrection.value.rx_bytes, correctionUnit.value)
  correctionTX.value = trafficValueInUnit(trafficCorrection.value.tx_bytes, correctionUnit.value)
}
function changeCorrectionUnit(event: Event) {
  const next = (event.target as HTMLSelectElement).value as TrafficCorrectionUnit
  const rxBytes = trafficBytesFrom(correctionRX.value, correctionUnit.value)
  const txBytes = trafficBytesFrom(correctionTX.value, correctionUnit.value)
  correctionUnit.value = next
  correctionRX.value = trafficValueInUnit(rxBytes, next)
  correctionTX.value = trafficValueInUnit(txBytes, next)
}
async function applyTrafficCorrection() {
  if (!editing.value || !trafficCorrection.value) return
  const rxBytes = trafficBytesFrom(correctionRX.value, correctionUnit.value)
  const txBytes = trafficBytesFrom(correctionTX.value, correctionUnit.value)
  if (!Number.isFinite(rxBytes) || !Number.isFinite(txBytes)) {
    error.value = text('请输入有效且不过大的非负流量值。', 'Enter valid non-negative traffic totals within the supported range.')
    return
  }
  correctionBusy.value = true; error.value = ''; correctionNotice.value = ''
  try {
    trafficCorrection.value = unwrap(await api<{ data: TrafficCorrectionStatus }>(`/api/v1/admin/nodes/${editing.value.id}/traffic-correction`, {
      method: 'PUT', body: JSON.stringify({ rx_bytes: rxBytes, tx_bytes: txBytes }),
    }))
    syncCorrectionInputs()
    correctionNotice.value = text('校正已应用，本周期后续流量会继续累加。', 'Correction applied; later traffic in this period will continue accumulating.')
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('流量校正失败。', 'Traffic correction failed.') }
  finally { correctionBusy.value = false }
}
async function clearTrafficCorrection() {
  if (!editing.value || !trafficCorrection.value) return
  correctionBusy.value = true; error.value = ''; correctionNotice.value = ''
  try {
    trafficCorrection.value = unwrap(await api<{ data: TrafficCorrectionStatus }>(`/api/v1/admin/nodes/${editing.value.id}/traffic-correction`, { method: 'DELETE' }))
    syncCorrectionInputs()
    correctionNotice.value = text('已恢复为 Agent 原始统计。', 'Restored to the original Agent totals.')
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('无法清除流量校正。', 'Traffic correction could not be cleared.') }
  finally { correctionBusy.value = false }
}
async function save() {
  if (!editing.value || !agentConfig.value) return
  busy.value = true; error.value = ''
  try {
    const trafficLimitBytes = trafficLimitBytesFromGiB(trafficLimitGiB.value)
    if (!Number.isFinite(trafficLimitBytes)) throw new Error(text('请输入有效且不过大的非负 GiB 流量额度。', 'Enter a valid non-negative GiB quota within the supported range.'))
    editing.value.traffic_limit = trafficLimitBytes
    editing.value.tags = tags.value.split(',').map((tag) => tag.trim()).filter(Boolean)
    editing.value.expires_at = expires.value ? new Date(`${expires.value}T00:00:00Z`).toISOString() : undefined
    agentConfig.value.include_nics = includeNICs.value.split(',').map((item) => item.trim()).filter(Boolean)
    agentConfig.value.exclude_nics = excludeNICs.value.split(',').map((item) => item.trim()).filter(Boolean)
    agentConfig.value.include_mountpoints = includeMounts.value.split(',').map((item) => item.trim()).filter(Boolean)
    const nodeID = editing.value.id
    const latencyEnabled = editing.value.latency_enabled
    await api(`/api/v1/admin/nodes/${nodeID}`, { method: 'PUT', body: JSON.stringify(editing.value) })
    await api(`/api/v1/admin/nodes/${nodeID}/agent-config`, { method: 'PUT', body: JSON.stringify(agentConfig.value) })
    const response = await api<{ data: AdminNode }>(`/api/v1/admin/nodes/${nodeID}/latency`, { method: 'PUT', body: JSON.stringify({ enabled: latencyEnabled }) })
    const updated = unwrap(response)
    nodes.value = nodes.value.map((node) => node.id === updated.id ? updated : node)
    editing.value = null
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('保存失败。', 'Update failed.') }
  finally { busy.value = false }
}
async function remove(node: AdminNode) {
  if (!confirm(text(`删除 ${node.name} 及其全部历史数据？此操作无法撤销。`, `Delete ${node.name} and all its history? This cannot be undone.`))) return
  await api(`/api/v1/admin/nodes/${node.id}`, { method: 'DELETE' })
  nodes.value = nodes.value.filter((item) => item.id !== node.id)
}
async function copyUninstallCommand() {
  await navigator.clipboard.writeText(uninstallCommand.value)
  uninstallCopied.value = true
  window.setTimeout(() => { uninstallCopied.value = false }, 1500)
}
onMounted(load)
</script>

<template>
  <section class="admin-page">
    <header class="page-title"><div><span class="eyebrow">{{ text('节点管理', 'Node management') }}</span><h1>{{ text('节点', 'Nodes') }}</h1><p>{{ text('编辑节点信息、可见性、计费和采集策略。', 'Edit metadata, visibility, billing, and collection policy.') }}</p></div><b class="page-count">{{ text(`${nodes.length} 个节点`, `${nodes.length} nodes`) }}</b></header>
    <div class="data-table-wrap"><table class="admin-table"><thead><tr><th>{{ text('节点', 'Node') }}</th><th>{{ text('分组 / 地区', 'Group / region') }}</th><th>{{ text('平台', 'Platform') }}</th><th>{{ text('最后上报', 'Last report') }}</th><th>{{ text('可见性', 'Visibility') }}</th><th /></tr></thead><tbody><tr v-for="node in nodes" :key="node.id"><td><b>{{ node.name }}</b><small>{{ node.id }}</small></td><td>{{ node.group || '—' }}<small>{{ node.region || node.country_code || '—' }}</small></td><td>{{ node.os || '—' }}<small>{{ node.arch }} · {{ node.cpu_cores }}C</small></td><td>{{ dateTime(node.last_seen_at) }}</td><td><span class="tag" :class="{ warning: node.hidden }">{{ node.hidden ? text('隐藏', 'Hidden') : text('公开', 'Public') }}</span> <span v-if="node.latency_enabled" class="tag">{{ text('延迟', 'Latency') }}</span></td><td class="row-actions"><button @click="edit(node)">{{ text('编辑', 'Edit') }}</button><button class="danger" @click="remove(node)">{{ text('删除', 'Delete') }}</button></td></tr></tbody></table></div>
    <section class="admin-panel enroll-panel agent-uninstall-panel"><div class="panel-label"><span>{{ text('一行卸载 Agent', 'One-line Agent uninstall') }}</span><b>{{ text('保留身份', 'SAFE') }}</b></div><p>{{ text('请在 Agent 所在设备上，以安装时相同的用户运行。默认保留节点身份，今后重装仍可继续使用原节点。', 'Run on the Agent host as the same user that installed it. The node identity is preserved for a later reinstall.') }}</p><div class="command-line"><code>{{ uninstallCommand }}</code><button @click="copyUninstallCommand">{{ uninstallCopied ? text('已复制', 'Copied') : text('复制', 'Copy') }}</button></div><small>{{ text('系统级安装请使用 sudo sh；Windows 管理员 PowerShell：', 'For a system install use sudo sh. Administrator PowerShell:') }} <b>{{ windowsUninstallCommand }}</b></small></section>
    <div v-if="editing" class="modal-backdrop" @click.self="editing = null"><form class="drawer" @submit.prevent="save"><header><div><span>{{ text('节点', 'Node') }} / {{ editing.id.slice(0, 8) }}</span><h2>{{ text(`编辑 ${editing.name}`, `Edit ${editing.name}`) }}</h2></div><button type="button" @click="editing = null">×</button></header><div class="drawer-body"><div class="form-grid"><label><span>{{ text('显示名称', 'Display name') }}</span><input v-model="editing.name" required maxlength="128" /></label><label><span>{{ text('分组', 'Group') }}</span><input v-model="editing.group" /></label><label><span>{{ text('地区', 'Region') }}</span><input v-model="editing.region" /></label><label><span>{{ text('国家代码', 'Country code') }}</span><input v-model="editing.country_code" maxlength="2" /></label><label class="wide"><span>{{ text('标签（逗号分隔）', 'Tags (comma separated)') }}</span><input v-model="tags" /></label><label class="wide"><span>{{ text('公开备注', 'Public remark') }}</span><textarea v-model="editing.public_remark" rows="3" /></label><label class="wide"><span>{{ text('私有备注', 'Private remark') }}</span><textarea v-model="editing.private_remark" rows="3" /></label><label><span>{{ text('价格', 'Price') }}</span><input v-model.number="editing.price" type="number" min="0" step="0.01" /></label><label><span>{{ text('币种', 'Currency') }}</span><input v-model="editing.currency" /></label><label><span>{{ text('计费周期（天）', 'Billing cycle (days)') }}</span><input v-model.number="editing.billing_cycle_days" type="number" min="1" max="3650" /></label><label><span>{{ text('到期日期', 'Expiry date') }}</span><input v-model="expires" type="date" /></label><label><span>{{ text('排序权重', 'Display weight') }}</span><input v-model.number="editing.weight" type="number" /></label><label><span>{{ text('每月流量额度', 'Monthly traffic quota') }} <small>GiB · {{ text('0 表示不限', '0 = unlimited') }}</small></span><input v-model.number="trafficLimitGiB" type="number" min="0" :max="maximumTrafficLimitGiB" step="0.01" :aria-label="text('每月流量额度（GiB）', 'Monthly traffic quota (GiB)')" /></label><label><span>{{ text('流量计算方式', 'Traffic mode') }}</span><select v-model="editing.traffic_limit_type"><option>sum</option><option>max</option><option>up</option><option>down</option></select></label><label><span>{{ text('每月重置日', 'Reset day') }}</span><input v-model.number="editing.traffic_reset_day" type="number" min="1" max="31" /></label></div><section v-if="trafficCorrection" class="traffic-correction-editor"><header><div><span>{{ text('流量校正', 'Traffic correction') }}</span><small>{{ text(`当前周期始于 ${trafficCorrection.period_start.slice(0, 10)} UTC`, `Current period began ${trafficCorrection.period_start.slice(0, 10)} UTC`) }}</small></div><b :class="{ active: trafficCorrection.active }">{{ trafficCorrection.active ? text('已校正', 'Adjusted') : text('原始统计', 'Raw totals') }}</b></header><div class="traffic-correction-readout"><div><span>{{ text('Agent 原始值', 'Raw Agent totals') }}</span><strong>↓ {{ bytes(trafficCorrection.raw_rx_bytes) }} · ↑ {{ bytes(trafficCorrection.raw_tx_bytes) }}</strong></div><div><span>{{ text('当前显示值', 'Displayed totals') }}</span><strong>↓ {{ bytes(trafficCorrection.rx_bytes) }} · ↑ {{ bytes(trafficCorrection.tx_bytes) }}</strong></div></div><div v-if="trafficCorrection.available" class="traffic-correction-fields"><label><span>{{ text('校正后下载', 'Corrected download') }}</span><input v-model.number="correctionRX" type="number" min="0" step="0.001" /></label><label><span>{{ text('校正后上传', 'Corrected upload') }}</span><input v-model.number="correctionTX" type="number" min="0" step="0.001" /></label><label><span>{{ text('单位', 'Unit') }}</span><select :value="correctionUnit" @change="changeCorrectionUnit"><option v-for="unit in correctionUnits" :key="unit" :value="unit">{{ unit }}</option></select></label></div><p v-else>{{ text('需要当前流量周期内的新上报后才能校正。', 'A fresh report from the current traffic period is required before correction.') }}</p><footer><small>{{ trafficCorrection.updated_at ? text(`上次调整：${dateTime(trafficCorrection.updated_at)}`, `Last adjusted: ${dateTime(trafficCorrection.updated_at)}`) : text('校正只影响当前周期，下个周期自动失效。', 'Corrections apply only to this period and expire automatically.') }}</small><div><button type="button" :disabled="correctionBusy || !trafficCorrection.active" @click="clearTrafficCorrection">{{ text('清除', 'Clear') }}</button><button type="button" class="secondary-action compact-action" :disabled="correctionBusy || !trafficCorrection.available" @click="applyTrafficCorrection">{{ correctionBusy ? text('处理中…', 'Applying…') : text('应用校正', 'Apply correction') }}</button></div></footer><div v-if="correctionNotice" class="traffic-correction-notice">{{ correctionNotice }}</div></section><div class="check-row"><label><input v-model="editing.hidden" type="checkbox" /><span>{{ text('对访客隐藏', 'Hide from public visitors') }}</span></label><label><input v-model="editing.auto_renewal" type="checkbox" /><span>{{ text('自动续费', 'Automatic renewal') }}</span></label><label><input v-model="editing.latency_enabled" type="checkbox" /><span>{{ text('同时作为延迟测量节点', 'Use as a latency measurement point') }}</span></label></div><template v-if="agentConfig"><div class="panel-label"><span>{{ text('探针采集', 'Agent collection') }}</span><b>V{{ agentConfig.config_version }}</b></div><div class="form-grid"><label><span>{{ text('实时采集间隔（秒）', 'Live interval (seconds)') }}</span><input v-model.number="agentConfig.collect_interval_seconds" type="number" min="1" max="300" /></label><label><span>{{ text('历史写入间隔（秒）', 'History interval (seconds)') }}</span><input v-model.number="agentConfig.persist_interval_seconds" type="number" min="1" max="86400" /></label><label class="wide"><span>{{ text('包含网卡（逗号分隔）', 'Include interfaces') }}</span><input v-model="includeNICs" /></label><label class="wide"><span>{{ text('排除网卡（逗号分隔）', 'Exclude interfaces') }}</span><input v-model="excludeNICs" /></label><label class="wide"><span>{{ text('包含挂载点（逗号分隔）', 'Include mountpoints') }}</span><input v-model="includeMounts" /></label></div><div class="check-row"><label><input v-model="agentConfig.enable_gpu" type="checkbox" /><span>{{ text('采集 GPU 指标', 'Collect GPU metrics') }}</span></label><label><input v-model="agentConfig.auto_update" type="checkbox" /><span>{{ text('启用签名自动更新', 'Signed automatic updates') }}</span></label></div></template><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="editing = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy || !agentConfig">{{ busy ? text('正在保存…', 'Saving…') : text('保存节点', 'Save node') }}</button></footer></form></div>
  </section>
</template>

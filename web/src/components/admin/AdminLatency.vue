<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { clone } from '../../clone'
import { dateTime, relativeTime } from '../../format'
import type { AdminNode, AgentConfig, LatencyResult, ProbeTask } from '../../types'
import { useLocale } from '../../i18n'

interface LatencyAdminResponse {
  nodes: AdminNode[]
  targets: ProbeTask[]
  monitored_nodes: AdminNode[]
  latest: LatencyResult[]
  offline_after_ms: number
  install_command: string
  windows_installer: string
  windows_install_command: string
}

const data = ref<LatencyAdminResponse>({ nodes: [], targets: [], monitored_nodes: [], latest: [], offline_after_ms: 90_000, install_command: '', windows_installer: '', windows_install_command: '' })
const editingTarget = ref<ProbeTask | null>(null)
const editingNode = ref<AdminNode | null>(null)
const agentConfig = ref<AgentConfig | null>(null)
const tagsText = ref('')
const error = ref('')
const copied = ref('')
const busy = ref(false)
const { text } = useLocale()

const latestByProbe = computed(() => {
  const result = new Map<string, LatencyResult[]>()
  for (const item of data.value.latest) result.set(item.probe_node_id, [...(result.get(item.probe_node_id) ?? []), item])
  return result
})

function blankTarget(): ProbeTask {
  const node = data.value.monitored_nodes[0]
  return {
    id: 0, name: node?.name ?? '', type: 'icmp', target: node?.ipv4 || node?.ipv6 || '',
    target_node_id: node?.id ?? '', interval_seconds: 30, timeout_seconds: 2,
    expected_status: 0, expected_value: '', node_ids: [], purpose: 'latency', run_on: 'probe',
    public: true, samples: 3, enabled: true,
  }
}

function monitorName(id?: string) {
  return data.value.monitored_nodes.find((item) => item.id === id)?.name ?? id ?? '—'
}

function selectTargetNode() {
  if (!editingTarget.value) return
  const node = data.value.monitored_nodes.find((item) => item.id === editingTarget.value?.target_node_id)
  if (!node) return
  editingTarget.value.name = node.name
  editingTarget.value.target = editingTarget.value.type === 'icmp'
    ? (node.ipv4 || node.ipv6 || '')
    : (node.ipv4 ? `${node.ipv4}:443` : node.ipv6 ? `[${node.ipv6}]:443` : '')
}

function changeTargetType() {
  selectTargetNode()
}

async function load() {
  data.value = await api<LatencyAdminResponse>('/api/v1/admin/latency')
}

async function saveTarget() {
  if (!editingTarget.value) return
  busy.value = true
  error.value = ''
  try {
    const path = editingTarget.value.id ? `/api/v1/admin/latency/targets/${editingTarget.value.id}` : '/api/v1/admin/latency/targets'
    await api(path, { method: editingTarget.value.id ? 'PUT' : 'POST', body: JSON.stringify(editingTarget.value) })
    editingTarget.value = null
    await load()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法保存延迟目标。', 'Could not save latency target.')
  } finally {
    busy.value = false
  }
}

async function removeTarget(task: ProbeTask) {
  if (!confirm(text(`删除延迟目标“${monitorName(task.target_node_id)}”？`, `Delete latency target “${monitorName(task.target_node_id)}”?`))) return
  await api(`/api/v1/admin/latency/targets/${task.id}`, { method: 'DELETE' })
  await load()
}

async function editNode(node: AdminNode) {
  editingNode.value = clone(node)
  tagsText.value = node.tags.join(', ')
  agentConfig.value = unwrap(await api<{ data: AgentConfig }>(`/api/v1/admin/nodes/${node.id}/agent-config`))
}

async function saveNode() {
  if (!editingNode.value || !agentConfig.value) return
  busy.value = true
  error.value = ''
  try {
    editingNode.value.tags = tagsText.value.split(',').map((item) => item.trim()).filter(Boolean)
    await Promise.all([
      api(`/api/v1/admin/latency/nodes/${editingNode.value.id}`, { method: 'PUT', body: JSON.stringify(editingNode.value) }),
      api(`/api/v1/admin/nodes/${editingNode.value.id}/agent-config`, { method: 'PUT', body: JSON.stringify(agentConfig.value) }),
    ])
    editingNode.value = null
    agentConfig.value = null
    await load()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法保存测量节点。', 'Could not save measurement node.')
  } finally {
    busy.value = false
  }
}

async function removeNode(node: AdminNode) {
  const shared = node.role === 'monitor'
  const prompt = shared
    ? text(`停用“${node.name}”的延迟测量功能？主机监控和已有历史不会被删除。`, `Disable latency measurement for “${node.name}”? Monitoring and existing history will be kept.`)
    : text(`删除测量节点“${node.name}”及其延迟历史？`, `Delete measurement node “${node.name}” and its latency history?`)
  if (!confirm(prompt)) return
  await api(`/api/v1/admin/latency/nodes/${node.id}`, { method: 'DELETE' })
  await load()
}

async function copy(value: string, label: string) {
  await navigator.clipboard.writeText(value)
  copied.value = label
  window.setTimeout(() => { copied.value = '' }, 1500)
}

function successRate(nodeID: string) {
  const values = latestByProbe.value.get(nodeID) ?? []
  return values.length ? values.filter((item) => item.success).length / values.length * 100 : 0
}

function isOnline(node: AdminNode) {
  return Boolean(node.last_seen_at && Date.now() - new Date(node.last_seen_at).getTime() <= data.value.offline_after_ms)
}

onMounted(load)
</script>

<template>
  <section class="admin-page latency-admin-page">
    <header class="page-title"><div><span class="eyebrow">{{ text('分布式延迟测量', 'Distributed latency measurement') }}</span><h1>{{ text('延迟节点', 'Latency nodes') }}</h1><p>{{ text('在公网地区、办公室、家庭网络或 OpenWrt 路由器部署仅出站的测量节点，无需公网 IP 或入站端口。', 'Deploy outbound-only measurement points in public regions, offices, homes, or OpenWrt routers. No public IP or inbound port is required.') }}</p></div><RouterLink class="primary-action compact-action" to="/latency">{{ text('查看延迟矩阵', 'Open matrix') }} ↗</RouterLink></header>

    <section class="probe-install-band">
      <div><span>01 / UNIX · OPENWRT · NAS</span><p>{{ text('下载 Hostpin Agent，并以延迟测量模式注册。', 'Download the Hostpin Agent and enroll it in latency-only mode.') }}</p><div class="command-line"><code>{{ data.install_command }}</code><button @click="copy(data.install_command, 'unix')">{{ copied === 'unix' ? text('已复制', 'Copied') : text('复制', 'Copy') }}</button></div></div>
      <div><span>02 / WINDOWS</span><p>{{ text('下载并检查安装器，然后使用 ProbeNode 参数运行。', 'Download and inspect the installer, then run it with the ProbeNode switch.') }}</p><div class="command-line"><code>{{ data.windows_install_command }}</code><button @click="copy(data.windows_install_command, 'windows')">{{ copied === 'windows' ? text('已复制', 'Copied') : text('复制', 'Copy') }}</button></div></div>
      <aside><b>{{ text('仅出站连接', 'Outbound only') }}</b><span>{{ text('测量节点', 'Probe Node') }} → HTTPS/WSS → Hostpin</span><small>{{ text('支持 NAT / CGNAT', 'NAT / CGNAT safe') }}</small></aside>
    </section>

    <div class="section-heading compact-heading admin-section-heading"><div><span class="section-number">01</span><h2>{{ text('测量节点', 'Measurement points') }}</h2><p>{{ text('普通监控节点可在“节点”编辑页开启延迟测量；纯测量节点仍可使用上方命令安装。', 'Enable latency measurement while editing a regular node, or install a lightweight probe-only node with the command above.') }}</p></div><b class="page-count">{{ text(`${data.nodes.length} 个`, `${data.nodes.length} probes`) }}</b></div>
    <div class="data-table-wrap"><table class="admin-table latency-node-table"><thead><tr><th>{{ text('测量点', 'Measurement point') }}</th><th>{{ text('平台', 'Platform') }}</th><th>{{ text('最后上报', 'Last report') }}</th><th>{{ text('链路健康', 'Path health') }}</th><th>{{ text('可见性', 'Visibility') }}</th><th /></tr></thead><tbody><tr v-for="node in data.nodes" :key="node.id"><td><b><span class="status-dot" :class="isOnline(node) ? 'online' : 'offline'" />{{ node.name }}</b><small>{{ node.region || node.country_code || text('未设置地区', 'No location') }} · {{ node.id }}</small></td><td>{{ node.os || '—' }}<small>{{ node.arch || '—' }} · {{ node.role === 'monitor' ? text('监控兼任', 'Monitor + latency') : text('纯测量', 'Probe only') }}</small></td><td>{{ relativeTime(node.last_seen_at) }}<small>{{ dateTime(node.last_seen_at) }}</small></td><td><strong>{{ successRate(node.id).toFixed(0) }}%</strong><small>{{ text(`${latestByProbe.get(node.id)?.length ?? 0} 条当前链路`, `${latestByProbe.get(node.id)?.length ?? 0} current paths`) }}</small></td><td><span class="tag" :class="{ warning: node.hidden }">{{ node.hidden ? text('私有', 'Private') : text('公开', 'Public') }}</span></td><td class="row-actions"><button @click="editNode(node)">{{ text('编辑', 'Edit') }}</button><button :class="{ danger: node.role !== 'monitor' }" @click="removeNode(node)">{{ node.role === 'monitor' ? text('停用延迟', 'Disable latency') : text('删除', 'Delete') }}</button></td></tr><tr v-if="!data.nodes.length"><td colspan="6"><div class="table-empty">{{ text('请在节点管理中开启延迟测量，或运行上方命令安装纯测量节点。', 'Enable latency measurement for a monitored node, or install a probe-only node with the command above.') }}</div></td></tr></tbody></table></div>

    <div class="section-heading compact-heading admin-section-heading"><div><span class="section-number">02</span><h2>{{ text('服务器目标', 'Server targets') }}</h2><p>{{ text('目标会自动下发到所有已连接的测量节点。', 'Targets are distributed automatically to every connected measurement node.') }}</p></div><button class="primary-action compact-action" :disabled="!data.monitored_nodes.length" @click="editingTarget = blankTarget()">+ {{ text('添加目标', 'Add target') }}</button></div>
    <div class="data-table-wrap"><table class="admin-table"><thead><tr><th>{{ text('服务器', 'Server') }}</th><th>{{ text('方式 / 地址', 'Method / address') }}</th><th>{{ text('频率', 'Cadence') }}</th><th>{{ text('公开显示', 'Public matrix') }}</th><th>{{ text('状态', 'State') }}</th><th /></tr></thead><tbody><tr v-for="task in data.targets" :key="task.id"><td><b>{{ monitorName(task.target_node_id) }}</b><small>{{ text('任务', 'Task') }} #{{ task.id }}</small></td><td><span class="tag">{{ task.type.toUpperCase() }}</span> <code>{{ task.target }}</code></td><td>{{ text(`每轮 ${task.samples} 次 / ${task.interval_seconds}s`, `${task.samples} samples / ${task.interval_seconds}s`) }}<small>{{ text(`每次超时 ${task.timeout_seconds}s`, `${task.timeout_seconds}s timeout each`) }}</small></td><td><span class="tag" :class="{ warning: !task.public }">{{ task.public ? text('可见', 'Visible') : text('仅管理员', 'Admin only') }}</span></td><td>{{ task.enabled ? text('启用', 'Active') : text('暂停', 'Paused') }}</td><td class="row-actions"><button @click="editingTarget = clone(task)">{{ text('编辑', 'Edit') }}</button><button class="danger" @click="removeTarget(task)">{{ text('删除', 'Delete') }}</button></td></tr><tr v-if="!data.targets.length"><td colspan="6"><div class="table-empty">{{ text('添加一个受监控服务器以开始生成延迟矩阵。', 'Add a monitored server to start building the latency matrix.') }}</div></td></tr></tbody></table></div>

    <div v-if="editingTarget" class="modal-backdrop" @click.self="editingTarget = null"><form class="drawer" @submit.prevent="saveTarget"><header><div><span>{{ text('延迟目标', 'Latency target') }}</span><h2>{{ editingTarget.id ? text('编辑目标', 'Edit target') : text('新建目标', 'New target') }}</h2></div><button type="button" @click="editingTarget = null">×</button></header><div class="drawer-body"><div class="form-grid"><label class="wide"><span>{{ text('受监控服务器', 'Monitored server') }}</span><select v-model="editingTarget.target_node_id" required @change="selectTargetNode"><option v-for="node in data.monitored_nodes" :key="node.id" :value="node.id">{{ node.name }} · {{ node.region || node.ipv4 || text('无地区', 'No region') }}</option></select></label><label><span>{{ text('测量方式', 'Measurement type') }}</span><select v-model="editingTarget.type" @change="changeTargetType"><option value="icmp">ICMP Ping</option><option value="tcp">TCP Connect</option></select></label><label><span>{{ text('每轮次数', 'Samples per round') }}</span><input v-model.number="editingTarget.samples" type="number" min="1" max="10" /></label><label class="wide"><span>{{ text('目标地址', 'Target address') }}</span><input v-model="editingTarget.target" required :placeholder="editingTarget.type === 'tcp' ? 'server.example.com:443' : 'server.example.com'" /><small>{{ text('本地路由器视角可填写局域网地址；公网测量节点请填写公网域名或 IP。', 'Use a LAN address for a private router perspective, or a public hostname/IP for global probes.') }}</small></label><label><span>{{ text('间隔（秒）', 'Interval (seconds)') }}</span><input v-model.number="editingTarget.interval_seconds" type="number" min="5" max="86400" /></label><label><span>{{ text('单次超时（秒）', 'Timeout per attempt') }}</span><input v-model.number="editingTarget.timeout_seconds" type="number" min="1" max="60" /></label></div><div class="check-row"><label><input v-model="editingTarget.public" type="checkbox" /><span>{{ text('在公开延迟页显示', 'Show on public latency page') }}</span></label><label><input v-model="editingTarget.enabled" type="checkbox" /><span>{{ text('启用目标', 'Target enabled') }}</span></label></div><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="editingTarget = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy">{{ busy ? text('正在保存…', 'Saving…') : text('保存目标', 'Save target') }}</button></footer></form></div>

    <div v-if="editingNode" class="modal-backdrop" @click.self="editingNode = null"><form class="drawer" @submit.prevent="saveNode"><header><div><span>{{ text('测量节点', 'Measurement node') }} / {{ editingNode.id.slice(0, 8) }}</span><h2>{{ text(`编辑 ${editingNode.name}`, `Edit ${editingNode.name}`) }}</h2></div><button type="button" @click="editingNode = null">×</button></header><div class="drawer-body"><div class="form-grid"><label><span>{{ text('显示名称', 'Display name') }}</span><input v-model="editingNode.name" required maxlength="128" /></label><label><span>{{ text('地区 / 运营商', 'Region / ISP') }}</span><input v-model="editingNode.region" maxlength="128" /></label><label><span>{{ text('国家代码', 'Country code') }}</span><input v-model="editingNode.country_code" maxlength="2" /></label><label class="wide"><span>{{ text('标签（逗号分隔）', 'Tags (comma separated)') }}</span><input v-model="tagsText" /></label><label class="wide"><span>{{ text('公开备注', 'Public remark') }}</span><textarea v-model="editingNode.public_remark" rows="2" /></label><label class="wide"><span>{{ text('私有备注', 'Private remark') }}</span><textarea v-model="editingNode.private_remark" rows="2" /></label></div><template v-if="agentConfig"><div class="panel-label"><span>{{ text('测量配置', 'Probe runtime') }}</span><b>V{{ agentConfig.config_version }}</b></div><div class="form-grid"><label><span>{{ text('心跳间隔（秒）', 'Heartbeat (seconds)') }}</span><input v-model.number="agentConfig.collect_interval_seconds" type="number" min="1" max="300" /></label><label><span>{{ text('最大并发数', 'Max concurrency') }}</span><input v-model.number="agentConfig.probe_concurrency" type="number" min="1" max="32" /></label></div></template><div class="check-row"><label><input v-model="editingNode.hidden" type="checkbox" /><span>{{ text('设为私有测量点', 'Keep this measurement point private') }}</span></label><label v-if="agentConfig"><input v-model="agentConfig.auto_update" type="checkbox" /><span>{{ text('启用签名自动更新', 'Signed automatic updates') }}</span></label></div><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="editingNode = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy || !agentConfig">{{ busy ? text('正在保存…', 'Saving…') : text('保存节点', 'Save node') }}</button></footer></form></div>
  </section>
</template>

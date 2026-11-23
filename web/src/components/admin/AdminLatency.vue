<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { APIError, api, unwrap } from '../../api'
import { clone } from '../../clone'
import { dateTime, relativeTime } from '../../format'
import type { AdminNode, AgentConfig, LatencyResult, ProbeTask } from '../../types'
import { useLocale } from '../../i18n'

type LatencyProbeTask = ProbeTask & { ip_version?: 0 | 4 | 6 }
type BatchStack = 'both' | '4' | '6'
type BatchProtocol = 'both' | 'icmp' | 'tcp'
type TargetProtocol = 'icmp' | 'tcp'

interface LatencyAdminResponse {
  nodes: AdminNode[]
  targets: LatencyProbeTask[]
  monitored_nodes: AdminNode[]
  latest: LatencyResult[]
  offline_after_ms: number
  install_command: string
  windows_installer: string
  windows_install_command: string
}

interface TargetCombination {
  ipVersion: 4 | 6
  proto: TargetProtocol
}

const data = ref<LatencyAdminResponse>({ nodes: [], targets: [], monitored_nodes: [], latest: [], offline_after_ms: 90_000, install_command: '', windows_installer: '', windows_install_command: '' })
const editingTarget = ref<LatencyProbeTask | null>(null)
const batchStack = ref<BatchStack>('4')
const batchProtocol = ref<BatchProtocol>('icmp')
const batchIPv4 = ref('')
const batchIPv6 = ref('')
const batchPort = ref<number>(443)
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

const plannedCombos = computed<TargetCombination[]>(() => {
  const stacks: (4 | 6)[] = batchStack.value === 'both' ? [4, 6] : [batchStack.value === '6' ? 6 : 4]
  const protos: TargetProtocol[] = batchProtocol.value === 'both' ? ['icmp', 'tcp'] : [batchProtocol.value]
  const combos: TargetCombination[] = []
  for (const ipVersion of stacks) {
    for (const proto of protos) combos.push({ ipVersion, proto })
  }
  return combos
})

const editTargetPlaceholder = computed(() => {
  if (!editingTarget.value) return 'server.example.com'
  const stack = inferTaskIPVersion(editingTarget.value)
  if (editingTarget.value.type === 'tcp') return stack === 6 ? '[2001:db8::1]:443' : 'server.example.com:443'
  return stack === 6 ? '2001:db8::1' : 'server.example.com'
})

function inferTaskIPVersion(task: Pick<LatencyProbeTask, 'ip_version' | 'target'>): 4 | 6 {
  if (task.ip_version === 6 || task.ip_version === 4) return task.ip_version
  const target = task.target.trim()
  if (target.startsWith('[')) return 6
  const colonCount = target.split(':').length - 1
  if (colonCount >= 2) return 6
  return 4
}

function ipVersionLabel(version: 4 | 6): 'IPv4' | 'IPv6' {
  return version === 6 ? 'IPv6' : 'IPv4'
}

function isIPv4Literal(host: string): boolean {
  const parts = host.split('.')
  return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)
}

function isValidPortNumber(port: number): boolean {
  return Number.isInteger(port) && port >= 1 && port <= 65535
}

function parseExplicitHostPort(raw: string): { host: string; port?: string } {
  const trimmed = raw.trim()
  const bracketedWithPort = /^\[([^\]]+)\]:(.*)$/.exec(trimmed)
  if (bracketedWithPort) {
    return { host: bracketedWithPort[1]!.trim(), port: bracketedWithPort[2]!.trim() }
  }
  if (trimmed.startsWith('[') && trimmed.endsWith(']')) {
    return { host: trimmed.slice(1, -1).trim() }
  }
  const colonCount = trimmed.split(':').length - 1
  if (colonCount === 1) {
    const idx = trimmed.indexOf(':')
    return { host: trimmed.slice(0, idx).trim(), port: trimmed.slice(idx + 1).trim() }
  }
  return { host: trimmed }
}

function formatTargetValue(host: string, proto: TargetProtocol, port = 443): string {
  const trimmed = host.trim()
  if (!trimmed) return ''
  if (proto === 'icmp') {
    const parsed = parseExplicitHostPort(trimmed)
    return parsed.host
  }
  const parsed = parseExplicitHostPort(trimmed)
  if (parsed.port !== undefined) return trimmed
  return parsed.host.includes(':') ? `[${parsed.host}]:${port}` : `${parsed.host}:${port}`
}

function defaultBatchStackForNode(node?: AdminNode): BatchStack {
  if (node?.ipv4?.trim() && node?.ipv6?.trim()) return 'both'
  if (node?.ipv6?.trim()) return '6'
  return '4'
}

function findMonitoredNode(nodeID?: string): AdminNode | undefined {
  return data.value.monitored_nodes.find((item) => item.id === nodeID)
}

function blankTarget(): LatencyProbeTask {
  const node = data.value.monitored_nodes[0]
  const defaultStack = defaultBatchStackForNode(node)
  const initialVersion: 4 | 6 = defaultStack === '6' ? 6 : 4
  const ipv4 = node?.ipv4?.trim() ?? ''
  const ipv6 = node?.ipv6?.trim() ?? ''
  return {
    id: 0,
    name: node?.name ?? '',
    type: 'icmp',
    ip_version: initialVersion,
    target: initialVersion === 6 ? ipv6 : ipv4,
    target_node_id: node?.id ?? '',
    interval_seconds: 30,
    timeout_seconds: 2,
    expected_status: 0,
    expected_value: '',
    node_ids: [],
    purpose: 'latency',
    run_on: 'probe',
    public: true,
    samples: 3,
    enabled: true,
  }
}

function openNewTarget() {
  error.value = ''
  const target = blankTarget()
  const node = findMonitoredNode(target.target_node_id)
  editingTarget.value = target
  batchStack.value = defaultBatchStackForNode(node)
  batchProtocol.value = 'icmp'
  batchIPv4.value = node?.ipv4?.trim() ?? ''
  batchIPv6.value = node?.ipv6?.trim() ?? ''
  batchPort.value = 443
}

function openEditTarget(task: LatencyProbeTask) {
  error.value = ''
  const copy = clone(task)
  copy.ip_version = inferTaskIPVersion(copy)
  editingTarget.value = copy
}

function closeTargetDrawer() {
  editingTarget.value = null
  error.value = ''
}

function monitorName(id?: string) {
  return findMonitoredNode(id)?.name ?? id ?? '—'
}

function suggestEditTarget(preserveCurrentHost: boolean) {
  if (!editingTarget.value) return
  const node = findMonitoredNode(editingTarget.value.target_node_id)
  if (!node) return
  const stack = inferTaskIPVersion(editingTarget.value)
  const proto: TargetProtocol = editingTarget.value.type === 'tcp' ? 'tcp' : 'icmp'
  const nodeHost = stack === 6 ? (node.ipv6 ?? '') : (node.ipv4 ?? '')
  let host = nodeHost
  if (preserveCurrentHost && editingTarget.value.target.trim()) {
    const parsed = parseExplicitHostPort(editingTarget.value.target)
    if (parsed.host) host = parsed.host
  }
  editingTarget.value.name = node.name
  editingTarget.value.target = host ? formatTargetValue(host, proto, 443) : ''
}

function selectTargetNode() {
  if (!editingTarget.value) return
  const node = findMonitoredNode(editingTarget.value.target_node_id)
  if (!node) return
  editingTarget.value.name = node.name
  if (editingTarget.value.id === 0) {
    batchStack.value = defaultBatchStackForNode(node)
    batchIPv4.value = node.ipv4?.trim() ?? ''
    batchIPv6.value = node.ipv6?.trim() ?? ''
    return
  }
  suggestEditTarget(false)
}

function changeTargetStack() {
  suggestEditTarget(false)
}

function changeTargetType() {
  suggestEditTarget(true)
}

function isTargetExistError(reason: unknown): boolean {
  if (reason instanceof APIError) {
    return reason.status === 409 || reason.code === 'latency_target_exists'
  }
  return typeof reason === 'object' && reason !== null && 'status' in reason && (reason as { status?: number }).status === 409
}

function validateHostForVersion(rawAddress: string, ipVersion: 4 | 6, proto: TargetProtocol, tcpPort: number): { target?: string; message?: string } {
  const trimmed = rawAddress.trim()
  const stackLabel = ipVersionLabel(ipVersion)
  if (!trimmed) {
    return { message: text(`请填写 ${stackLabel} 目标地址。`, `Enter a target address for ${stackLabel}.`) }
  }
  if (/\s|\//.test(trimmed)) {
    return { message: text(`${stackLabel} 目标地址不能包含空格或斜杠。`, `${stackLabel} target address must not contain spaces or slashes.`) }
  }
  const parsed = parseExplicitHostPort(trimmed)
  if (!parsed.host || parsed.host.includes('[') || parsed.host.includes(']')) {
    return { message: text(`${stackLabel} 目标地址格式无效。`, `Invalid ${stackLabel} target address format.`) }
  }
  if (ipVersion === 4 && parsed.host.includes(':')) {
    return { message: text('IPv4 目标地址不能填写 IPv6 字面量。', 'IPv4 target must not use an IPv6 literal address.') }
  }
  if (ipVersion === 6 && isIPv4Literal(parsed.host)) {
    return { message: text('IPv6 目标地址不能填写 IPv4 字面量。', 'IPv6 target must not use an IPv4 literal address.') }
  }
  if (proto === 'icmp') {
    if (parsed.port !== undefined) {
      return { message: text('ICMP 目标不能包含端口；请填写纯域名或 IP 地址。', 'ICMP targets must not include a port; enter only a hostname or IP address.') }
    }
    return { target: parsed.host }
  }
  if (parsed.port !== undefined) {
    if (!/^\d+$/.test(parsed.port) || !isValidPortNumber(Number(parsed.port))) {
      return { message: text(`${stackLabel} TCP 端口必须在 1 到 65535 之间。`, `${stackLabel} TCP port must be between 1 and 65535.`) }
    }
    return { target: parsed.host.includes(':') ? `[${parsed.host}]:${parsed.port}` : `${parsed.host}:${parsed.port}` }
  }
  if (!isValidPortNumber(tcpPort)) {
    return { message: text('TCP 端口必须在 1 到 65535 之间。', 'TCP port must be between 1 and 65535.') }
  }
  return { target: parsed.host.includes(':') ? `[${parsed.host}]:${tcpPort}` : `${parsed.host}:${tcpPort}` }
}

function previewComboTarget(combo: TargetCombination): string {
  const raw = combo.ipVersion === 6 ? batchIPv6.value.trim() : batchIPv4.value.trim()
  if (!raw) return '—'
  const port = isValidPortNumber(batchPort.value) ? batchPort.value : 443
  return formatTargetValue(raw, combo.proto, port) || '—'
}

function comboAlreadyConfigured(combo: TargetCombination): boolean {
  const targetNodeID = editingTarget.value?.target_node_id
  if (!targetNodeID) return false
  return data.value.targets.some(
    (task) => task.target_node_id === targetNodeID && inferTaskIPVersion(task) === combo.ipVersion && task.type === combo.proto,
  )
}

async function load() {
  data.value = await api<LatencyAdminResponse>('/api/v1/admin/latency')
}

async function saveTarget() {
  if (!editingTarget.value) return
  error.value = ''

  if (editingTarget.value.id > 0) {
    const ipVersion = inferTaskIPVersion(editingTarget.value)
    const proto: TargetProtocol = editingTarget.value.type === 'tcp' ? 'tcp' : 'icmp'
    let validated: { target?: string; message?: string }
    if (proto === 'tcp') {
      const parsed = parseExplicitHostPort(editingTarget.value.target)
      if (parsed.port === undefined) {
        error.value = text('TCP 目标需要包含端口（例如 host:443 或 [2001:db8::1]:443）。', 'TCP targets require a port (e.g. host:443 or [2001:db8::1]:443).')
        return
      }
      validated = validateHostForVersion(editingTarget.value.target, ipVersion, 'tcp', Number(parsed.port))
    } else {
      validated = validateHostForVersion(editingTarget.value.target, ipVersion, 'icmp', 443)
    }
    if (validated.message || !validated.target) {
      error.value = validated.message ?? text('目标地址无效。', 'Invalid target address.')
      return
    }

    busy.value = true
    try {
      const payload: LatencyProbeTask = {
        ...editingTarget.value,
        name: monitorName(editingTarget.value.target_node_id),
        ip_version: ipVersion,
        type: proto,
        target: validated.target,
      }
      await api(`/api/v1/admin/latency/targets/${editingTarget.value.id}`, {
        method: 'PUT',
        body: JSON.stringify(payload),
      })
      closeTargetDrawer()
      await load()
    } catch (reason) {
      error.value = reason instanceof Error ? reason.message : text('无法保存延迟目标。', 'Could not save latency target.')
    } finally {
      busy.value = false
    }
    return
  }

  const combos = plannedCombos.value
  const payloads: LatencyProbeTask[] = []
  const nodeName = monitorName(editingTarget.value.target_node_id)

  for (const combo of combos) {
    const rawAddress = combo.ipVersion === 6 ? batchIPv6.value : batchIPv4.value
    const validated = validateHostForVersion(rawAddress, combo.ipVersion, combo.proto, batchPort.value)
    if (validated.message || !validated.target) {
      error.value = validated.message ?? text('目标地址无效。', 'Invalid target address.')
      return
    }
    payloads.push({
      ...editingTarget.value,
      id: 0,
      name: nodeName,
      type: combo.proto,
      ip_version: combo.ipVersion,
      target: validated.target,
    })
  }

  busy.value = true
  let created = 0
  try {
    for (const payload of payloads) {
      try {
        await api('/api/v1/admin/latency/targets', {
          method: 'POST',
          body: JSON.stringify(payload),
        })
        created++
      } catch (reason) {
        if (payloads.length > 1 && isTargetExistError(reason)) {
          continue
        }
        throw reason
      }
    }
    closeTargetDrawer()
    await load()
  } catch (reason) {
    if (created > 0) {
      try {
        await load()
      } catch {
        // Keep the primary save error visible in the drawer.
      }
    }
    error.value = reason instanceof Error ? reason.message : text('无法保存延迟目标。', 'Could not save latency target.')
  } finally {
    busy.value = false
  }
}

function taskSummaryLabel(task: LatencyProbeTask): string {
  const stack = ipVersionLabel(inferTaskIPVersion(task))
  const proto = task.type.toUpperCase()
  return `${monitorName(task.target_node_id)} (${stack} · ${proto})`
}

async function removeTarget(task: LatencyProbeTask) {
  const label = taskSummaryLabel(task)
  if (!confirm(text(`删除延迟目标“${label}”？`, `Delete latency target “${label}”?`))) return
  await api(`/api/v1/admin/latency/targets/${task.id}`, { method: 'DELETE' })
  await load()
}

async function editNode(node: AdminNode) {
  error.value = ''
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

    <div class="section-heading compact-heading admin-section-heading"><div><span class="section-number">02</span><h2>{{ text('服务器目标', 'Server targets') }}</h2><p>{{ text('目标会自动下发到所有已连接的测量节点，每台服务器可同时配置 IPv4/IPv6 与 ICMP/TCP 目标。', 'Targets are distributed automatically to every connected measurement node, with up to IPv4/IPv6 × ICMP/TCP per server.') }}</p></div><button class="primary-action compact-action" :disabled="!data.monitored_nodes.length" @click="openNewTarget">+ {{ text('添加目标', 'Add target') }}</button></div>
    <div class="data-table-wrap"><table class="admin-table"><thead><tr><th>{{ text('服务器', 'Server') }}</th><th>{{ text('协议栈 / 方式 / 地址', 'Stack / method / address') }}</th><th>{{ text('频率', 'Cadence') }}</th><th>{{ text('公开显示', 'Public matrix') }}</th><th>{{ text('状态', 'State') }}</th><th /></tr></thead><tbody><tr v-for="task in data.targets" :key="task.id"><td><b>{{ monitorName(task.target_node_id) }}</b><small>{{ text('任务', 'Task') }} #{{ task.id }}</small></td><td><div class="target-method-cell"><span class="tag ip-tag" :class="inferTaskIPVersion(task) === 6 ? 'v6' : 'v4'">{{ ipVersionLabel(inferTaskIPVersion(task)) }}</span><span class="tag">{{ task.type.toUpperCase() }}</span> <code>{{ task.target }}</code></div></td><td>{{ text(`每轮 ${task.samples} 次 / ${task.interval_seconds}s`, `${task.samples} samples / ${task.interval_seconds}s`) }}<small>{{ text(`每次超时 ${task.timeout_seconds}s`, `${task.timeout_seconds}s timeout each`) }}</small></td><td><span class="tag" :class="{ warning: !task.public }">{{ task.public ? text('可见', 'Visible') : text('仅管理员', 'Admin only') }}</span></td><td>{{ task.enabled ? text('启用', 'Active') : text('暂停', 'Paused') }}</td><td class="row-actions"><button @click="openEditTarget(task)">{{ text('编辑', 'Edit') }}</button><button class="danger" @click="removeTarget(task)">{{ text('删除', 'Delete') }}</button></td></tr><tr v-if="!data.targets.length"><td colspan="6"><div class="table-empty">{{ text('添加一个受监控服务器以开始生成延迟矩阵。', 'Add a monitored server to start building the latency matrix.') }}</div></td></tr></tbody></table></div>

    <div v-if="editingTarget" class="modal-backdrop" @click.self="closeTargetDrawer"><form class="drawer" @submit.prevent="saveTarget"><header><div><span>{{ text('延迟目标', 'Latency target') }}</span><h2>{{ editingTarget.id ? text('编辑目标', 'Edit target') : text('新建目标', 'New target') }}</h2></div><button type="button" @click="closeTargetDrawer">×</button></header><div class="drawer-body"><div class="form-grid"><label class="wide"><span>{{ text('受监控服务器', 'Monitored server') }}</span><select v-model="editingTarget.target_node_id" required @change="selectTargetNode"><option v-for="node in data.monitored_nodes" :key="node.id" :value="node.id">{{ node.name }} · {{ node.region || node.ipv4 || node.ipv6 || text('无地区', 'No region') }}</option></select></label><template v-if="editingTarget.id"><label><span>{{ text('IP 协议栈', 'IP stack') }}</span><select v-model.number="editingTarget.ip_version" @change="changeTargetStack"><option :value="4">IPv4</option><option :value="6">IPv6</option></select></label><label><span>{{ text('测量方式', 'Measurement type') }}</span><select v-model="editingTarget.type" @change="changeTargetType"><option value="icmp">ICMP Ping</option><option value="tcp">TCP Connect</option></select></label><label class="wide"><span>{{ text('目标地址', 'Target address') }}</span><input v-model="editingTarget.target" required :placeholder="editTargetPlaceholder" /><small>{{ text('本地路由器视角可填写局域网地址；公网测量节点请填写公网域名或 IP。IPv6 TCP 请使用 [ipv6]:port 格式。', 'Use a LAN address for a private router perspective, or a public hostname/IP for global probes. IPv6 TCP uses [ipv6]:port.') }}</small></label><label class="wide"><span>{{ text('每轮次数', 'Samples per round') }}</span><input v-model.number="editingTarget.samples" type="number" min="1" max="10" /></label></template><template v-else><div class="field-group wide"><span class="field-label">{{ text('IP 协议栈', 'IP stack') }}</span><div class="target-segmented" role="group" :aria-label="text('IP 协议栈', 'IP stack')"><button type="button" :class="{ active: batchStack === 'both' }" :aria-pressed="batchStack === 'both'" @click="batchStack = 'both'">{{ text('IPv4 + IPv6 双栈', 'Dual stack (IPv4 + IPv6)') }}</button><button type="button" :class="{ active: batchStack === '4' }" :aria-pressed="batchStack === '4'" @click="batchStack = '4'">{{ text('仅 IPv4', 'IPv4 only') }}</button><button type="button" :class="{ active: batchStack === '6' }" :aria-pressed="batchStack === '6'" @click="batchStack = '6'">{{ text('仅 IPv6', 'IPv6 only') }}</button></div></div><div class="field-group wide"><span class="field-label">{{ text('测量方式', 'Protocol') }}</span><div class="target-segmented" role="group" :aria-label="text('测量方式', 'Protocol')"><button type="button" :class="{ active: batchProtocol === 'both' }" :aria-pressed="batchProtocol === 'both'" @click="batchProtocol = 'both'">ICMP + TCP</button><button type="button" :class="{ active: batchProtocol === 'icmp' }" :aria-pressed="batchProtocol === 'icmp'" @click="batchProtocol = 'icmp'">ICMP Ping</button><button type="button" :class="{ active: batchProtocol === 'tcp' }" :aria-pressed="batchProtocol === 'tcp'" @click="batchProtocol = 'tcp'">TCP Connect</button></div></div><label v-if="batchStack !== '6'" class="wide"><span>{{ text('IPv4 地址', 'IPv4 address') }}</span><input v-model="batchIPv4" required :placeholder="batchProtocol === 'tcp' ? '192.0.2.10 or v4.example.com:443' : '192.0.2.10 or v4.example.com'" /><small>{{ text('本地路由器视角可填写局域网地址；公网测量节点请填写公网域名或 IPv4 地址。', 'Use a LAN address for a private router perspective, or a public hostname/IPv4 address for global probes.') }}</small></label><label v-if="batchStack !== '4'" class="wide"><span>{{ text('IPv6 地址', 'IPv6 address') }}</span><input v-model="batchIPv6" required :placeholder="batchProtocol === 'tcp' ? '2001:db8::1 or [2001:db8::1]:443' : '2001:db8::1 or v6.example.com'" /><small>{{ text('填写 IPv6 字面量或仅解析 AAAA 的域名；生成 TCP 目标时会自动加上方括号和端口。', 'Enter an IPv6 literal or hostname; TCP targets automatically wrap IPv6 literals in brackets with the port.') }}</small></label><label v-if="batchProtocol !== 'icmp'"><span>{{ text('TCP 端口', 'TCP port') }}</span><input v-model.number="batchPort" type="number" min="1" max="65535" required /></label><label :class="{ wide: batchProtocol === 'icmp' }"><span>{{ text('每轮次数', 'Samples per round') }}</span><input v-model.number="editingTarget.samples" type="number" min="1" max="10" /></label></template><label><span>{{ text('间隔（秒）', 'Interval (seconds)') }}</span><input v-model.number="editingTarget.interval_seconds" type="number" min="5" max="86400" /></label><label><span>{{ text('单次超时（秒）', 'Timeout per attempt') }}</span><input v-model.number="editingTarget.timeout_seconds" type="number" min="1" max="60" /></label></div><div v-if="!editingTarget.id" class="combo-preview"><div class="combo-preview-head"><span>{{ text(`即将保存的组合（${plannedCombos.length}）`, `Target combinations (${plannedCombos.length})`) }}</span><small v-if="plannedCombos.length > 1">{{ text('已存在的组合会自动跳过', 'Existing combinations will be skipped') }}</small></div><ul><li v-for="combo in plannedCombos" :key="`${combo.ipVersion}-${combo.proto}`"><span class="tag ip-tag" :class="combo.ipVersion === 6 ? 'v6' : 'v4'">{{ ipVersionLabel(combo.ipVersion) }}</span><span class="tag">{{ combo.proto.toUpperCase() }}</span><code>{{ previewComboTarget(combo) }}</code><em v-if="comboAlreadyConfigured(combo)" class="combo-exists">{{ text('已存在', 'Exists') }}</em></li></ul></div><div class="check-row"><label><input v-model="editingTarget.public" type="checkbox" /><span>{{ text('在公开延迟页显示', 'Show on public latency page') }}</span></label><label><input v-model="editingTarget.enabled" type="checkbox" /><span>{{ text('启用目标', 'Target enabled') }}</span></label></div><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="closeTargetDrawer">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy">{{ busy ? text('正在保存…', 'Saving…') : editingTarget.id || plannedCombos.length === 1 ? text('保存目标', 'Save target') : text(`创建 ${plannedCombos.length} 个目标`, `Create ${plannedCombos.length} targets`) }}</button></footer></form></div>

    <div v-if="editingNode" class="modal-backdrop" @click.self="editingNode = null"><form class="drawer" @submit.prevent="saveNode"><header><div><span>{{ text('测量节点', 'Measurement node') }} / {{ editingNode.id.slice(0, 8) }}</span><h2>{{ text(`编辑 ${editingNode.name}`, `Edit ${editingNode.name}`) }}</h2></div><button type="button" @click="editingNode = null">×</button></header><div class="drawer-body"><div class="form-grid"><label><span>{{ text('显示名称', 'Display name') }}</span><input v-model="editingNode.name" required maxlength="128" /></label><label><span>{{ text('地区 / 运营商', 'Region / ISP') }}</span><input v-model="editingNode.region" maxlength="128" /></label><label><span>{{ text('国家代码', 'Country code') }}</span><input v-model="editingNode.country_code" maxlength="2" /></label><label class="wide"><span>{{ text('标签（逗号分隔）', 'Tags (comma separated)') }}</span><input v-model="tagsText" /></label><label class="wide"><span>{{ text('公开备注', 'Public remark') }}</span><textarea v-model="editingNode.public_remark" rows="2" /></label><label class="wide"><span>{{ text('私有备注', 'Private remark') }}</span><textarea v-model="editingNode.private_remark" rows="2" /></label></div><template v-if="agentConfig"><div class="panel-label"><span>{{ text('测量配置', 'Probe runtime') }}</span><b>V{{ agentConfig.config_version }}</b></div><div class="form-grid"><label><span>{{ text('心跳间隔（秒）', 'Heartbeat (seconds)') }}</span><input v-model.number="agentConfig.collect_interval_seconds" type="number" min="1" max="300" /></label><label><span>{{ text('最大并发数', 'Max concurrency') }}</span><input v-model.number="agentConfig.probe_concurrency" type="number" min="1" max="32" /></label></div></template><div class="check-row"><label><input v-model="editingNode.hidden" type="checkbox" /><span>{{ text('设为私有测量点', 'Keep this measurement point private') }}</span></label><label v-if="agentConfig"><input v-model="agentConfig.auto_update" type="checkbox" /><span>{{ text('启用签名自动更新', 'Signed automatic updates') }}</span></label></div><div v-if="error" class="form-error">{{ error }}</div></div><footer><button type="button" @click="editingNode = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy || !agentConfig">{{ busy ? text('正在保存…', 'Saving…') : text('保存节点', 'Save node') }}</button></footer></form></div>
  </section>
</template>

<style scoped>
.target-method-cell {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.ip-tag.v4 {
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--line));
}

.ip-tag.v6 {
  color: var(--violet);
  border-color: color-mix(in srgb, var(--violet) 50%, var(--line));
}

.field-group {
  display: block;
}

.field-group.wide {
  grid-column: 1 / -1;
}

.field-label {
  display: block;
  margin-bottom: 9px;
  color: var(--text-muted);
  font: 600 13px/1.3 var(--font-ui);
}

.form-grid label small {
  display: block;
  margin-top: 7px;
  color: var(--text-muted);
  font-size: 12px;
  line-height: 1.45;
}

.target-segmented {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  min-height: 49px;
  border: 1px solid var(--line-strong);
  background: var(--bg);
}

.target-segmented button {
  padding: 10px 12px;
  border: 0;
  border-right: 1px solid var(--line);
  background: transparent;
  color: var(--text-muted);
  font: 600 13px/1.2 var(--font-ui);
  transition: background .14s ease, color .14s ease;
}

.target-segmented button:last-child {
  border-right: 0;
}

.target-segmented button:hover {
  color: var(--text);
  background: var(--surface-2);
}

.target-segmented button.active {
  color: var(--accent-ink);
  background: var(--accent);
}

.combo-preview {
  margin-top: 22px;
  border: 1px solid var(--line-strong);
  background: var(--bg);
}

.combo-preview-head {
  padding: 11px 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  border-bottom: 1px solid var(--line);
  color: var(--text-soft);
  font: 600 12px/1.3 var(--font-ui);
}

.combo-preview-head small {
  color: var(--text-muted);
  font-weight: 500;
}

.combo-preview ul {
  margin: 0;
  padding: 0;
  list-style: none;
}

.combo-preview li {
  padding: 10px 14px;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  border-bottom: 1px solid var(--line);
}

.combo-preview li:last-child {
  border-bottom: 0;
}

.combo-preview code {
  color: var(--text-soft);
  font: 500 12px/1.3 var(--font-mono);
  overflow-wrap: anywhere;
}

.combo-exists {
  margin-left: auto;
  color: var(--warning);
  font: 600 11px/1 var(--font-ui);
  font-style: normal;
}

@media (max-width: 560px) {
  .field-group.wide {
    grid-column: auto;
  }

  .target-segmented {
    grid-template-columns: 1fr;
  }

  .target-segmented button {
    border-right: 0;
    border-bottom: 1px solid var(--line);
  }

  .target-segmented button:last-child {
    border-bottom: 0;
  }
}
</style>

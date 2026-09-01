<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { clone } from '../../clone'
import type { AdminNode, ProbeTask } from '../../types'
import { useLocale } from '../../i18n'

const tasks = ref<ProbeTask[]>([])
const carrierTasks = ref<ProbeTask[]>([])
const nodes = ref<AdminNode[]>([])
const editing = ref<ProbeTask | null>(null)
const carrierEditing = ref<ProbeTask | null>(null)
const error = ref('')
const carrierError = ref('')
const busy = ref(false)
const { text } = useLocale()

function blank(): ProbeTask {
  return { id: 0, name: '', type: 'tcp', target: '', interval_seconds: 60, timeout_seconds: 5, expected_status: 200, expected_value: '', node_ids: [], purpose: 'custom', run_on: 'monitor', public: false, samples: 1, enabled: true }
}

function edit(task: ProbeTask) { editing.value = clone(task) }
function editCarrier(task: ProbeTask) { carrierEditing.value = clone(task); carrierError.value = '' }
function carrierKey(task: ProbeTask) { return task.purpose?.split('.')[1] ?? '' }
function carrierName(task: ProbeTask) {
  if (task.purpose === 'carrier.telecom') return text('中国电信', 'China Telecom')
  if (task.purpose === 'carrier.unicom') return text('中国联通', 'China Unicom')
  if (task.purpose === 'carrier.mobile') return text('中国移动', 'China Mobile')
  return task.name
}

async function load() {
  const [taskResponse, carrierResponse, nodeResponse] = await Promise.all([
    api<{ data: ProbeTask[] }>('/api/v1/admin/probes'),
    api<{ data: ProbeTask[] }>('/api/v1/admin/carrier-probes'),
    api<{ data: AdminNode[] }>('/api/v1/admin/nodes'),
  ])
  tasks.value = unwrap(taskResponse)
  carrierTasks.value = unwrap(carrierResponse)
  nodes.value = unwrap(nodeResponse)
}

async function save() {
  if (!editing.value) return
  error.value = ''
  try {
    const path = editing.value.id ? `/api/v1/admin/probes/${editing.value.id}` : '/api/v1/admin/probes'
    const response = await api<{ data: ProbeTask }>(path, { method: editing.value.id ? 'PUT' : 'POST', body: JSON.stringify(editing.value) })
    const saved = unwrap(response)
    const index = tasks.value.findIndex((task) => task.id === saved.id)
    if (index >= 0) tasks.value[index] = saved
    else tasks.value.push(saved)
    editing.value = null
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法保存探测任务。', 'Could not save probe.')
  }
}

async function saveCarrier() {
  if (!carrierEditing.value) return
  carrierError.value = ''
  busy.value = true
  try {
    const key = carrierKey(carrierEditing.value)
    const response = await api<{ data: ProbeTask }>(`/api/v1/admin/carrier-probes/${encodeURIComponent(key)}`, {
      method: 'PUT', body: JSON.stringify(carrierEditing.value),
    })
    const saved = unwrap(response)
    const index = carrierTasks.value.findIndex((task) => task.id === saved.id)
    if (index >= 0) carrierTasks.value[index] = saved
    carrierEditing.value = null
  } catch (reason) {
    carrierError.value = reason instanceof Error ? reason.message : text('无法保存三网延迟设置。', 'Could not save carrier latency settings.')
  } finally {
    busy.value = false
  }
}

async function remove(task: ProbeTask) {
  if (!confirm(text(`删除探测任务“${task.name}”？`, `Delete probe “${task.name}”?`))) return
  await api(`/api/v1/admin/probes/${task.id}`, { method: 'DELETE' })
  tasks.value = tasks.value.filter((item) => item.id !== task.id)
}

onMounted(load)
</script>

<template>
  <section class="admin-page">
    <header class="page-title">
      <div><span class="eyebrow">{{ text('主动检测', 'Active checks') }}</span><h1>{{ text('服务探测', 'Service probes') }}</h1><p>{{ text('管理三网延迟和 ICMP、TCP、HTTP(S)、DNS 服务检测；不会执行任意命令。', 'Manage carrier latency and ICMP, TCP, HTTP(S), or DNS checks without arbitrary commands.') }}</p></div>
      <button class="primary-action compact-action" @click="editing = blank()">+ {{ text('新建任务', 'New probe') }}</button>
    </header>

    <section class="carrier-admin-board">
      <header><div><span>{{ text('内置测量', 'Built-in measurements') }}</span><h2>{{ text('三网延迟', 'Carrier latency') }}</h2><p>{{ text('每台普通监控 Agent 主动连接电信、联通和移动测试目标；内网节点也可以测量。', 'Every monitor Agent connects outward to Telecom, Unicom, and Mobile targets, including nodes behind NAT.') }}</p></div><b>3</b></header>
      <div class="carrier-admin-grid">
        <article v-for="task in carrierTasks" :key="task.id" :data-carrier="carrierKey(task)">
          <header><div><span>{{ carrierName(task) }}</span><small>{{ task.enabled ? text('正在测量', 'Active') : text('已暂停', 'Paused') }}</small></div><i class="status-dot" :class="task.enabled ? 'online' : ''" /></header>
          <code>{{ task.target }}</code>
          <footer><span>{{ task.type.toUpperCase() }} · {{ task.samples }} {{ text('次/轮', 'samples') }} · {{ task.interval_seconds }}s</span><button @click="editCarrier(task)">{{ text('设置', 'Configure') }}</button></footer>
        </article>
      </div>
      <p class="carrier-admin-note">{{ text('默认使用 TCP 443，适合 ICMP 受限的主机；目标和协议均可修改。结果显示在每个节点的详情页。', 'TCP 443 is the default for hosts where ICMP is restricted. Targets and protocols are editable, and results appear on each node detail page.') }}</p>
    </section>

    <div class="section-heading compact-heading admin-probe-heading"><div><span class="section-number">02</span><h2>{{ text('自定义服务探测', 'Custom service probes') }}</h2></div></div>
    <div class="data-table-wrap"><table class="admin-table"><thead><tr><th>{{ text('任务', 'Check') }}</th><th>{{ text('类型', 'Type') }}</th><th>{{ text('目标', 'Target') }}</th><th>{{ text('间隔', 'Interval') }}</th><th>{{ text('执行节点', 'Assignment') }}</th><th /></tr></thead><tbody><tr v-for="task in tasks" :key="task.id"><td><b>{{ task.name }}</b><small>#{{ task.id }}</small></td><td><span class="tag">{{ task.type.toUpperCase() }}</span></td><td><code>{{ task.target }}</code></td><td>{{ task.interval_seconds }}s / {{ text(`超时 ${task.timeout_seconds}s`, `${task.timeout_seconds}s timeout`) }}</td><td>{{ task.node_ids?.length ? text(`${task.node_ids.length} 个节点`, `${task.node_ids.length} nodes`) : text('全部节点', 'All nodes') }}</td><td class="row-actions"><button @click="edit(task)">{{ text('编辑', 'Edit') }}</button><button class="danger" @click="remove(task)">{{ text('删除', 'Delete') }}</button></td></tr><tr v-if="!tasks.length"><td colspan="6"><div class="table-empty">{{ text('还没有自定义服务探测。', 'No custom service probes yet.') }}</div></td></tr></tbody></table></div>

    <div v-if="carrierEditing" class="modal-backdrop" @click.self="carrierEditing = null">
      <form class="drawer" @submit.prevent="saveCarrier">
        <header><div><span>{{ text('三网延迟', 'Carrier latency') }}</span><h2>{{ carrierName(carrierEditing) }}</h2></div><button type="button" @click="carrierEditing = null">×</button></header>
        <div class="drawer-body"><div class="form-grid">
          <label><span>{{ text('测量协议', 'Probe protocol') }}</span><select v-model="carrierEditing.type"><option value="tcp">TCP</option><option value="icmp">ICMP</option></select></label>
          <label><span>{{ text('每轮采样次数', 'Samples per round') }}</span><input v-model.number="carrierEditing.samples" type="number" min="1" max="10" required /></label>
          <label class="wide"><span>{{ text('测试目标', 'Measurement target') }}</span><input v-model="carrierEditing.target" required :placeholder="carrierEditing.type === 'tcp' ? 'example.com:443' : 'example.com'" /><small>{{ text('TCP 使用 host:port；ICMP 只填写域名或 IP。', 'TCP uses host:port; ICMP uses only a hostname or IP address.') }}</small></label>
          <label><span>{{ text('测量间隔（秒）', 'Interval (seconds)') }}</span><input v-model.number="carrierEditing.interval_seconds" type="number" min="5" max="86400" required /></label>
          <label><span>{{ text('单次超时（秒）', 'Timeout per sample') }}</span><input v-model.number="carrierEditing.timeout_seconds" type="number" min="1" max="60" required /></label>
        </div><label class="switch-line"><input v-model="carrierEditing.enabled" type="checkbox" /><span>{{ text('启用这条三网测量', 'Enable this carrier measurement') }}</span></label><div v-if="carrierError" class="form-error">{{ carrierError }}</div></div>
        <footer><button type="button" @click="carrierEditing = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action" :disabled="busy">{{ busy ? text('正在保存…', 'Saving…') : text('保存设置', 'Save settings') }}</button></footer>
      </form>
    </div>

    <div v-if="editing" class="modal-backdrop" @click.self="editing = null">
      <form class="drawer" @submit.prevent="save">
        <header><div><span>{{ text('服务探测', 'Service probe') }}</span><h2>{{ editing.id ? text('编辑任务', 'Edit probe') : text('新建任务', 'New probe') }}</h2></div><button type="button" @click="editing = null">×</button></header>
        <div class="drawer-body"><div class="form-grid"><label class="wide"><span>{{ text('名称', 'Name') }}</span><input v-model="editing.name" required /></label><label><span>{{ text('类型', 'Type') }}</span><select v-model="editing.type"><option>icmp</option><option>tcp</option><option>http</option><option>dns</option></select></label><label class="wide"><span>{{ text('目标', 'Target') }}</span><input v-model="editing.target" required :placeholder="editing.type === 'tcp' ? 'host:port' : editing.type === 'http' ? 'https://example.com/health' : 'example.com'" /></label><label><span>{{ text('执行间隔（秒）', 'Interval (seconds)') }}</span><input v-model.number="editing.interval_seconds" type="number" min="5" /></label><label><span>{{ text('超时时间（秒）', 'Timeout (seconds)') }}</span><input v-model.number="editing.timeout_seconds" type="number" min="1" /></label><label v-if="editing.type === 'http'"><span>{{ text('预期 HTTP 状态码', 'Expected HTTP status') }}</span><input v-model.number="editing.expected_status" type="number" min="100" max="599" /></label><label class="wide"><span>{{ text('预期值（可选）', 'Expected value (optional)') }}</span><input v-model="editing.expected_value" /></label><label class="wide"><span>{{ text('执行节点', 'Node assignment') }}</span><select v-model="editing.node_ids" multiple><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option></select><small>{{ text('留空表示在全部监控节点上执行。', 'Leave empty to assign every monitor node.') }}</small></label></div><label class="switch-line"><input v-model="editing.enabled" type="checkbox" /><span>{{ text('启用任务', 'Probe enabled') }}</span></label><div v-if="error" class="form-error">{{ error }}</div></div>
        <footer><button type="button" @click="editing = null">{{ text('取消', 'Cancel') }}</button><button class="primary-action">{{ text('保存任务', 'Save probe') }}</button></footer>
      </form>
    </div>
  </section>
</template>

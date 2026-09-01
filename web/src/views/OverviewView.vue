<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import PublicHeader from '../components/PublicHeader.vue'
import NodeCard from '../components/NodeCard.vue'
import NodeFlag from '../components/NodeFlag.vue'
import SystemLogo from '../components/SystemLogo.vue'
import TrafficUsageBar from '../components/TrafficUsageBar.vue'
import { useMonitorStore } from '../stores/monitor'
import { useSessionStore } from '../stores/session'
import { bytes, duration, rate } from '../format'
import { useLocale } from '../i18n'

const monitor = useMonitorStore()
const session = useSessionStore()
const group = ref('')
const search = ref('')
const status = ref<'all' | 'online' | 'offline'>('all')
const view = ref<'grid' | 'table'>((localStorage.getItem('hostpin-view') as 'grid' | 'table') || 'grid')
const error = ref('')
const { text } = useLocale()
const statusOptions = computed(() => [
  { value: 'all' as const, label: text('全部', 'All') },
  { value: 'online' as const, label: text('在线', 'Online') },
  { value: 'offline' as const, label: text('离线', 'Offline') },
])

const filtered = computed(() => monitor.nodes.filter(({ node }) => {
  const query = search.value.trim().toLowerCase()
  return (!group.value || node.group === group.value)
    && (status.value === 'all' || (status.value === 'online') === node.online)
    && (!query || [node.name, node.group, node.region, node.country_code, ...node.tags].join(' ').toLowerCase().includes(query))
}))
const totalMemory = computed(() => monitor.nodes.reduce((sum, item) => sum + (item.metric?.memory_total ?? 0), 0))
const trafficRate = computed(() => monitor.nodes.reduce((sum, item) => sum + (item.metric?.net_rx_bps ?? 0) + (item.metric?.net_tx_bps ?? 0), 0))
const avgCPU = computed(() => {
  const samples = monitor.nodes.filter((item) => item.node.online && item.metric)
  return samples.length ? samples.reduce((sum, item) => sum + (item.metric?.cpu ?? 0), 0) / samples.length : 0
})

function setView(value: 'grid' | 'table') {
  view.value = value
  localStorage.setItem('hostpin-view', value)
}

onMounted(async () => {
  try {
    await monitor.load()
    monitor.connect()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法加载监控数据。', 'Could not load telemetry.')
  }
})
onBeforeUnmount(() => monitor.disconnect())
</script>

<template>
  <div class="public-layout km-layout">
    <PublicHeader />
    <main class="overview km-page-instance">
      <header class="compact-public-head">
        <h1>{{ text('节点', 'Nodes') }}</h1>
        <span class="update-stamp"><i /> {{ monitor.lastUpdate ? `${text('更新', 'Updated')} ${monitor.lastUpdate.toLocaleTimeString()}` : text('正在连接', 'Connecting') }}</span>
      </header>
      <section class="fleet-readout fleet-overview" :aria-label="text('节点概览', 'Fleet summary')">
        <div><span>{{ text('在线节点', 'Online') }}</span><strong>{{ monitor.onlineCount }}<small>/{{ monitor.nodes.length }}</small></strong><i :style="{ width: `${monitor.nodes.length ? monitor.onlineCount / monitor.nodes.length * 100 : 0}%` }" /></div>
        <div><span>{{ text('平均 CPU', 'Average CPU') }}</span><strong>{{ avgCPU.toFixed(1) }}<small>%</small></strong></div>
        <div><span>{{ text('总内存', 'Total memory') }}</span><strong>{{ bytes(totalMemory, 0) }}</strong></div>
        <div><span>{{ text('实时流量', 'Throughput') }}</span><strong>{{ rate(trafficRate) }}</strong></div>
      </section>

      <section class="fleet-section">
        <div class="fleet-toolbar">
          <label class="search-field"><span>⌕</span><input v-model="search" :aria-label="text('搜索节点', 'Search nodes')" :placeholder="text('搜索名称、地区或标签', 'Search name, region, or tag')" /></label>
          <div class="segmented" :aria-label="text('节点状态筛选', 'Node status filter')">
            <button v-for="item in statusOptions" :key="item.value" :class="{ active: status === item.value }" :aria-pressed="status === item.value" @click="status = item.value">{{ item.label }}</button>
          </div>
          <select v-model="group" :aria-label="text('按分组筛选', 'Filter by group')"><option value="">{{ text('全部分组', 'All groups') }}</option><option v-for="item in monitor.groups" :key="item">{{ item }}</option></select>
          <div class="view-switch"><button :class="{ active: view === 'grid' }" :aria-pressed="view === 'grid'" :aria-label="text('卡片视图', 'Grid view')" @click="setView('grid')">▦</button><button :class="{ active: view === 'table' }" :aria-pressed="view === 'table'" :aria-label="text('表格视图', 'Table view')" @click="setView('table')">☷</button></div>
        </div>

        <div v-if="error" class="notice error"><b>{{ text('连接失败', 'Connection failed') }}</b><span>{{ error }}</span><button @click="monitor.load()">{{ text('重试', 'Retry') }}</button></div>
        <div v-else-if="monitor.loading && !monitor.nodes.length" class="loading-grid"><i v-for="n in 6" :key="n" /></div>
        <div v-else-if="!filtered.length" class="empty-state"><span>∅</span><h3>{{ text('没有匹配的节点', 'No matching nodes') }}</h3><p>{{ text('请调整筛选条件，或注册一个新节点。', 'Change the filters or enroll a new node.') }}</p></div>

        <div v-else-if="view === 'grid'" class="node-grid km-instance-server-list">
          <NodeCard v-for="item in filtered" :key="item.node.id" :snapshot="item" />
        </div>
        <div v-else class="node-table-wrap">
          <table class="node-table km-ui-table">
            <thead><tr><th>{{ text('状态 / 节点', 'Status / node') }}</th><th>{{ text('地区', 'Location') }}</th><th>CPU</th><th>{{ text('内存', 'Memory') }}</th><th>{{ text('网络 ↓ / ↑', 'Network ↓ / ↑') }}</th><th>{{ text('本月已用', 'Monthly used') }}</th><th>{{ text('运行时间', 'Uptime') }}</th></tr></thead>
            <tbody>
              <tr v-for="item in filtered" :key="item.node.id" tabindex="0" role="link" :aria-label="text(`打开 ${item.node.name}`, `Open ${item.node.name}`)" @click="$router.push(`/nodes/${item.node.id}`)" @keydown.enter="$router.push(`/nodes/${item.node.id}`)">
                <td><span class="status-dot" :class="item.node.online ? 'online' : 'offline'" /><SystemLogo :os="item.node.os" /><b>{{ item.node.name }}</b><small>{{ item.node.os }}</small></td>
                <td><span class="node-table-location"><NodeFlag :country-code="item.node.country_code" /><span>{{ item.node.region || item.node.country_code || '—' }}</span></span></td>
                <td>{{ item.metric?.cpu.toFixed(1) ?? '—' }}%</td>
                <td>{{ bytes(item.metric?.memory_used) }} / {{ bytes(item.metric?.memory_total) }}</td>
                <td>{{ rate(item.metric?.net_rx_bps) }} / {{ rate(item.metric?.net_tx_bps) }}</td>
                <td><TrafficUsageBar compact :traffic-limit="item.node.traffic_limit" :traffic-mode="item.node.traffic_limit_type" :monthly-rx-bytes="item.metric?.monthly_rx_bytes" :monthly-tx-bytes="item.metric?.monthly_tx_bytes" /></td>
                <td>{{ item.metric ? duration(item.metric.uptime_seconds) : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </main>
    <footer class="public-footer"><span>HOSTPIN</span><span>v{{ session.version }}</span></footer>
  </div>
</template>

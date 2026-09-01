<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import PublicHeader from '../components/PublicHeader.vue'
import MetricBar from '../components/MetricBar.vue'
import TimeSeriesChart from '../components/TimeSeriesChart.vue'
import { api, unwrap } from '../api'
import { bytes, dateTime, duration, percent, rate, relativeTime } from '../format'
import { trafficUsage, trafficUtilization } from '../traffic'
import type { MetricSample, NodeSnapshot, ProbeResult, ProbeTask, PublicProbeSnapshot } from '../types'
import { useLocale } from '../i18n'

const route = useRoute()
const snapshot = ref<NodeSnapshot | null>(null)
const history = ref<MetricSample[]>([])
const probes = ref<PublicProbeSnapshot[]>([])
const carrierProbes = ref<PublicProbeSnapshot[]>([])
const hours = ref(24)
const loading = ref(true)
const error = ref('')
let socket: WebSocket | null = null
let refreshTimer = 0
let presenceTimer = 0
let offlineAfterMS = 90_000
const { text } = useLocale()

const nodeID = computed(() => String(route.params.id))
const shareToken = computed(() => typeof route.params.token === 'string' ? route.params.token : '')
const metric = computed(() => snapshot.value?.metric)
const memoryPercent = computed(() => percent(metric.value?.memory_used, metric.value?.memory_total))
const diskPercent = computed(() => percent(metric.value?.disk_used, metric.value?.disk_total))
const labels = computed(() => history.value.map((item) => new Date(item.received_at ?? item.collected_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })))
const resourceSeries = computed(() => [
  { name: 'CPU', values: history.value.map((item) => item.cpu), color: '#65d5c5' },
  { name: text('内存', 'MEM'), values: history.value.map((item) => percent(item.memory_used, item.memory_total)), color: '#d8b34f' },
  { name: text('磁盘', 'DISK'), values: history.value.map((item) => percent(item.disk_used, item.disk_total)), color: '#7c91ff' },
])
const networkSeries = computed(() => [
  { name: text('下载', 'DOWN'), values: history.value.map((item) => item.net_rx_bps / 1024 / 1024), color: '#65d5c5' },
  { name: text('上传', 'UP'), values: history.value.map((item) => item.net_tx_bps / 1024 / 1024), color: '#db7b5b' },
])
const trafficUsed = computed(() => {
  return trafficUsage(metric.value?.monthly_rx_bytes, metric.value?.monthly_tx_bytes, snapshot.value?.node.traffic_limit_type)
})
const trafficPercent = computed(() => trafficUtilization(trafficUsed.value, snapshot.value?.node.traffic_limit))
const trafficTone = computed(() => trafficPercent.value >= 100 ? 'critical' : trafficPercent.value >= 85 ? 'warning' : 'normal')

const carrierChart = computed(() => {
  const buckets = new Set<number>()
  for (const probe of carrierProbes.value) {
    for (const result of probe.results) {
      const timestamp = new Date(result.received_at ?? result.collected_at).getTime()
      if (Number.isFinite(timestamp)) buckets.add(Math.floor(timestamp / 60_000) * 60_000)
    }
  }
  const timeline = [...buckets].sort((left, right) => left - right)
  return {
    labels: timeline.map((timestamp) => new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })),
    series: carrierProbes.value.map((probe) => {
      const values = new Map<number, number | null>()
      for (const result of probe.results) {
        const timestamp = new Date(result.received_at ?? result.collected_at).getTime()
        const bucket = Math.floor(timestamp / 60_000) * 60_000
        values.set(bucket, result.success && result.latency_ms >= 0 ? Number(result.latency_ms.toFixed(2)) : null)
      }
      return {
        name: carrierName(probe.task.purpose),
        values: timeline.map((timestamp) => values.get(timestamp) ?? null),
        color: carrierColor(probe.task.purpose),
      }
    }),
  }
})

function latestProbe(results: ProbeResult[]) { return results.at(-1) }
function probeAvailability(results: ProbeResult[]) { return results.length ? results.filter((item) => item.success).length / results.length * 100 : 0 }
function carrierKey(purpose?: ProbeTask['purpose']) { return purpose?.split('.')[1] ?? '' }
function carrierName(purpose?: ProbeTask['purpose']) {
  if (purpose === 'carrier.telecom') return text('中国电信', 'China Telecom')
  if (purpose === 'carrier.unicom') return text('中国联通', 'China Unicom')
  if (purpose === 'carrier.mobile') return text('中国移动', 'China Mobile')
  return text('运营商', 'Carrier')
}
function carrierColor(purpose?: ProbeTask['purpose']) {
  if (purpose === 'carrier.telecom') return '#5b8ff9'
  if (purpose === 'carrier.unicom') return '#e66a6a'
  return '#5fbd78'
}
function carrierLoss(probe: PublicProbeSnapshot) { return latestProbe(probe.results)?.loss_percent ?? 0 }

async function load() {
  loading.value = true
  error.value = ''
  try {
    const nodePromise = shareToken.value
      ? api<{ nodes: NodeSnapshot[] }>(`/api/v1/public/share/${encodeURIComponent(shareToken.value)}`).then((value) => value.nodes.find((item) => item.node.id === nodeID.value) ?? null)
      : api<{ data: NodeSnapshot }>(`/api/v1/public/nodes/${nodeID.value}`).then(unwrap)
    const historyPath = shareToken.value
      ? `/api/v1/public/share/${encodeURIComponent(shareToken.value)}/history`
      : '/api/v1/public/history'
    const probePromise = shareToken.value
      ? Promise.resolve({ data: [] as PublicProbeSnapshot[] })
      : api<{ data: PublicProbeSnapshot[] }>(`/api/v1/public/probes?node_id=${encodeURIComponent(nodeID.value)}&hours=${hours.value}`).catch(() => ({ data: [] }))
    const carrierPromise = shareToken.value
      ? Promise.resolve({ data: [] as PublicProbeSnapshot[] })
      : api<{ data: PublicProbeSnapshot[] }>(`/api/v1/public/probes?purpose=carrier&node_id=${encodeURIComponent(nodeID.value)}&hours=${hours.value}&max_points=600`).catch(() => ({ data: [] }))
    const [node, points, probeData, carrierData] = await Promise.all([
      nodePromise,
      api<{ data: MetricSample[] }>(`${historyPath}?node_id=${encodeURIComponent(nodeID.value)}&hours=${hours.value}&max_points=600`),
      probePromise,
      carrierPromise,
    ])
    if (!node) throw new Error(text('此分享链接不包含该节点。', 'Node is not included in this share link.'))
    snapshot.value = node
    history.value = unwrap(points)
    probes.value = unwrap(probeData)
    carrierProbes.value = unwrap(carrierData)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法加载节点。', 'Could not load node.')
  } finally {
    loading.value = false
  }
}

async function changeWindow(value: number) {
  hours.value = value
  await load()
}

onMounted(async () => {
  await load()
  const livePath = shareToken.value ? `/api/v1/public/share/${encodeURIComponent(shareToken.value)}/live` : '/api/v1/public/live'
  const url = new URL(livePath, location.href)
  url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  socket = new WebSocket(url)
  socket.onmessage = (event) => {
	const update = JSON.parse(String(event.data)) as { type: string; node_id?: string; sample?: MetricSample; offline_after_ms?: number }
	if (update.type === 'snapshot' && update.offline_after_ms && update.offline_after_ms >= 10_000) offlineAfterMS = update.offline_after_ms
    if (update.type === 'sample' && update.node_id === nodeID.value && update.sample && snapshot.value) {
      snapshot.value.metric = update.sample
      snapshot.value.node.online = true
    }
  }
  refreshTimer = window.setInterval(() => load(), 60_000)
	presenceTimer = window.setInterval(() => {
		if (!snapshot.value?.metric) return
		const received = new Date(snapshot.value.metric.received_at ?? snapshot.value.metric.collected_at).getTime()
		snapshot.value.node.online = Date.now() - received < offlineAfterMS
	}, 1000)
})
onBeforeUnmount(() => { socket?.close(); window.clearInterval(refreshTimer); window.clearInterval(presenceTimer) })
</script>

<template>
  <div class="public-layout node-detail-layout">
    <PublicHeader />
    <main v-if="snapshot" class="node-detail km-page-instance">
      <RouterLink class="back-link" :to="shareToken ? `/share/${shareToken}` : '/'">← {{ text('返回节点列表', 'Back to nodes') }}</RouterLink>
      <section class="node-title-row">
        <div><span class="status-pill" :class="snapshot.node.online ? 'online' : 'offline'"><i />{{ snapshot.node.online ? text('在线', 'Online') : text('离线', 'Offline') }}</span><h1>{{ snapshot.node.name }}</h1><p>{{ snapshot.node.public_remark || `${snapshot.node.os || text('未知系统', 'Unknown OS')} · ${snapshot.node.arch || '—'}` }}</p></div>
        <div class="node-title-meta"><span>ID {{ snapshot.node.id }}</span><span>{{ text('最后上报', 'Last report') }} {{ relativeTime(metric?.received_at || snapshot.node.last_seen_at) }}</span></div>
      </section>

      <section class="detail-readouts">
        <div><span>CPU</span><strong>{{ metric?.cpu.toFixed(1) ?? '—' }}<small>%</small></strong><MetricBar label="" :value="metric?.cpu ?? 0" /></div>
        <div><span>{{ text('内存', 'Memory') }}</span><strong>{{ memoryPercent.toFixed(1) }}<small>%</small></strong><p>{{ bytes(metric?.memory_used) }} / {{ bytes(metric?.memory_total) }}</p></div>
        <div><span>{{ text('磁盘', 'Storage') }}</span><strong>{{ diskPercent.toFixed(1) }}<small>%</small></strong><p>{{ bytes(metric?.disk_used) }} / {{ bytes(metric?.disk_total) }}</p></div>
        <div><span>{{ text('运行时间', 'Uptime') }}</span><strong class="compact">{{ duration(metric?.uptime_seconds) }}</strong><p>{{ text(`${metric?.processes ?? '—'} 个进程`, `${metric?.processes ?? '—'} processes`) }}</p></div>
      </section>

      <section class="telemetry-grid">
        <div class="chart-panel wide">
          <div class="panel-label"><span>{{ text('资源使用率', 'Resource utilization') }}</span><div class="legend"><i class="cpu" />CPU <i class="mem" />{{ text('内存', 'MEM') }} <i class="disk" />{{ text('磁盘', 'DISK') }}</div></div>
          <TimeSeriesChart :labels="labels" :series="resourceSeries" unit="%" :max="100" :aria-label="text('CPU、内存和磁盘使用率历史', 'CPU, memory, and disk utilization history')" />
        </div>
        <div class="machine-panel">
          <div class="panel-label"><span>{{ text('系统信息', 'System information') }}</span></div>
          <dl><div><dt>CPU</dt><dd>{{ snapshot.node.cpu_name || '—' }}</dd></div><div><dt>{{ text('核心', 'Cores') }}</dt><dd>{{ snapshot.node.cpu_cores || '—' }}</dd></div><div><dt>{{ text('内核', 'Kernel') }}</dt><dd>{{ snapshot.node.kernel_version || '—' }}</dd></div><div><dt>{{ text('虚拟化', 'Virtualization') }}</dt><dd>{{ snapshot.node.virtualization || text('未知', 'Unknown') }}</dd></div><div><dt>{{ text('负载', 'Load') }}</dt><dd>{{ metric ? `${metric.load1.toFixed(2)} / ${metric.load5.toFixed(2)} / ${metric.load15.toFixed(2)}` : '—' }}</dd></div><div><dt>{{ text('温度', 'Temperature') }}</dt><dd>{{ metric?.temperature ? `${metric.temperature.toFixed(1)}°C` : '—' }}</dd></div></dl>
        </div>
        <div class="chart-panel wide">
          <div class="panel-label"><span>{{ text('网络吞吐', 'Network throughput') }}</span><b>MiB/s</b></div>
          <TimeSeriesChart :labels="labels" :series="networkSeries" :aria-label="text('网络下载和上传历史', 'Network download and upload history')" />
        </div>
        <div class="network-now">
          <div><span>{{ text('下载', 'Download') }}</span><strong>↓ {{ rate(metric?.net_rx_bps) }}</strong><small>{{ text(`累计 ${bytes(metric?.net_rx_bytes)}`, `${bytes(metric?.net_rx_bytes)} total`) }}</small></div>
          <div><span>{{ text('上传', 'Upload') }}</span><strong>↑ {{ rate(metric?.net_tx_bps) }}</strong><small>{{ text(`累计 ${bytes(metric?.net_tx_bytes)}`, `${bytes(metric?.net_tx_bytes)} total`) }}</small></div>
          <div><span>{{ text('连接数', 'Connections') }}</span><strong>{{ metric?.tcp_connections ?? '—' }} TCP</strong><small>{{ metric?.udp_connections ?? '—' }} UDP</small></div>
        </div>
      </section>

      <section v-if="carrierProbes.length" class="inventory-section carrier-latency-section">
        <div class="section-heading compact-heading"><div><span class="section-number">02</span><h2>{{ text('三网延迟', 'Carrier latency') }}</h2><p>{{ text(`当前服务器主动连接三网测试目标 · 最近 ${hours} 小时`, `Measured outbound by this server · last ${hours} hours`) }}</p></div></div>
        <div class="carrier-grid">
          <article v-for="probe in carrierProbes" :key="probe.task.id" :data-carrier="carrierKey(probe.task.purpose)">
            <header><div><span>{{ carrierName(probe.task.purpose) }}</span><small>{{ probe.task.type.toUpperCase() }} · {{ probe.task.samples }} {{ text('次/轮', 'samples') }}</small></div><i class="status-dot" :class="latestProbe(probe.results)?.success ? 'online' : latestProbe(probe.results) ? 'offline' : ''" /></header>
            <strong>{{ latestProbe(probe.results)?.success ? latestProbe(probe.results)?.latency_ms.toFixed(1) : '—' }}<small> ms</small></strong>
            <footer><span>{{ text('丢包', 'Loss') }} {{ carrierLoss(probe).toFixed(1) }}%</span><span>{{ probe.task.interval_seconds }}s</span></footer>
          </article>
        </div>
        <div class="carrier-history">
          <div class="panel-label"><span>{{ text('三网延迟历史', 'Carrier latency history') }}</span><div class="legend"><template v-for="probe in carrierProbes" :key="probe.task.id"><i :style="{ background: carrierColor(probe.task.purpose) }" />{{ carrierName(probe.task.purpose) }}</template></div></div>
          <TimeSeriesChart v-if="carrierChart.labels.length" :labels="carrierChart.labels" :series="carrierChart.series" unit=" ms" :aria-label="text('中国电信、中国联通和中国移动延迟历史', 'China Telecom, Unicom, and Mobile latency history')" />
          <div v-else class="carrier-empty">{{ text('等待第一轮测量结果，通常不超过 2 分钟。', 'Waiting for the first measurement round; this normally takes under two minutes.') }}</div>
        </div>
        <p class="carrier-note">{{ text('测量由 Agent 主动发起，不要求此节点具有公网 IP，也不会向节点下发脚本。', 'Measurements are initiated by the Agent; no public IP or remotely supplied script is required.') }}</p>
      </section>

      <section v-if="metric?.disks?.length" class="inventory-section"><div class="section-heading compact-heading"><div><span class="section-number">03</span><h2>{{ text('磁盘卷', 'Storage volumes') }}</h2></div></div><div class="inventory-table"><div class="inventory-row heading"><span>{{ text('挂载点', 'Mount') }}</span><span>{{ text('文件系统', 'Filesystem') }}</span><span>{{ text('容量', 'Capacity') }}</span><span>{{ text('读取 / 写入', 'Read / write') }}</span></div><div v-for="disk in metric.disks" :key="disk.mountpoint" class="inventory-row"><b>{{ disk.mountpoint }}</b><span>{{ disk.filesystem || '—' }}</span><span>{{ bytes(disk.used) }} / {{ bytes(disk.total) }}</span><span>{{ rate(disk.read_bps) }} / {{ rate(disk.write_bps) }}</span></div></div></section>
      <section v-if="metric?.gpus?.length" class="inventory-section"><div class="section-heading compact-heading"><div><span class="section-number">04</span><h2>GPU</h2></div></div><div class="gpu-grid"><article v-for="gpu in metric.gpus" :key="gpu.index"><span>GPU {{ gpu.index }}</span><h3>{{ gpu.name }}</h3><strong>{{ gpu.utilization.toFixed(1) }}<small>%</small></strong><p>{{ bytes(gpu.memory_used) }} / {{ bytes(gpu.memory_total) }} VRAM · {{ gpu.temperature ? `${gpu.temperature.toFixed(1)}°C` : text('无温度传感器', 'No temperature sensor') }}</p></article></div></section>
      <section v-if="probes.length" class="inventory-section"><div class="section-heading compact-heading"><div><span class="section-number">05</span><h2>{{ text('服务探测', 'Service probes') }}</h2><p>{{ text(`最近 ${hours} 小时`, `Last ${hours} hours`) }}</p></div></div><div class="probe-grid"><article v-for="probe in probes" :key="probe.task.id"><header><span class="status-dot" :class="latestProbe(probe.results)?.success ? 'online' : 'offline'" /><div><b>{{ probe.task.name }}</b><small>{{ probe.task.type.toUpperCase() }} · {{ probe.task.interval_seconds }}s</small></div></header><strong>{{ latestProbe(probe.results)?.latency_ms.toFixed(1) ?? '—' }}<small> ms</small></strong><footer><span>{{ probeAvailability(probe.results).toFixed(2) }}% {{ text('可用', 'available') }}</span><span>{{ text(`${probe.results.length} 个样本`, `${probe.results.length} samples`) }}</span></footer></article></div></section>
      <section v-if="metric" class="traffic-strip" :data-tone="trafficTone">
        <div><span>{{ text('本月流量', 'Monthly traffic') }} / {{ snapshot.node.traffic_limit_type.toUpperCase() }}</span><strong>{{ bytes(trafficUsed) }}<small v-if="snapshot.node.traffic_limit"> / {{ bytes(snapshot.node.traffic_limit) }}</small></strong><small class="traffic-breakdown">↓ {{ bytes(metric.monthly_rx_bytes) }} · ↑ {{ bytes(metric.monthly_tx_bytes) }}</small></div>
        <div v-if="snapshot.node.traffic_limit" class="traffic-gauge" role="meter" :aria-label="text('月流量配额使用率', 'Monthly traffic quota used')" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="Math.round(trafficPercent)"><i :style="{ width: `${trafficPercent}%` }" /></div>
        <div class="traffic-policy"><span>{{ text(`每月 UTC ${snapshot.node.traffic_reset_day || 1} 日重置`, `Reset on UTC day ${snapshot.node.traffic_reset_day || 1}`) }}</span><b>{{ snapshot.node.traffic_limit ? text(`已用 ${trafficPercent.toFixed(1)}%`, `${trafficPercent.toFixed(1)}% used`) : text('未设置配额', 'No quota') }}</b></div>
      </section>
      <section v-if="snapshot.node.price || snapshot.node.expires_at" class="billing-strip"><span>{{ text('计费', 'Billing') }}</span><b v-if="snapshot.node.price">{{ snapshot.node.currency }} {{ snapshot.node.price }} / {{ snapshot.node.billing_cycle_days }}{{ text('天', 'd') }}</b><b v-if="snapshot.node.expires_at">{{ text('到期', 'Expires') }} {{ dateTime(snapshot.node.expires_at) }}</b><b>{{ snapshot.node.auto_renewal ? text('自动续费', 'Auto renewal') : text('手动续费', 'Manual renewal') }}</b></section>
      <div class="window-switch"><span>{{ text('历史范围', 'History window') }}</span><button v-for="value in [1, 6, 24, 168, 720]" :key="value" :class="{ active: hours === value }" @click="changeWindow(value)">{{ value < 24 ? `${value}${text('小时', 'h')}` : `${value / 24}${text('天', 'd')}` }}</button></div>
    </main>
    <main v-else class="center-state"><div v-if="loading" class="radar-loader" /><template v-else><h1>{{ text('节点不可用', 'Node unavailable') }}</h1><p>{{ error }}</p><RouterLink :to="shareToken ? `/share/${shareToken}` : '/'">{{ text('返回节点列表', 'Return to nodes') }}</RouterLink></template></main>
  </div>
</template>

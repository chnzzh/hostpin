<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import PublicHeader from '../components/PublicHeader.vue'
import TimeSeriesChart from '../components/TimeSeriesChart.vue'
import { api, unwrap } from '../api'
import { relativeTime } from '../format'
import {
  FAILED_LATENCY_MS,
  MATRIX_REFRESH_INTERVAL_MS,
  latencyChartValue,
  latencyHistoryKey,
  shouldRefreshLatencyHistory,
  summarizeLatencyWindow,
} from '../latency'
import type { LatencyOverview, LatencyResult, LatencyTarget, LatencyWindowSummary } from '../types'
import { useLocale } from '../i18n'

const overview = ref<LatencyOverview>({ probe_nodes: [], targets: [], latest: [], offline_after_ms: 90_000 })
const history = ref<LatencyResult[]>([])
const historySummary = ref<LatencyWindowSummary>(summarizeLatencyWindow([]))
const selected = ref<{ probeNodeID: string; targetNodeID: string } | null>(null)
const hours = ref(24)
const loading = ref(true)
const historyLoading = ref(false)
const error = ref('')
const { text } = useLocale()
let refreshTimer = 0
let historyRequestID = 0
let unmounted = false
let inFlight = false
let activeLoadIncludeHistory = false
let upgradeHistoryRefresh = false
let lastHistoryLoadedAt: number | null = null
let lastHistoryKey = ''
let inFlightHistoryKey = ''
let inFlightHistoryPromise: Promise<void> | null = null

const latestMap = computed(() => new Map(overview.value.latest.map((item) => [`${item.probe_node_id}:${item.target_node_id}`, item])))
const onlineProbes = computed(() => overview.value.probe_nodes.filter((item) => item.online).length)
const healthyPaths = computed(() => overview.value.latest.filter((item) => item.success && item.loss_percent < 50).length)
const currentAverageLatency = computed<number | null>(() => {
  const values = overview.value.latest.filter((item) => item.success && item.latency_ms >= 0).map((item) => item.latency_ms)
  return values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : null
})
const selectedProbe = computed(() => overview.value.probe_nodes.find((item) => item.id === selected.value?.probeNodeID))
const selectedTarget = computed(() => overview.value.targets.find((item) => item.node.id === selected.value?.targetNodeID))
const historyLabels = computed(() => history.value.map((item) => new Date(item.received_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })))
const latencySeries = computed(() => [{ name: 'LATENCY', values: history.value.map(latencyChartValue), color: '#65d5c5' }])
const lossSeries = computed(() => [{ name: 'LOSS', values: history.value.map((item) => item.loss_percent), color: '#db7b5b' }])

function cell(probeNodeID: string, targetNodeID: string) {
  return latestMap.value.get(`${probeNodeID}:${targetNodeID}`)
}

function cellClass(result: LatencyResult | undefined, probeOnline: boolean, intervalSeconds: number) {
  if (!result) return 'empty'
  const staleAfter = Math.max(overview.value.offline_after_ms, intervalSeconds * 2_500)
  if (!probeOnline || Date.now() - new Date(result.received_at).getTime() > staleAfter) return 'stale'
  if (!result.success) return 'failed'
  if (result.loss_percent > 0 || result.latency_ms >= 180) return 'critical'
  if (result.latency_ms >= 80) return 'warning'
  return 'healthy'
}

function isDocumentHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

function clearRefreshTimer() {
  if (refreshTimer) {
    window.clearTimeout(refreshTimer)
    refreshTimer = 0
  }
}

function scheduleRefresh() {
  clearRefreshTimer()
  if (unmounted || isDocumentHidden()) return
  refreshTimer = window.setTimeout(() => {
    refreshTimer = 0
    if (unmounted || isDocumentHidden()) return
    void load({ includeHistory: false })
  }, MATRIX_REFRESH_INTERVAL_MS)
}

async function load(options: { includeHistory?: boolean } = {}) {
  const includeHistory = options.includeHistory === true
  if (unmounted) return
  clearRefreshTimer()
  if (inFlight) {
    if (includeHistory && !activeLoadIncludeHistory) {
      upgradeHistoryRefresh = true
    }
    return
  }

  inFlight = true
  activeLoadIncludeHistory = includeHistory
  upgradeHistoryRefresh = false

  try {
    const nextOverview = unwrap(await api<{ data: LatencyOverview }>('/api/v1/public/latency'))
    if (unmounted) return
    overview.value = nextOverview
    error.value = ''
    if (
      selected.value &&
      (!overview.value.probe_nodes.some((item) => item.id === selected.value?.probeNodeID) ||
        !overview.value.targets.some((item) => item.node.id === selected.value?.targetNodeID))
    ) {
      historyRequestID++
      inFlightHistoryKey = ''
      inFlightHistoryPromise = null
      historyLoading.value = false
      selected.value = null
      history.value = []
      historySummary.value = summarizeLatencyWindow([])
      lastHistoryKey = ''
      lastHistoryLoadedAt = null
    }
    if (!selected.value && overview.value.latest.length) {
      const first = overview.value.latest[0]
      if (first) selected.value = { probeNodeID: first.probe_node_id, targetNodeID: first.target_node_id }
    }
    if (selected.value) {
      const forceHistory = includeHistory || upgradeHistoryRefresh
      const nextKey = latencyHistoryKey(selected.value.probeNodeID, selected.value.targetNodeID, hours.value)
      if (inFlightHistoryPromise && inFlightHistoryKey === nextKey && !forceHistory) {
        await inFlightHistoryPromise
      } else if (
        shouldRefreshLatencyHistory({
          force: forceHistory,
          nextKey,
          lastHistoryKey,
          lastHistoryLoadedAt,
          hours: hours.value,
        })
      ) {
        await loadHistory()
      }
    }
  } catch (reason) {
    if (!unmounted) {
      error.value = reason instanceof Error ? reason.message : text('无法加载延迟数据。', 'Could not load latency telemetry.')
    }
  } finally {
    if (!unmounted) {
      loading.value = false
    }
    inFlight = false
    activeLoadIncludeHistory = false
    upgradeHistoryRefresh = false
    scheduleRefresh()
  }
}

function retry() {
  void load({ includeHistory: true })
}

async function selectPath(probeNodeID: string, targetNodeID: string) {
  selected.value = { probeNodeID, targetNodeID }
  history.value = []
  historySummary.value = summarizeLatencyWindow([])
  lastHistoryKey = ''
  lastHistoryLoadedAt = null
  inFlightHistoryKey = ''
  inFlightHistoryPromise = null
  await loadHistory()
}

async function loadHistory() {
  const path = selected.value
  if (!path || unmounted) return
  const requestedHours = hours.value
  const key = latencyHistoryKey(path.probeNodeID, path.targetNodeID, requestedHours)
  const requestID = ++historyRequestID
  historyLoading.value = true
  inFlightHistoryKey = key

  const run = (async () => {
    try {
      const query = new URLSearchParams({
        probe_node_id: path.probeNodeID,
        target_node_id: path.targetNodeID,
        hours: String(requestedHours),
        max_points: '600',
      })
      const response = await api<{ data: LatencyResult[]; summary?: LatencyWindowSummary }>(`/api/v1/public/latency/history?${query}`)
      if (unmounted || requestID !== historyRequestID) return
      const points = unwrap(response)
      history.value = points
      historySummary.value = response.summary ?? summarizeLatencyWindow(points)
      lastHistoryKey = key
      lastHistoryLoadedAt = Date.now()
      error.value = ''
    } catch (reason) {
      if (!unmounted && requestID === historyRequestID) {
        lastHistoryLoadedAt = null
        error.value = reason instanceof Error ? reason.message : text('无法加载链路历史。', 'Could not load route history.')
      }
    } finally {
      if (requestID === historyRequestID) {
        inFlightHistoryKey = ''
        inFlightHistoryPromise = null
        if (!unmounted) historyLoading.value = false
      }
    }
  })()

  inFlightHistoryPromise = run
  return run
}

async function changeWindow(value: number) {
  hours.value = value
  history.value = []
  historySummary.value = summarizeLatencyWindow([])
  lastHistoryKey = ''
  lastHistoryLoadedAt = null
  inFlightHistoryKey = ''
  inFlightHistoryPromise = null
  await loadHistory()
}

function targetLabel(target: LatencyTarget) {
  return target.node.region || target.node.country_code || target.node.group || text('未设置地区', 'No location')
}

function handleVisibilityChange() {
  if (unmounted) return
  if (isDocumentHidden()) {
    clearRefreshTimer()
    return
  }
  void load({ includeHistory: false })
}

onMounted(() => {
  unmounted = false
  document.addEventListener('visibilitychange', handleVisibilityChange)
  void load({ includeHistory: true })
})
onBeforeUnmount(() => {
  unmounted = true
  historyRequestID++
  clearRefreshTimer()
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<template>
  <div class="public-layout latency-layout">
    <PublicHeader />
    <main class="latency-page">
      <header class="compact-public-head">
        <h1>{{ text('延迟', 'Latency') }}</h1>
        <span class="update-stamp"><i /> {{ text('15 秒刷新', '15s refresh') }}</span>
      </header>
      <section class="latency-summary" :aria-label="text('延迟概览', 'Latency summary')">
        <article><span>{{ text('在线测量节点', 'Measurement nodes') }}</span><strong>{{ onlineProbes }}<small>/{{ overview.probe_nodes.length }}</small></strong></article>
        <article><span>{{ text('正常链路', 'Healthy paths') }}</span><strong>{{ healthyPaths }}<small>/{{ overview.latest.length }}</small></strong></article>
        <article><span>{{ text('当前平均延迟', 'Current mean RTT') }}</span><strong>{{ currentAverageLatency === null ? '—' : currentAverageLatency.toFixed(1) }}<small>ms</small></strong></article>
      </section>

      <div v-if="error" class="notice error"><b>{{ text('延迟数据不可用', 'Route data unavailable') }}</b><span>{{ error }}</span><button @click="retry">{{ text('重试', 'Retry') }}</button></div>
      <section class="latency-matrix-section">
        <div v-if="loading" class="latency-loading"><i /><span>{{ text('正在加载延迟数据', 'Loading latency data') }}</span></div>
        <div v-else-if="!overview.probe_nodes.length || !overview.targets.length" class="empty-state latency-empty">
          <span>⌁</span><h3>{{ text('暂无测量链路', 'No measurement paths') }}</h3><p>{{ text('请先在后台添加延迟测量节点和服务器目标。', 'Add a Probe Node and server targets in the administration page.') }}</p>
        </div>
        <div v-else class="latency-matrix-wrap">
          <table class="latency-matrix">
            <thead>
              <tr><th><span>{{ text('目标服务器', 'Target server') }}</span><small>{{ text(`${overview.targets.length} 条链路`, `${overview.targets.length} routes`) }}</small></th><th v-for="probe in overview.probe_nodes" :key="probe.id"><span class="status-dot" :class="probe.online ? 'online' : 'offline'" /><b>{{ probe.name }}</b><small>{{ probe.region || probe.country_code || text('私有网络', 'Private edge') }}</small></th></tr>
            </thead>
            <tbody>
              <tr v-for="target in overview.targets" :key="target.task_id">
                <th><RouterLink :to="`/nodes/${target.node.id}`">{{ target.node.name }}</RouterLink><span>{{ targetLabel(target) }} · {{ target.type.toUpperCase() }} · {{ target.samples }}×</span></th>
                <td v-for="probe in overview.probe_nodes" :key="probe.id">
                  <button :class="[cellClass(cell(probe.id, target.node.id), probe.online, target.interval_seconds), { selected: selected?.probeNodeID === probe.id && selected?.targetNodeID === target.node.id }]" :disabled="!cell(probe.id, target.node.id)" @click="selectPath(probe.id, target.node.id)">
                    <template v-if="cell(probe.id, target.node.id)"><strong>{{ cell(probe.id, target.node.id)?.success ? cell(probe.id, target.node.id)?.latency_ms.toFixed(1) : text('失败', 'Fail') }}<small v-if="cell(probe.id, target.node.id)?.success"> ms</small></strong><span>{{ cell(probe.id, target.node.id)?.loss_percent.toFixed(0) }}% {{ text('丢包', 'loss') }}</span><time>{{ relativeTime(cell(probe.id, target.node.id)?.received_at) }}</time></template>
                    <template v-else><strong>—</strong><span>{{ text('暂无数据', 'No sample') }}</span></template>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="!loading && overview.probe_nodes.length && overview.targets.length" class="latency-mobile-matrix" :aria-label="text('按测量节点分组的延迟', 'Latency grouped by measurement node')">
          <section v-for="probe in overview.probe_nodes" :key="probe.id">
            <header><div class="mobile-probe-node"><i class="status-dot" :class="probe.online ? 'online' : 'offline'" /><div><b>{{ probe.name }}</b><span>{{ probe.region || probe.country_code || text('私有网络', 'Private edge') }}</span></div></div><small>{{ text('测量节点', 'Probe Node') }}</small></header>
            <div v-for="target in overview.targets" :key="target.task_id" class="mobile-route" :class="[cellClass(cell(probe.id, target.node.id), probe.online, target.interval_seconds), { selected: selected?.probeNodeID === probe.id && selected?.targetNodeID === target.node.id }]">
              <RouterLink class="mobile-route-node" :to="`/nodes/${target.node.id}`"><b>{{ target.node.name }}</b><small>{{ targetLabel(target) }} · {{ target.type.toUpperCase() }} · {{ target.samples }}×</small></RouterLink>
              <button class="mobile-route-result" :disabled="!cell(probe.id, target.node.id)" :aria-label="text(`${probe.name} 到 ${target.node.name} 的延迟`, `${probe.name} to ${target.node.name} latency`)" @click="selectPath(probe.id, target.node.id)">
                <template v-if="cell(probe.id, target.node.id)"><strong>{{ cell(probe.id, target.node.id)?.success ? cell(probe.id, target.node.id)?.latency_ms.toFixed(1) : text('失败', 'Fail') }}<small v-if="cell(probe.id, target.node.id)?.success"> ms</small></strong><span>{{ cell(probe.id, target.node.id)?.loss_percent.toFixed(0) }}% {{ text('丢包', 'loss') }} · {{ relativeTime(cell(probe.id, target.node.id)?.received_at) }}</span></template>
                <template v-else><strong>—</strong><span>{{ text('暂无数据', 'No sample') }}</span></template>
              </button>
            </div>
          </section>
        </div>
      </section>

      <section v-if="selected" class="latency-history-section">
        <div class="section-heading compact-heading route-history-heading">
          <div><h2>{{ selectedProbe?.name }} → {{ selectedTarget?.node.name }}</h2></div>
          <div class="window-switch latency-window"><button v-for="value in [1, 6, 24, 168, 720]" :key="value" :class="{ active: hours === value }" @click="changeWindow(value)">{{ value < 24 ? `${value}H` : `${value / 24}D` }}</button></div>
        </div>
        <dl class="latency-window-stats" :class="{ loading: historyLoading }" :aria-busy="historyLoading" :aria-label="text('所选时间段统计', 'Selected window statistics')">
          <div>
            <dt>{{ text('平均延迟', 'Mean RTT') }}</dt>
            <dd>{{ historySummary.sample_count ? historySummary.average_latency_ms.toFixed(1) : '—' }}<small>ms</small></dd>
          </div>
          <div>
            <dt>{{ text('平均丢包率', 'Mean packet loss') }}</dt>
            <dd>{{ historySummary.sample_count ? historySummary.average_loss_percent.toFixed(1) : '—' }}<small>%</small></dd>
          </div>
          <div>
            <dt>{{ text('样本数量', 'Samples') }}</dt>
            <dd>{{ historySummary.sample_count }}<small>{{ text('次', 'total') }}</small></dd>
          </div>
        </dl>
        <div class="latency-chart-grid" :class="{ loading: historyLoading }">
          <article><div class="panel-label"><span>{{ text('往返延迟', 'Round-trip time') }}</span><b>{{ text(`失败 = ${FAILED_LATENCY_MS} ms`, `Failure = ${FAILED_LATENCY_MS} ms`) }}</b></div><TimeSeriesChart :labels="historyLabels" :series="latencySeries" unit="ms" :aria-label="text('所选链路延迟历史', 'Selected route latency history')" /></article>
          <article><div class="panel-label"><span>{{ text('丢包率', 'Packet loss') }}</span><b>%</b></div><TimeSeriesChart :labels="historyLabels" :series="lossSeries" unit="%" :max="100" :aria-label="text('所选链路丢包历史', 'Selected route packet loss history')" /></article>
        </div>
      </section>
    </main>
    <footer class="public-footer"><span>HOSTPIN</span><span>{{ text(`${overview.probe_nodes.length} 个测量点`, `${overview.probe_nodes.length} perspectives`) }}</span></footer>
  </div>
</template>

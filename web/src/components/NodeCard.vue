<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import type { NodeSnapshot } from '../types'
import { bytes, duration, percent, rate, relativeTime } from '../format'
import MetricBar from './MetricBar.vue'
import NodeFlag from './NodeFlag.vue'
import SystemLogo from './SystemLogo.vue'
import TrafficUsageBar from './TrafficUsageBar.vue'
import { useLocale } from '../i18n'

const props = defineProps<{ snapshot: NodeSnapshot; to?: string }>()
const node = computed(() => props.snapshot.node)
const metric = computed(() => props.snapshot.metric)
const memory = computed(() => percent(metric.value?.memory_used, metric.value?.memory_total))
const disk = computed(() => percent(metric.value?.disk_used, metric.value?.disk_total))
const { text } = useLocale()
</script>

<template>
  <RouterLink class="node-card km-node-card" :class="{ offline: !node.online }" :to="to || `/nodes/${node.id}`">
    <div class="node-card-topline">
      <span class="status-dot" :class="node.online ? 'online' : 'offline'" />
      <span class="node-region">{{ node.region || node.country_code || text('未设置地区', 'No location') }}</span>
      <NodeFlag :country-code="node.country_code" />
    </div>
    <div class="node-identity">
      <div>
        <h3 class="node-name"><SystemLogo :os="node.os" /><span>{{ node.name }}</span></h3>
        <p>{{ node.os || text('未知系统', 'Unknown OS') }} · {{ node.arch || '—' }}</p>
      </div>
      <strong class="node-cpu-number">{{ metric?.cpu.toFixed(0) ?? '—' }}<small>%</small></strong>
    </div>
    <div class="node-tags">
      <span v-if="node.group">{{ node.group }}</span>
      <span v-for="tag in node.tags.slice(0, 3)" :key="tag">{{ tag }}</span>
    </div>
    <div class="node-metrics">
      <MetricBar label="CPU" :value="metric?.cpu ?? 0" />
      <MetricBar :label="text('内存', 'MEM')" :value="memory" :detail="metric ? `${bytes(metric.memory_used)} / ${bytes(metric.memory_total)}` : ''" />
      <MetricBar :label="text('磁盘', 'DISK')" :value="disk" :detail="metric ? `${bytes(metric.disk_used)} / ${bytes(metric.disk_total)}` : ''" />
    </div>
    <TrafficUsageBar
      :traffic-limit="node.traffic_limit"
      :traffic-mode="node.traffic_limit_type"
      :monthly-rx-bytes="metric?.monthly_rx_bytes"
      :monthly-tx-bytes="metric?.monthly_tx_bytes"
    />
    <div class="node-io">
      <span><b>↓</b> {{ rate(metric?.net_rx_bps) }}</span>
      <span><b>↑</b> {{ rate(metric?.net_tx_bps) }}</span>
      <span>{{ text('运行', 'UP') }} {{ duration(metric?.uptime_seconds) }}</span>
    </div>
    <footer>
      <span>{{ node.online ? text('运行正常', 'Healthy') : `${text('最后上报', 'Last report')} ${relativeTime(metric?.received_at || node.last_seen_at)}` }}</span>
      <i aria-hidden="true">→</i>
    </footer>
  </RouterLink>
</template>

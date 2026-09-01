<script setup lang="ts">
import { computed } from 'vue'
import { useLocale } from '../i18n'
import { trafficCorrectionUnits, trafficUsage, trafficUtilization } from '../traffic'

const props = defineProps<{
  trafficLimit: number
  trafficMode?: string
  monthlyRxBytes?: number
  monthlyTxBytes?: number
  compact?: boolean
}>()

const { locale, text } = useLocale()
const quota = computed(() => Number.isFinite(props.trafficLimit) ? Math.max(0, props.trafficLimit) : 0)
const hasUsage = computed(() => typeof props.monthlyRxBytes === 'number' && Number.isFinite(props.monthlyRxBytes)
  && typeof props.monthlyTxBytes === 'number' && Number.isFinite(props.monthlyTxBytes))
const used = computed(() => trafficUsage(props.monthlyRxBytes, props.monthlyTxBytes, props.trafficMode))
const usedPercent = computed(() => trafficUtilization(used.value, quota.value))
const state = computed(() => !hasUsage.value ? 'unavailable' : quota.value <= 0 ? 'unlimited' : 'metered')
const tone = computed(() => usedPercent.value >= 100 ? 'critical' : usedPercent.value >= 85 ? 'warning' : 'normal')
const roundedPercent = computed(() => Math.round(usedPercent.value))
const usedLabel = computed(() => {
  const value = used.value / trafficCorrectionUnits.GiB
  if (value > 0 && value < 0.01) return '<0.01 GiB'
  return `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value)} GiB`
})
const ariaValueText = computed(() => text(
  `本月已用 ${usedLabel.value}，配额的 ${roundedPercent.value}%`,
  `${usedLabel.value} used this month, ${roundedPercent.value}% of quota`,
))
</script>

<template>
  <div class="traffic-usage" :class="{ compact }" :data-state="state" :data-tone="tone">
    <div class="traffic-usage-head">
      <span>{{ text('本月已用', 'Monthly used') }}</span>
      <strong v-if="state === 'metered'">{{ usedLabel }}<small>{{ roundedPercent }}%</small></strong>
      <strong v-else-if="state === 'unlimited'">{{ usedLabel }}<small>{{ text('不限量', 'Unlimited') }}</small></strong>
      <strong v-else>{{ text('暂无数据', 'No data') }}</strong>
    </div>
    <div
      v-if="state === 'metered'"
      class="traffic-usage-track"
      role="meter"
      :aria-label="text('本月流量使用率', 'Monthly traffic used')"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="roundedPercent"
      :aria-valuetext="ariaValueText"
    ><i :style="{ width: `${usedPercent}%` }" /></div>
    <div v-else class="traffic-usage-track" aria-hidden="true"><i /></div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{ label: string; value: number; detail?: string; warn?: number; critical?: number }>(), {
  detail: '',
  warn: 75,
  critical: 90,
})

const normalized = computed(() => Math.min(100, Math.max(0, Number.isFinite(props.value) ? props.value : 0)))
const tone = computed(() => normalized.value >= props.critical ? 'critical' : normalized.value >= props.warn ? 'warning' : 'normal')
</script>

<template>
  <div class="metric-bar" :data-tone="tone">
    <div class="metric-bar-label"><span>{{ label }}</span><strong>{{ normalized.toFixed(1) }}%</strong></div>
    <div class="metric-track" aria-hidden="true"><i :style="{ width: `${normalized}%` }" /></div>
    <small v-if="detail">{{ detail }}</small>
  </div>
</template>

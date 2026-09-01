<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

const props = defineProps<{
  labels: string[]
  series: Array<{ name: string; values: Array<number | null>; color: string }>
  unit?: string
  max?: number
  ariaLabel?: string
}>()
const element = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
let observer: ResizeObserver | null = null
let appearanceObserver: MutationObserver | null = null
let colorScheme: MediaQueryList | null = null

function render() {
  if (!chart) return
  const styles = getComputedStyle(document.documentElement)
  const muted = styles.getPropertyValue('--text-muted').trim()
  const grid = styles.getPropertyValue('--line').trim()
  const uiFont = styles.getPropertyValue('--font-ui').trim()
  chart.setOption({
    animation: false,
    backgroundColor: 'transparent',
    grid: { left: 54, right: 16, top: 18, bottom: 34 },
    tooltip: { trigger: 'axis', backgroundColor: '#111619', borderColor: '#4b595d', padding: 12, textStyle: { color: '#eef2ef', fontFamily: uiFont, fontSize: 13 } },
    xAxis: { type: 'category', data: props.labels, boundaryGap: false, axisLabel: { color: muted, fontFamily: uiFont, fontSize: 11, hideOverlap: true }, axisLine: { lineStyle: { color: grid } }, axisTick: { show: false } },
    yAxis: { type: 'value', min: 0, max: props.max, axisLabel: { color: muted, fontFamily: uiFont, fontSize: 11, formatter: `{value}${props.unit ?? ''}` }, splitLine: { lineStyle: { color: grid, type: 'dashed' } } },
    series: props.series.map((item) => ({ name: item.name, type: 'line', data: item.values, symbol: 'none', smooth: 0.18, lineStyle: { width: 2, color: item.color }, areaStyle: { color: item.color, opacity: 0.08 } })),
  }, true)
}

onMounted(() => {
  if (!element.value) return
  chart = echarts.init(element.value)
  render()
  observer = new ResizeObserver(() => chart?.resize())
  observer.observe(element.value)
  appearanceObserver = new MutationObserver(render)
  appearanceObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-appearance', 'data-locale'] })
  colorScheme = window.matchMedia('(prefers-color-scheme: light)')
  colorScheme.addEventListener('change', render)
})
watch(() => [props.labels, props.series], render, { deep: true })
onBeforeUnmount(() => { observer?.disconnect(); appearanceObserver?.disconnect(); colorScheme?.removeEventListener('change', render); chart?.dispose() })
</script>

<template><div ref="element" class="time-series-chart" role="img" :aria-label="ariaLabel || `${series.map((item) => item.name).join(', ')} time series chart`" /></template>

import type { LatencyResult, LatencyWindowSummary } from './types'

export const FAILED_LATENCY_MS = 999
export const MATRIX_REFRESH_INTERVAL_MS = 15_000
export const SHORT_HISTORY_REFRESH_MS = 30_000
export const LONG_HISTORY_REFRESH_MS = 60_000

export function latencyHistoryKey(probeNodeID: string, targetNodeID: string, hours: number): string {
  return `${probeNodeID}:${targetNodeID}:${hours}`
}

export function historyRefreshIntervalMs(hours: number): number {
  return hours <= 6 ? SHORT_HISTORY_REFRESH_MS : LONG_HISTORY_REFRESH_MS
}

export function shouldRefreshLatencyHistory(options: {
  force?: boolean
  nextKey: string
  lastHistoryKey: string
  lastHistoryLoadedAt: number | null
  hours: number
  now?: number
}): boolean {
  const { force = false, nextKey, lastHistoryKey, lastHistoryLoadedAt, hours, now = Date.now() } = options
  if (!nextKey) return false
  if (force) return true
  if (!lastHistoryKey || lastHistoryKey !== nextKey || lastHistoryLoadedAt === null) return true
  return now - lastHistoryLoadedAt >= historyRefreshIntervalMs(hours)
}

type LatencySample = Pick<LatencyResult, 'success' | 'latency_ms' | 'loss_percent'>

export function latencyChartValue(sample: LatencySample) {
  return sample.success && Number.isFinite(sample.latency_ms) && sample.latency_ms >= 0
    ? sample.latency_ms
    : FAILED_LATENCY_MS
}

export function summarizeLatencyWindow(samples: LatencySample[]): LatencyWindowSummary {
  let latencyTotal = 0
  let successCount = 0
  let lossTotal = 0
  let lossCount = 0

  for (const sample of samples) {
    if (sample.success && Number.isFinite(sample.latency_ms) && sample.latency_ms >= 0) {
      latencyTotal += sample.latency_ms
      successCount++
    }
    if (Number.isFinite(sample.loss_percent)) {
      lossTotal += Math.min(100, Math.max(0, sample.loss_percent))
      lossCount++
    }
  }

  return {
    average_latency_ms: successCount > 0 ? latencyTotal / successCount : samples.length > 0 ? FAILED_LATENCY_MS : 0,
    average_loss_percent: lossCount > 0 ? lossTotal / lossCount : 0,
    sample_count: samples.length,
    success_count: successCount,
  }
}

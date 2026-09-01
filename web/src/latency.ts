import type { LatencyResult, LatencyWindowSummary } from './types'

export const FAILED_LATENCY_MS = 999

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

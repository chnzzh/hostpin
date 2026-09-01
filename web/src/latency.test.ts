import { describe, expect, it } from 'vitest'
import { FAILED_LATENCY_MS, latencyChartValue, summarizeLatencyWindow } from './latency'

describe('latency presentation', () => {
  it('renders failed and invalid samples at the explicit timeout value', () => {
    expect(latencyChartValue({ success: false, latency_ms: -1, loss_percent: 100 })).toBe(FAILED_LATENCY_MS)
    expect(latencyChartValue({ success: true, latency_ms: -1, loss_percent: 0 })).toBe(FAILED_LATENCY_MS)
    expect(latencyChartValue({ success: true, latency_ms: 86.25, loss_percent: 0 })).toBe(86.25)
  })

  it('summarizes the complete selected window without counting failures as successful RTT', () => {
    expect(summarizeLatencyWindow([
      { success: true, latency_ms: 80, loss_percent: 0 },
      { success: true, latency_ms: 120, loss_percent: 50 },
      { success: false, latency_ms: -1, loss_percent: 100 },
    ])).toEqual({
      average_latency_ms: 100,
      average_loss_percent: 50,
      sample_count: 3,
      success_count: 2,
    })
  })

  it('uses 999 ms when every sample in the selected window failed', () => {
    expect(summarizeLatencyWindow([
      { success: false, latency_ms: -1, loss_percent: 100 },
    ])).toEqual({
      average_latency_ms: FAILED_LATENCY_MS,
      average_loss_percent: 100,
      sample_count: 1,
      success_count: 0,
    })
  })
})

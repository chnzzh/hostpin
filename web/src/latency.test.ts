import { describe, expect, it } from 'vitest'
import {
  FAILED_LATENCY_MS,
  LONG_HISTORY_REFRESH_MS,
  SHORT_HISTORY_REFRESH_MS,
  historyRefreshIntervalMs,
  latencyChartValue,
  latencyHistoryKey,
  shouldRefreshLatencyHistory,
  summarizeLatencyWindow,
} from './latency'

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

  it('computes history refresh cadence and staleness per window size', () => {
    expect(latencyHistoryKey('probe-a', 'target-b', 24)).toBe('probe-a:target-b:24')
    expect(historyRefreshIntervalMs(1)).toBe(SHORT_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(6)).toBe(SHORT_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(24)).toBe(LONG_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(720)).toBe(LONG_HISTORY_REFRESH_MS)

    const key24 = latencyHistoryKey('probe-a', 'target-b', 24)
    expect(shouldRefreshLatencyHistory({
      nextKey: key24,
      lastHistoryKey: '',
      lastHistoryLoadedAt: null,
      hours: 24,
      now: 10_000,
    })).toBe(true)

    expect(shouldRefreshLatencyHistory({
      nextKey: key24,
      lastHistoryKey: key24,
      lastHistoryLoadedAt: 10_000,
      hours: 24,
      now: 55_000,
    })).toBe(false)

    expect(shouldRefreshLatencyHistory({
      nextKey: key24,
      lastHistoryKey: key24,
      lastHistoryLoadedAt: 10_000,
      hours: 24,
      now: 70_000,
    })).toBe(true)

    expect(shouldRefreshLatencyHistory({
      force: true,
      nextKey: key24,
      lastHistoryKey: key24,
      lastHistoryLoadedAt: 10_000,
      hours: 24,
      now: 12_000,
    })).toBe(true)

    const key6 = latencyHistoryKey('probe-a', 'target-b', 6)
    expect(shouldRefreshLatencyHistory({
      nextKey: key6,
      lastHistoryKey: key6,
      lastHistoryLoadedAt: 10_000,
      hours: 6,
      now: 25_000,
    })).toBe(false)

    expect(shouldRefreshLatencyHistory({
      nextKey: key6,
      lastHistoryKey: key6,
      lastHistoryLoadedAt: 10_000,
      hours: 6,
      now: 40_000,
    })).toBe(true)
  })
})

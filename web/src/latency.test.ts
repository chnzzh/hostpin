import { describe, expect, it } from 'vitest'
import {
  FAILED_LATENCY_MS,
  LONG_HISTORY_REFRESH_MS,
  SHORT_HISTORY_REFRESH_MS,
  filterLatencyTargets,
  historyRefreshIntervalMs,
  latencyChartValue,
  latencyHistoryKey,
  normalizeLatencyIPVersion,
  shouldRefreshLatencyHistory,
  summarizeLatencyWindow,
} from './latency'
import type { LatencyTarget, PublicNode } from './types'

function makeTarget(taskID: number, ipVersion: 4 | 6 | undefined, type: 'icmp' | 'tcp'): LatencyTarget {
  const now = '2026-11-23T06:00:00Z'
  const node: PublicNode = {
    id: 'target-node',
    role: 'monitor',
    latency_enabled: true,
    name: 'Tokyo Edge',
    tags: [],
    hidden: false,
    weight: 0,
    price: 0,
    currency: 'USD',
    billing_cycle_days: 30,
    auto_renewal: false,
    traffic_limit: 0,
    traffic_limit_type: 'sum',
    traffic_reset_day: 1,
    created_at: now,
    updated_at: now,
    online: true,
  }
  return {
    task_id: taskID,
    node,
    ip_version: ipVersion as 4 | 6,
    type,
    interval_seconds: 30,
    samples: 3,
  }
}

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

  it('computes history refresh cadence and staleness per window size keyed by task ID', () => {
    expect(latencyHistoryKey('probe-a', 101, 24)).toBe('probe-a:101:24')
    expect(latencyHistoryKey('probe-a', 'task-102', 24)).toBe('probe-a:task-102:24')
    expect(latencyHistoryKey('probe-a', 101, 24)).not.toBe(latencyHistoryKey('probe-a', 102, 24))
    expect(historyRefreshIntervalMs(1)).toBe(SHORT_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(6)).toBe(SHORT_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(24)).toBe(LONG_HISTORY_REFRESH_MS)
    expect(historyRefreshIntervalMs(720)).toBe(LONG_HISTORY_REFRESH_MS)

    const key24 = latencyHistoryKey('probe-a', 101, 24)
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

    const key6 = latencyHistoryKey('probe-a', 101, 6)
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

  it('normalizes IP version defensively to 4 unless explicitly 6', () => {
    expect(normalizeLatencyIPVersion(6)).toBe(6)
    expect(normalizeLatencyIPVersion(4)).toBe(4)
    expect(normalizeLatencyIPVersion(0)).toBe(4)
    expect(normalizeLatencyIPVersion(undefined)).toBe(4)
    expect(normalizeLatencyIPVersion(99)).toBe(4)
  })

  it('filters latency targets by stack and protocol filters', () => {
    const targets = [
      makeTarget(1, 4, 'icmp'),
      makeTarget(2, 4, 'tcp'),
      makeTarget(3, 6, 'icmp'),
      makeTarget(4, 6, 'tcp'),
      makeTarget(5, undefined, 'icmp'),
    ]

    expect(filterLatencyTargets(targets, 'all', 'all').map((t) => t.task_id)).toEqual([1, 2, 3, 4, 5])
    expect(filterLatencyTargets(targets, 4, 'all').map((t) => t.task_id)).toEqual([1, 2, 5])
    expect(filterLatencyTargets(targets, 6, 'all').map((t) => t.task_id)).toEqual([3, 4])
    expect(filterLatencyTargets(targets, 'all', 'icmp').map((t) => t.task_id)).toEqual([1, 3, 5])
    expect(filterLatencyTargets(targets, 'all', 'tcp').map((t) => t.task_id)).toEqual([2, 4])
    expect(filterLatencyTargets(targets, 6, 'tcp').map((t) => t.task_id)).toEqual([4])
    expect(filterLatencyTargets(targets.slice(0, 2), 6, 'tcp')).toEqual([])
  })
})

// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import LatencyView from './LatencyView.vue'
import type { LatencyOverview, LatencyResult, PublicNode } from '../types'

vi.mock('../components/PublicHeader.vue', () => ({
  default: defineComponent({
    name: 'PublicHeaderStub',
    setup() {
      return () => h('header', { class: 'public-header-stub' })
    },
  }),
}))

vi.mock('../components/TimeSeriesChart.vue', () => ({
  default: defineComponent({
    name: 'TimeSeriesChartStub',
    props: {
      labels: { type: Array, default: () => [] },
      series: { type: Array, default: () => [] },
      unit: { type: String, default: '' },
      max: { type: Number, default: undefined },
      ariaLabel: { type: String, default: '' },
    },
    setup() {
      return () => h('div', { class: 'time-series-chart-stub' })
    },
  }),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeNode(id: string, name: string): PublicNode {
  const now = new Date().toISOString()
  return {
    id,
    role: 'monitor',
    latency_enabled: true,
    name,
    region: 'Tokyo',
    country_code: 'JP',
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
}

function makeOverview(withLatest = true): LatencyOverview {
  const now = new Date().toISOString()
  return {
    probe_nodes: [
      { id: 'probe-1', name: 'HK Probe', region: 'Hong Kong', tags: [], online: true },
      { id: 'probe-2', name: 'SG Probe', region: 'Singapore', tags: [], online: true },
    ],
    targets: [
      { task_id: 1, node: makeNode('target-1', 'Tokyo Edge'), type: 'icmp', interval_seconds: 30, samples: 3 },
      { task_id: 2, node: makeNode('target-2', 'LA Edge'), type: 'tcp', interval_seconds: 30, samples: 3 },
    ],
    latest: withLatest
      ? [
          {
            probe_node_id: 'probe-1',
            target_node_id: 'target-1',
            task_id: 1,
            collected_at: now,
            received_at: now,
            success: true,
            latency_ms: 34.2,
            loss_percent: 0,
          },
          {
            probe_node_id: 'probe-2',
            target_node_id: 'target-2',
            task_id: 2,
            collected_at: now,
            received_at: now,
            success: true,
            latency_ms: 112.5,
            loss_percent: 0,
          },
        ]
      : [],
    offline_after_ms: 90_000,
  }
}

function makeHistory(probeNodeID = 'probe-1', targetNodeID = 'target-1'): LatencyResult[] {
  const now = new Date().toISOString()
  return [
    {
      probe_node_id: probeNodeID,
      target_node_id: targetNodeID,
      task_id: 1,
      collected_at: now,
      received_at: now,
      success: true,
      latency_ms: 35,
      loss_percent: 0,
    },
  ]
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

async function flushAsync() {
  for (let index = 0; index < 16; index++) {
    await Promise.resolve()
    await nextTick()
  }
}

function setVisibilityState(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => state,
  })
  document.dispatchEvent(new Event('visibilitychange'))
}

describe('LatencyView polling and history refresh', () => {
  let container: HTMLDivElement | null = null
  let unmountApp: (() => void) | null = null

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-23T06:00:00Z'))
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => 'visible',
    })
  })

  afterEach(() => {
    unmountApp?.()
    unmountApp = null
    container?.remove()
    container = null
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  async function mountView() {
    container = document.createElement('div')
    document.body.appendChild(container)
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/latency', component: LatencyView },
        { path: '/nodes/:id', component: defineComponent({ render: () => h('div') }) },
      ],
    })
    await router.push('/latency')
    await router.isReady()
    const app = createApp(LatencyView)
    app.use(createPinia())
    app.use(router)
    app.mount(container)
    unmountApp = () => app.unmount()
    await flushAsync()
    return container
  }

  it('does not overlap polling requests when slow responses exceed 15s', async () => {
    let latencyCalls = 0
    let historyCalls = 0
    const slowOverview = deferred<Response>()

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/public/latency') {
        latencyCalls++
        if (latencyCalls === 2) {
          return slowOverview.promise
        }
        return Promise.resolve(jsonResponse({ data: makeOverview() }))
      }
      if (url.startsWith('/api/v1/public/latency/history?')) {
        historyCalls++
        return Promise.resolve(jsonResponse({ data: makeHistory() }))
      }
      throw new Error(`Unexpected URL: ${url}`)
    }))

    const root = await mountView()
    expect(latencyCalls).toBe(1)
    expect(historyCalls).toBe(1)
    expect(root.querySelector('.latency-mobile-matrix .mobile-probe-node')).not.toBeNull()

    // Trigger the first 15s refresh, whose /api/v1/public/latency response hangs for 40s.
    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(latencyCalls).toBe(2)

    // Advancing 40s while request #2 is still in flight must not queue overlapping requests.
    await vi.advanceTimersByTimeAsync(40_000)
    await flushAsync()
    expect(latencyCalls).toBe(2)
    expect(historyCalls).toBe(1)

    slowOverview.resolve(jsonResponse({ data: makeOverview() }))
    await flushAsync()
    // 55s elapsed since initial history load (<60s for 24h), so history is still skipped here.
    expect(historyCalls).toBe(1)

    // Next timer fires 15s after completion (total 70s >= 60s), refreshing both matrix and history.
    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(latencyCalls).toBe(3)
    expect(historyCalls).toBe(2)
  })

  it('throttles background history refreshes to 60s on >=24h and 30s on <=6h while keeping user actions immediate', async () => {
    const historyUrls: string[] = []
    let latencyCalls = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/public/latency') {
        latencyCalls++
        return Promise.resolve(jsonResponse({ data: makeOverview() }))
      }
      if (url.startsWith('/api/v1/public/latency/history?')) {
        historyUrls.push(url)
        return Promise.resolve(jsonResponse({ data: makeHistory() }))
      }
      throw new Error(`Unexpected URL: ${url}`)
    }))

    const root = await mountView()
    expect(latencyCalls).toBe(1)
    expect(historyUrls).toHaveLength(1)
    expect(historyUrls[0]).toContain('hours=24')

    // At 15s, 30s, 45s on 24H window: matrix refreshes, history does NOT.
    for (const expectedLatencyCalls of [2, 3, 4]) {
      await vi.advanceTimersByTimeAsync(15_000)
      await flushAsync()
      expect(latencyCalls).toBe(expectedLatencyCalls)
      expect(historyUrls).toHaveLength(1)
    }

    // At 60s on 24H window: history refreshes!
    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(latencyCalls).toBe(5)
    expect(historyUrls).toHaveLength(2)

    // User clicks 6H window button -> immediate history fetch.
    const sixHourButton = [...root.querySelectorAll('.latency-window button')].find((btn) => btn.textContent?.trim() === '6H') as HTMLButtonElement
    expect(sixHourButton).toBeDefined()
    sixHourButton.click()
    await flushAsync()
    expect(historyUrls).toHaveLength(3)
    expect(historyUrls[2]).toContain('hours=6')

    // Next 15s tick on 6H: skipped (<30s). Following 15s tick (+30s): refreshed!
    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(historyUrls).toHaveLength(3)

    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(historyUrls).toHaveLength(4)

    // User selects another route -> immediate history fetch even before 30s!
    const matrixButtons = [...root.querySelectorAll('.latency-matrix tbody button:not([disabled])')] as HTMLButtonElement[]
    expect(matrixButtons.length).toBeGreaterThanOrEqual(2)
    matrixButtons[1]!.click()
    await flushAsync()
    expect(historyUrls).toHaveLength(5)
    expect(historyUrls[4]).toContain('probe_node_id=probe-2')
    expect(historyUrls[4]).toContain('target_node_id=target-2')
  })

  it('loads history immediately when auto-selecting the first route on a background refresh', async () => {
    let latencyCalls = 0
    let historyCalls = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/public/latency') {
        latencyCalls++
        return Promise.resolve(jsonResponse({ data: makeOverview(latencyCalls > 1) }))
      }
      if (url.startsWith('/api/v1/public/latency/history?')) {
        historyCalls++
        return Promise.resolve(jsonResponse({ data: makeHistory() }))
      }
      throw new Error(`Unexpected URL: ${url}`)
    }))

    await mountView()
    expect(latencyCalls).toBe(1)
    expect(historyCalls).toBe(0)

    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(latencyCalls).toBe(2)
    expect(historyCalls).toBe(1)
  })

  it('pauses polling while hidden, refreshes on return to visible, and cleans up on unmount', async () => {
    let latencyCalls = 0
    let historyCalls = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/public/latency') {
        latencyCalls++
        return Promise.resolve(jsonResponse({ data: makeOverview() }))
      }
      if (url.startsWith('/api/v1/public/latency/history?')) {
        historyCalls++
        return Promise.resolve(jsonResponse({ data: makeHistory() }))
      }
      throw new Error(`Unexpected URL: ${url}`)
    }))

    await mountView()
    expect(latencyCalls).toBe(1)
    expect(historyCalls).toBe(1)

    // Brief 5s tab hide -> return to visible refreshes matrix immediately without re-querying fresh 24h history.
    setVisibilityState('hidden')
    await vi.advanceTimersByTimeAsync(5_000)
    await flushAsync()
    expect(latencyCalls).toBe(1)
    setVisibilityState('visible')
    await flushAsync()
    expect(latencyCalls).toBe(2)
    expect(historyCalls).toBe(1)

    // Hide document and advance 90s: zero background polls.
    setVisibilityState('hidden')
    await vi.advanceTimersByTimeAsync(90_000)
    await flushAsync()
    expect(latencyCalls).toBe(2)
    expect(historyCalls).toBe(1)

    // Return to visible after 90s: immediate matrix + history refresh, and schedule restarts.
    setVisibilityState('visible')
    await flushAsync()
    expect(latencyCalls).toBe(3)
    expect(historyCalls).toBe(2)

    await vi.advanceTimersByTimeAsync(15_000)
    await flushAsync()
    expect(latencyCalls).toBe(4)
    expect(historyCalls).toBe(2)

    // Unmount cleans up timer and visibilitychange listener.
    unmountApp?.()
    unmountApp = null

    setVisibilityState('hidden')
    setVisibilityState('visible')
    await vi.advanceTimersByTimeAsync(60_000)
    await flushAsync()
    expect(latencyCalls).toBe(4)
    expect(historyCalls).toBe(2)
  })

  it('guards manual Retry against in-flight overlap and always refreshes history immediately on Retry', async () => {
    let latencyCalls = 0
    let historyCalls = 0
    let failHistoryOnce = false
    const retryOverview = deferred<Response>()

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/v1/public/latency') {
        latencyCalls++
        if (latencyCalls === 2) {
          return retryOverview.promise
        }
        return Promise.resolve(jsonResponse({ data: makeOverview() }))
      }
      if (url.startsWith('/api/v1/public/latency/history?')) {
        historyCalls++
        if (failHistoryOnce) {
          failHistoryOnce = false
          return Promise.resolve(jsonResponse({ error: { code: 'db_busy', message: 'History timed out' } }, 500))
        }
        return Promise.resolve(jsonResponse({ data: makeHistory() }))
      }
      throw new Error(`Unexpected URL: ${url}`)
    }))

    const root = await mountView()
    expect(latencyCalls).toBe(1)
    expect(historyCalls).toBe(1)

    // Trigger a window switch that fails so the Retry banner appears while last load was <60s ago.
    failHistoryOnce = true
    const sevenDayButton = [...root.querySelectorAll('.latency-window button')].find((btn) => btn.textContent?.trim() === '7D') as HTMLButtonElement
    sevenDayButton.click()
    await flushAsync()
    expect(historyCalls).toBe(2)

    const retryButton = root.querySelector('.notice.error button') as HTMLButtonElement
    expect(retryButton).not.toBeNull()

    // Click Retry twice rapidly while /api/v1/public/latency is in flight -> only 1 request!
    retryButton.click()
    retryButton.click()
    await flushAsync()
    expect(latencyCalls).toBe(2)

    retryOverview.resolve(jsonResponse({ data: makeOverview() }))
    await flushAsync()
    expect(latencyCalls).toBe(2)
    expect(historyCalls).toBe(3)
    expect(root.querySelector('.notice.error')).toBeNull()
  })
})

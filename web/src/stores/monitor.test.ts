import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useMonitorStore } from './monitor'
import type { NodeSnapshot } from '../types'

describe('monitor presence', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('marks stale samples offline using the server threshold', () => {
    const store = useMonitorStore()
    store.offlineAfterMS = 10_000
    const snapshot = (receivedAt: string): NodeSnapshot => ({
      node: {
        id: receivedAt, role: 'monitor', latency_enabled: false, name: 'edge', tags: [], hidden: false, weight: 0, price: 0,
        currency: 'USD', billing_cycle_days: 30, auto_renewal: false,
        traffic_limit: 0, traffic_limit_type: 'sum', traffic_reset_day: 1,
        created_at: receivedAt, updated_at: receivedAt, online: true,
      },
      metric: {
        sequence: 1, collected_at: receivedAt, received_at: receivedAt,
        cpu: 0, load1: 0, load5: 0, load15: 0, memory_total: 1, memory_used: 0,
        swap_total: 0, swap_used: 0, disk_total: 1, disk_used: 0,
        net_rx_bps: 0, net_tx_bps: 0, net_rx_bytes: 0, net_tx_bytes: 0,
        tcp_connections: 0, udp_connections: 0, processes: 0, uptime_seconds: 1,
      },
    })
    store.nodes = [snapshot(new Date(Date.now() - 20_000).toISOString()), snapshot(new Date().toISOString())]
    store.refreshPresence()
    expect(store.nodes.map((item) => item.node.online)).toEqual([false, true])
  })
})

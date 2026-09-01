import { defineStore } from 'pinia'
import { api, socketURL, unwrap } from '../api'
import type { MetricSample, NodeSnapshot, ProbeTask } from '../types'

export const useMonitorStore = defineStore('monitor', {
  state: () => ({
    nodes: [] as NodeSnapshot[],
    probes: [] as ProbeTask[],
    loading: false,
    lastUpdate: null as Date | null,
    socket: null as WebSocket | null,
    reconnectTimer: 0,
    presenceTimer: 0,
    offlineAfterMS: 90_000,
  }),
  getters: {
    onlineCount: (state) => state.nodes.filter((item) => item.node.online).length,
    groups: (state) => [...new Set(state.nodes.map((item) => item.node.group).filter(Boolean) as string[])].sort(),
  },
  actions: {
    async load() {
      this.loading = true
      try {
        const [nodes, probes] = await Promise.all([
          api<{ data: NodeSnapshot[] }>('/api/v1/public/nodes'),
          api<{ data: ProbeTask[] }>('/api/v1/public/probes'),
        ])
        this.nodes = unwrap(nodes)
        this.probes = unwrap(probes)
        this.lastUpdate = new Date()
      } finally {
        this.loading = false
      }
    },
    connect() {
      this.disconnect()
      const socket = new WebSocket(socketURL('/api/v1/public/live'))
      this.socket = socket
      socket.onmessage = (event) => {
        const message = JSON.parse(String(event.data)) as {
          type: string
          node_id?: string
          sample?: MetricSample
          samples?: Record<string, MetricSample>
          offline_after_ms?: number
        }
        if (message.type === 'snapshot' && message.samples) {
          if (message.offline_after_ms && message.offline_after_ms >= 10_000) this.offlineAfterMS = message.offline_after_ms
          for (const [id, sample] of Object.entries(message.samples)) this.applySample(id, sample)
        } else if (message.type === 'sample' && message.node_id && message.sample) {
          this.applySample(message.node_id, message.sample)
        }
        this.lastUpdate = new Date()
      }
      socket.onclose = () => {
        if (this.socket !== socket) return
        this.socket = null
        this.nodes = []
        this.reconnectTimer = window.setTimeout(async () => {
          try {
            await this.load()
          } catch {
            this.nodes = []
          }
          if (!this.socket) this.connect()
        }, 3000)
      }
      this.presenceTimer = window.setInterval(() => this.refreshPresence(), 1000)
    },
    disconnect() {
      window.clearTimeout(this.reconnectTimer)
      window.clearInterval(this.presenceTimer)
      if (this.socket) {
        const old = this.socket
        this.socket = null
        old.close()
      }
    },
    applySample(nodeID: string, sample: MetricSample) {
      const target = this.nodes.find((item) => item.node.id === nodeID)
      if (!target) return
      target.metric = sample
      const received = new Date(sample.received_at ?? sample.collected_at).getTime()
      target.node.online = Date.now() - received < this.offlineAfterMS
    },
    refreshPresence() {
      const now = Date.now()
      for (const target of this.nodes) {
        if (!target.metric) {
          target.node.online = false
          continue
        }
        const received = new Date(target.metric.received_at ?? target.metric.collected_at).getTime()
        target.node.online = now - received < this.offlineAfterMS
      }
    },
  },
})

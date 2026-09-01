<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import PublicHeader from '../components/PublicHeader.vue'
import NodeCard from '../components/NodeCard.vue'
import { api } from '../api'
import { dateTime } from '../format'
import type { MetricSample, NodeSnapshot } from '../types'
import { useLocale } from '../i18n'

const route = useRoute()
const token = String(route.params.token)
const nodes = ref<NodeSnapshot[]>([])
const expiresAt = ref('')
const error = ref('')
const { text } = useLocale()
let socket: WebSocket | null = null

onMounted(async () => {
  try {
    const result = await api<{ expires_at: string; nodes: NodeSnapshot[] }>(`/api/v1/public/share/${encodeURIComponent(token)}`)
    nodes.value = result.nodes; expiresAt.value = result.expires_at
    const url = new URL(`/api/v1/public/share/${encodeURIComponent(token)}/live`, location.href)
    url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    socket = new WebSocket(url)
    socket.onmessage = (event) => {
      const update = JSON.parse(String(event.data)) as { type: string; node_id?: string; sample?: MetricSample; samples?: Record<string, MetricSample> }
      const apply = (id: string, sample: MetricSample) => { const target = nodes.value.find((item) => item.node.id === id); if (target) { target.metric = sample; target.node.online = true } }
      if (update.node_id && update.sample) apply(update.node_id, update.sample)
      if (update.samples) Object.entries(update.samples).forEach(([id, sample]) => apply(id, sample))
    }
  } catch (reason) { error.value = reason instanceof Error ? reason.message : text('分享链接不可用。', 'Share link unavailable.') }
})
onBeforeUnmount(() => socket?.close())
</script>

<template><div class="public-layout"><PublicHeader /><main class="overview share-page"><section class="share-heading"><span class="eyebrow">{{ text('只读分享', 'Read-only share') }}</span><h1>{{ text('节点状态', 'Shared nodes') }}</h1><p v-if="expiresAt">{{ text('有效期至', 'Access expires') }} {{ dateTime(expiresAt) }}</p></section><div v-if="error" class="empty-state"><span>×</span><h3>{{ text('分享链接不可用', 'Share link unavailable') }}</h3><p>{{ error }}</p></div><div v-else class="node-grid"><NodeCard v-for="item in nodes" :key="item.node.id" :snapshot="item" :to="`/share/${token}/nodes/${item.node.id}`" /></div></main></div></template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { dateTime } from '../../format'
import type { AuditEntry } from '../../types'
import { useLocale } from '../../i18n'

const entries = ref<AuditEntry[]>([])
const { text } = useLocale()
async function load() { entries.value = unwrap(await api<{ data: AuditEntry[] }>('/api/v1/admin/audit?limit=500')) }
onMounted(load)
</script>

<template><section class="admin-page"><header class="page-title"><div><span class="eyebrow">{{ text('操作记录', 'Operation history') }}</span><h1>{{ text('审计日志', 'Audit log') }}</h1><p>{{ text('按时间倒序记录登录、节点注册和后台修改。', 'Authentication, enrollment, and administration changes in reverse time order.') }}</p></div><button class="secondary-action" @click="load">{{ text('刷新', 'Refresh') }}</button></header><div class="audit-stream"><div class="audit-header"><span>{{ text('时间', 'Time') }}</span><span>{{ text('操作者', 'Actor') }}</span><span>{{ text('操作', 'Action') }}</span><span>{{ text('对象 / 详情', 'Target / detail') }}</span></div><article v-for="entry in entries" :key="entry.id"><time>{{ dateTime(entry.occurred_at) }}</time><b>{{ entry.actor }}</b><code>{{ entry.action }}</code><div><span>{{ entry.target }}</span><small>{{ entry.detail }}</small></div></article></div></section></template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { RouterView } from 'vue-router'
import { watchBrowserLocale } from './i18n'

let stopWatchingBrowserLocale: (() => void) | undefined

onMounted(() => {
  const saved = localStorage.getItem('appearance') ?? 'system'
  document.documentElement.dataset.appearance = saved
  stopWatchingBrowserLocale = watchBrowserLocale()
})

onBeforeUnmount(() => stopWatchingBrowserLocale?.())
</script>

<template>
  <RouterView />
</template>

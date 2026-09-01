<script setup lang="ts">
import { computed } from 'vue'
import { systemLogoSource } from '../nodeAssets'

const props = defineProps<{ os?: string }>()
const logo = computed(() => systemLogoSource(props.os))
</script>

<template>
  <img class="node-os-logo" :src="logo" alt="" aria-hidden="true" decoding="async" />
</template>

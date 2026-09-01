<script setup lang="ts">
import { computed } from 'vue'
import { countryFlagSource } from '../nodeAssets'

const props = defineProps<{ countryCode?: string }>()
const flag = computed(() => countryFlagSource(props.countryCode))
</script>

<template>
  <img v-if="flag" class="node-flag" :src="flag" alt="" aria-hidden="true" decoding="async" />
</template>

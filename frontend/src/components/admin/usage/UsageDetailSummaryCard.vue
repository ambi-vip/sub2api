<template>
  <div class="rounded-xl border border-gray-200 px-3 py-3 dark:border-dark-700">
    <div class="text-xs text-gray-500 dark:text-gray-400">{{ label }}</div>
    <div class="mt-1 text-base font-semibold tabular-nums text-gray-900 dark:text-white">{{ value }}</div>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; value: string }>()
</script>

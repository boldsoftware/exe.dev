<template>
  <div class="metric-bar">
    <div class="metric-bar-header">
      <span class="metric-bar-label">{{ label }}</span>
      <span class="metric-bar-value">{{ displayValue }}</span>
    </div>
    <div class="metric-bar-track">
      <div class="metric-bar-fill" :style="{ width: percent + '%' }" :class="barClass"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  label: string
  used: number
  total: number
  format?: 'bytes' | 'percent'
  warningAt?: number
  dangerAt?: number
}>(), {
  warningAt: 70,
  dangerAt: 90,
})

const percent = computed(() => {
  if (props.total === 0) return 0
  return Math.min(100, (props.used / props.total) * 100)
})

const barClass = computed(() => {
  const p = percent.value
  if (p >= props.dangerAt) return 'bar-danger'
  if (p >= props.warningAt) return 'bar-warning'
  return 'bar-normal'
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i]
}

const displayValue = computed(() => {
  if (props.format === 'percent') {
    return percent.value.toFixed(1) + '%'
  }
  return formatBytes(props.used) + ' / ' + formatBytes(props.total)
})
</script>

<style scoped>
.metric-bar {
  margin-bottom: 0.625rem;
}

.metric-bar-header {
  display: flex;
  justify-content: space-between;
  font-size: 0.75rem;
  margin-bottom: 0.35rem;
}

.metric-bar-label {
  color: var(--text-color-secondary);
  font-weight: 500;
}

.metric-bar-value {
  color: var(--text-color);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.metric-bar-value {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
}

.metric-bar-track {
  height: 6px;
  background: var(--surface-overlay);
  border-radius: 99px;
  overflow: hidden;
}

.metric-bar-fill {
  height: 100%;
  border-radius: 99px;
  transition: width 0.4s ease;
}

.bar-normal {
  background: var(--primary-color);
}

.bar-warning {
  background: linear-gradient(90deg, var(--yellow-500), var(--yellow-400));
}

.bar-danger {
  background: linear-gradient(90deg, var(--red-500), var(--red-400));
}
</style>

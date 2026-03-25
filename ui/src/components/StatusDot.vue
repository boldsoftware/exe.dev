<template>
  <span class="status-dot" :class="status" :title="status" role="status" :aria-label="status"></span>
</template>

<script setup lang="ts">
defineProps<{ status: string }>()
</script>

<style scoped>
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.running { background: var(--success-color); box-shadow: 0 0 4px rgba(34, 197, 94, 0.4); }
.stopped { background: var(--text-color-muted); }
.creating { background: var(--warning-color); animation: pulse 1.5s ease-in-out infinite; }
.error { background: var(--danger-color); }

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}
</style>

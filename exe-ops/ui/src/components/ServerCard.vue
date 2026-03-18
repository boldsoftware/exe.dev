<template>
  <router-link :to="`/servers/${server.name}`" class="server-card">
    <div class="card-header">
      <div class="card-title-row">
        <span class="status-indicator" :class="isOnline ? 'online' : 'offline'"></span>
        <h3>{{ server.name }}</h3>
      </div>
      <span class="card-location" v-if="server.region">
        {{ server.region }}<span class="loc-sep">/</span>{{ server.env }}
      </span>
    </div>

    <div class="card-metrics">
      <MetricBar label="CPU" :used="server.cpu_percent" :total="100" format="percent" />
      <MetricBar label="Memory" :used="server.mem_used" :total="server.mem_total" format="bytes" :warningAt="90" :dangerAt="96" />
      <MetricBar label="Disk" :used="server.disk_used" :total="server.disk_total" format="bytes" />
    </div>

    <div class="card-footer">
      <div class="net-stats">
        <span class="net-stat">
          <i class="pi pi-arrow-up"></i>{{ formatBytes(server.net_send) }}
        </span>
        <span class="net-stat">
          <i class="pi pi-arrow-down"></i>{{ formatBytes(server.net_recv) }}
        </span>
      </div>
      <TagList :tags="server.tags" />
    </div>
  </router-link>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ServerSummary } from '../api/client'
import MetricBar from './MetricBar.vue'
import TagList from './TagList.vue'

const props = defineProps<{
  server: ServerSummary
}>()

const isOnline = computed(() => {
  const ago = Date.now() - new Date(props.server.last_seen).getTime()
  return ago < 120_000
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i]
}
</script>

<style scoped>
.server-card {
  display: block;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1rem;
  text-decoration: none;
  color: var(--text-color);
  transition: border-color 0.2s;
}

.server-card:hover {
  border-color: rgba(72, 209, 204, 0.5);
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 1rem;
}

.card-title-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.status-indicator {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.status-indicator.online {
  background: var(--green-500);
  box-shadow: 0 0 6px rgba(63, 185, 80, 0.4);
}

.status-indicator.offline {
  background: var(--text-color-muted);
}

.card-header h3 {
  font-size: 0.95rem;
  font-weight: 600;
  letter-spacing: -0.01em;
}

.card-location {
  font-size: 0.7rem;
  color: var(--text-color-muted);
  background: var(--surface-overlay);
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  font-family: 'JetBrains Mono', monospace;
}

.loc-sep {
  margin: 0 0.15rem;
  opacity: 0.4;
}

.card-metrics {
  margin-bottom: 1rem;
}

.card-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding-top: 0.75rem;
  border-top: 1px solid var(--surface-border);
}

.net-stats {
  display: flex;
  gap: 0.75rem;
}

.net-stat {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
}

.net-stat i {
  font-size: 0.65rem;
}
</style>

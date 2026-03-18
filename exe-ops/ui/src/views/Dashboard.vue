<template>
  <div class="dashboard">
    <div class="page-header" v-if="servers.length > 0">
      <div>
        <h1>Dashboard</h1>
        <p class="page-subtitle">Fleet overview</p>
      </div>
      <div class="header-right">
        <router-link v-if="alertCount > 0" to="/alerts" class="alert-link">
          <i class="pi pi-exclamation-triangle"></i>
          Alerts
        </router-link>
        <div class="filter-groups" v-if="uniqueEnvs.length > 1">
          <div class="filter-group">
            <span class="filter-label">Env</span>
            <div class="filter-buttons">
              <button
                v-for="e in uniqueEnvs"
                :key="'env-' + e"
                class="filter-btn"
                :class="{ active: activeEnvs.has(e) }"
                @click="toggleEnvFilter(e)"
              >{{ e }}</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Summary Stats -->
    <div class="stats-header" v-if="servers.length > 0">
      <div class="stats-row">
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--primary-50); color: var(--primary-color);">
            <i class="pi pi-server"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ filteredServers.length }}</span>
            <span class="stat-label">Total Servers</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--green-subtle); color: var(--green-400);">
            <i class="pi pi-check-circle"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ onlineCount }}</span>
            <span class="stat-label">Online</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--red-subtle); color: var(--red-400);">
            <i class="pi pi-times-circle"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ filteredServers.length - onlineCount }}</span>
            <span class="stat-label">Offline</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--yellow-subtle); color: var(--yellow-400);">
            <i class="pi pi-microchip"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ avgCpu }}%</span>
            <span class="stat-label">Avg CPU</span>
          </div>
        </div>
        <div class="stat-card" v-if="capacitySummary && capacitySummary.total_capacity > 0">
          <div class="stat-icon" style="background: var(--primary-50); color: var(--primary-color);">
            <i class="pi pi-box"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ capacityPct }}%</span>
            <span class="stat-label">Capacity ({{ capacityLabel }})</span>
          </div>
        </div>
      </div>
    </div>

    <!-- World Map -->
    <WorldMap
      v-if="servers.length > 0"
      :servers="envFilteredServers"
      :selected-region="selectedRegion"
      @select-region="toggleRegion"
    />

    <!-- Region filter badge -->
    <div v-if="selectedRegion" class="filter-badge">
      <i class="pi pi-map-marker"></i>
      <span>Filtering by region: <strong>{{ selectedRegion }}</strong></span>
      <button class="filter-clear" @click="selectedRegion = null">
        <i class="pi pi-times"></i>
      </button>
    </div>

    <div v-if="error" class="message-banner message-error">
      <i class="pi pi-exclamation-triangle"></i>
      <span>{{ error }}</span>
    </div>

    <div v-if="loading && servers.length === 0" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i>
      <span>Loading servers...</span>
    </div>

    <!-- Empty state: welcome + getting started -->
    <div v-else-if="!loading && servers.length === 0" class="welcome">
      <div class="welcome-icon">
        <i class="pi pi-server"></i>
      </div>
      <h2 class="welcome-title">Welcome to exe-ops</h2>
      <p class="welcome-subtitle">No agents connected yet. Follow the steps below to get your first agent reporting.</p>

      <div class="steps">
        <div class="step">
          <span class="step-number">1</span>
          <div class="step-body">
            <strong>Download the agent</strong>
            <p>Grab the binary from the server:</p>
            <code class="step-code">curl -fsSL http://&lt;server&gt;/api/v1/agent/binary -o exe-ops-agent &amp;&amp; chmod +x exe-ops-agent</code>
          </div>
        </div>
        <div class="step">
          <span class="step-number">2</span>
          <div class="step-body">
            <strong>Run the agent</strong>
            <p>Point it at this server:</p>
            <code class="step-code">./exe-ops-agent --server http://&lt;server&gt;</code>
          </div>
        </div>
        <div class="step">
          <span class="step-number">3</span>
          <div class="step-body">
            <strong>Check back here</strong>
            <p>Once the agent connects, it will appear on this dashboard automatically.</p>
          </div>
        </div>
      </div>
    </div>

    <div v-else class="collapsible-section">
      <button class="section-toggle" @click="toggleServersOpen">
        <i class="pi" :class="serversOpen ? 'pi-chevron-down' : 'pi-chevron-right'"></i>
        <span class="section-toggle-title">Servers</span>
        <span class="section-toggle-count">{{ filteredServers.length }}</span>
      </button>
      <div class="server-grid" v-if="serversOpen">
        <ServerCard v-for="server in filteredServers" :key="server.name" :server="server" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { fetchServers, fetchExeletCapacitySummary, type ServerSummary, type StatusEvent, type ReportEvent, type ExeletCapacitySummary } from '../api/client'
import ServerCard from '../components/ServerCard.vue'
import WorldMap from '../components/WorldMap.vue'

const servers = ref<ServerSummary[]>([])
const loading = ref(true)
const error = ref('')
const selectedRegion = ref<string | null>(null)
const activeEnvs = reactive(new Set<string>())
const serversOpen = ref(sessionStorage.getItem('exe-ops-dashboard-servers') !== 'collapsed')
const capacitySummary = ref<ExeletCapacitySummary | null>(null)
let pollTimer: ReturnType<typeof setInterval> | null = null

try {
  const saved = sessionStorage.getItem('exe-ops-env-filter')
  if (saved) for (const e of JSON.parse(saved)) activeEnvs.add(e)
} catch {}

watch(activeEnvs, () => {
  if (activeEnvs.size > 0) {
    sessionStorage.setItem('exe-ops-env-filter', JSON.stringify([...activeEnvs]))
  } else {
    sessionStorage.removeItem('exe-ops-env-filter')
  }
})
let eventSource: EventSource | null = null
let sseRetryTimer: ReturnType<typeof setTimeout> | null = null

function normalizeRegion(region: string): string {
  return region.replace(/\d+$/, '')
}

function toggleRegion(code: string | null) {
  selectedRegion.value = code
}

function toggleEnvFilter(value: string) {
  if (activeEnvs.has(value)) {
    activeEnvs.delete(value)
  } else {
    activeEnvs.add(value)
  }
}

function toggleServersOpen() {
  serversOpen.value = !serversOpen.value
  if (serversOpen.value) {
    sessionStorage.removeItem('exe-ops-dashboard-servers')
  } else {
    sessionStorage.setItem('exe-ops-dashboard-servers', 'collapsed')
  }
}

const uniqueEnvs = computed(() =>
  [...new Set(servers.value.map(s => s.env).filter(Boolean))].sort()
)

const envFilteredServers = computed(() => {
  if (activeEnvs.size === 0) return servers.value
  return servers.value.filter(s => activeEnvs.has(s.env))
})

const filteredServers = computed(() => {
  if (!selectedRegion.value) return envFilteredServers.value
  return envFilteredServers.value.filter(s => normalizeRegion(s.region) === selectedRegion.value)
})

const onlineCount = computed(() => {
  return filteredServers.value.filter(s => {
    const ago = Date.now() - new Date(s.last_seen).getTime()
    return ago < 120_000
  }).length
})

const avgCpu = computed(() => {
  const list = filteredServers.value
  if (list.length === 0) return '0'
  const sum = list.reduce((a, s) => a + s.cpu_percent, 0)
  return (sum / list.length).toFixed(1)
})

const capacityPct = computed(() => {
  if (!capacitySummary.value || capacitySummary.value.total_capacity === 0) return '0.0'
  return ((capacitySummary.value.total_instances / capacitySummary.value.total_capacity) * 100).toFixed(1)
})

const capacityLabel = computed(() => {
  if (!capacitySummary.value) return 'Capacity'
  return `${capacitySummary.value.total_instances} / ${capacitySummary.value.total_capacity}`
})

async function loadCapacity() {
  try {
    const env = activeEnvs.size === 1 ? [...activeEnvs][0] : undefined
    const region = selectedRegion.value || undefined
    capacitySummary.value = await fetchExeletCapacitySummary(env, region)
  } catch {
    // Capacity is non-critical; fail silently.
  }
}

const alertCount = computed(() => {
  let count = 0
  for (const s of filteredServers.value) {
    if (s.cpu_percent > 90) count++
    if (s.mem_total > 0 && (s.mem_used / s.mem_total) * 100 > 90) count++
    if (s.disk_total > 0 && (s.disk_used / s.disk_total) * 100 > 90) count++
    if (Date.now() - new Date(s.last_seen).getTime() > 120_000) count++
  }
  return count
})

async function load() {
  try {
    servers.value = await fetchServers()
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load servers'
  } finally {
    loading.value = false
  }
}

function connectSSE() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }

  eventSource = new EventSource('/api/v1/events')

  eventSource.addEventListener('status', (e: MessageEvent) => {
    const data: StatusEvent = JSON.parse(e.data)
    const server = servers.value.find(s => s.name === data.name)
    if (server) {
      server.last_seen = data.online ? new Date().toISOString() : '1970-01-01T00:00:00Z'
    }
  })

  eventSource.addEventListener('report', (e: MessageEvent) => {
    const data: ReportEvent = JSON.parse(e.data)
    const server = servers.value.find(s => s.name === data.name)
    if (server) {
      server.cpu_percent = data.cpu_percent
      server.mem_total = data.mem_total
      server.mem_used = data.mem_used
      server.disk_total = data.disk_total
      server.disk_used = data.disk_used
      server.net_send = data.net_send
      server.net_recv = data.net_recv
      server.last_seen = new Date().toISOString()
    }
  })

  eventSource.addEventListener('connected', () => {
    // SSE connected: stop polling fallback.
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  })

  eventSource.onerror = () => {
    eventSource?.close()
    eventSource = null
    // Fall back to polling, retry SSE after 10s.
    if (!pollTimer) {
      pollTimer = setInterval(load, 30000)
    }
    sseRetryTimer = setTimeout(connectSSE, 10000)
  }
}

watch([() => [...activeEnvs], selectedRegion], () => {
  loadCapacity()
})

onMounted(() => {
  load()
  loadCapacity()
  connectSSE()
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (eventSource) eventSource.close()
  if (sseRetryTimer) clearTimeout(sseRetryTimer)
})
</script>

<style scoped>
/* ── Summary Stats ── */
.stats-header {
  margin-bottom: 1.5rem;
}

.stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 1rem;
}

.stat-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1.25rem 1.25rem;
  display: flex;
  align-items: center;
  gap: 1rem;
}

.stat-icon {
  width: 44px;
  height: 44px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1rem;
  flex-shrink: 0;
}

.stat-body {
  display: flex;
  flex-direction: column;
}

.stat-value {
  font-size: 1.75rem;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1;
}

.stat-label {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  margin-top: 0.375rem;
}

/* ── Environment Filter ── */
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 1.5rem;
}

.page-header h1 {
  font-size: 1.25rem;
  font-weight: 500;
  letter-spacing: -0.02em;
}

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.alert-link {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--red-400);
  background: var(--red-subtle);
  padding: 0.375rem 0.75rem;
  border-radius: 4px;
  border: 1px solid rgba(248, 81, 73, 0.2);
  text-decoration: none;
  transition: all 0.15s;
}

.alert-link:hover {
  border-color: var(--red-400);
}

.filter-groups {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.filter-group {
  display: flex;
  align-items: center;
  gap: 0.375rem;
}

.filter-label {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  margin-right: 0.125rem;
}

.filter-buttons {
  display: flex;
  gap: 0.25rem;
}

.filter-btn {
  padding: 0.25rem 0.5rem;
  font-size: 0.7rem;
  font-family: inherit;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.filter-btn:hover {
  border-color: var(--surface-border-bright);
  color: var(--text-color);
}

.filter-btn.active {
  background: var(--primary-50);
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.clear-filters-btn {
  padding: 0.25rem 0.5rem;
  font-size: 0.7rem;
  font-family: inherit;
  background: none;
  border: 1px solid transparent;
  border-radius: 3px;
  color: var(--text-color-muted);
  cursor: pointer;
  transition: color 0.15s;
}

.clear-filters-btn:hover {
  color: var(--text-color);
}

/* ── Collapsible Section ── */
.collapsible-section {
  margin-top: 0.5rem;
}

.section-toggle {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0;
  background: none;
  border: none;
  cursor: pointer;
  font-family: inherit;
  color: var(--text-color);
  margin-bottom: 0.75rem;
}

.section-toggle:hover {
  color: var(--primary-color);
}

.section-toggle .pi {
  font-size: 0.7rem;
  color: var(--text-color-muted);
  transition: color 0.15s;
}

.section-toggle:hover .pi {
  color: var(--primary-color);
}

.section-toggle-title {
  font-size: 0.9rem;
  font-weight: 500;
}

.section-toggle-count {
  font-size: 0.7rem;
  font-weight: 600;
  color: var(--text-color-muted);
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  padding: 0.1rem 0.4rem;
}

/* ── Server Grid ── */
.server-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
  gap: 1rem;
}

.loading-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  padding: 4rem 0;
  color: var(--text-color-muted);
  font-size: 0.85rem;
}

.message-banner {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  border-radius: 4px;
  margin-bottom: 1.5rem;
  font-size: 0.85rem;
}

.message-error {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid rgba(248, 81, 73, 0.2);
}

/* ── Filter Badge ── */
.filter-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.75rem;
  color: var(--primary-color);
  background: var(--primary-50);
  padding: 0.375rem 0.75rem;
  border-radius: 4px;
  border: 1px solid var(--primary-color);
  margin-bottom: 1rem;
}

.filter-clear {
  background: none;
  border: none;
  color: var(--primary-color);
  cursor: pointer;
  padding: 0;
  display: flex;
  align-items: center;
  font-size: 0.7rem;
  opacity: 0.7;
}

.filter-clear:hover {
  opacity: 1;
}

/* ── Welcome / Empty State ── */
.welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 4rem 1rem;
  max-width: 560px;
  margin: 0 auto;
}

.welcome-icon {
  width: 56px;
  height: 56px;
  border-radius: 8px;
  background: var(--primary-50);
  color: var(--primary-color);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.5rem;
  margin-bottom: 1.25rem;
}

.welcome-title {
  font-size: 1.25rem;
  font-weight: 600;
  margin-bottom: 0.5rem;
}

.welcome-subtitle {
  color: var(--text-color-secondary);
  font-size: 0.85rem;
  margin-bottom: 2rem;
  line-height: 1.6;
}

.steps {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
  width: 100%;
  text-align: left;
}

.step {
  display: flex;
  gap: 1rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1rem;
}

.step-number {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: var(--primary-50);
  color: var(--primary-color);
  font-size: 0.75rem;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  margin-top: 2px;
}

.step-body {
  min-width: 0;
}

.step-body strong {
  font-size: 0.85rem;
  display: block;
  margin-bottom: 0.25rem;
}

.step-body p {
  color: var(--text-color-secondary);
  font-size: 0.8rem;
  margin-bottom: 0.5rem;
}

.step-code {
  display: block;
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  color: var(--text-color);
  word-break: break-all;
  white-space: pre-wrap;
}

@media (max-width: 768px) {
  .stats-row {
    grid-template-columns: repeat(2, 1fr);
  }

  .server-grid {
    grid-template-columns: 1fr;
  }
}
</style>

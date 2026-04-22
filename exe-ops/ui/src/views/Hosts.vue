<template>
  <div class="hosts-view">
    <div class="page-header">
      <div>
        <h1>Hosts</h1>
        <p class="page-subtitle">Host-level metrics from Prometheus</p>
      </div>
    </div>

    <div v-if="error" class="message-banner message-error">
      <i class="pi pi-exclamation-triangle"></i>
      <span>{{ error }}</span>
    </div>

    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i>
      <span>Loading...</span>
    </div>

    <template v-else>
      <!-- Toolbar: filters + search -->
      <div class="toolbar-row">
        <div class="filter-dropdown" v-if="uniqueRoles.length > 0">
          <button class="dropdown-trigger" @click="toggleDropdown('role')" :class="{ 'has-selection': activeRoles.size > 0 }">
            <span class="dropdown-label">Role</span>
            <span class="dropdown-value">{{ activeRoles.size === 0 ? 'All' : [...activeRoles].join(', ') }}</span>
            <i class="pi pi-chevron-down dropdown-chevron"></i>
          </button>
          <div v-if="openDropdown === 'role'" class="dropdown-menu">
            <label v-for="r in uniqueRoles" :key="'role-' + r" class="dropdown-option">
              <input type="checkbox" :checked="activeRoles.has(r)" @change="toggleFilter(activeRoles, r)" />
              <span>{{ r }}</span>
            </label>
            <button v-if="activeRoles.size > 0" class="dropdown-clear" @click="activeRoles.clear()">Clear</button>
          </div>
        </div>
        <div class="search-box">
          <i class="pi pi-search"></i>
          <input v-model="search" type="text" placeholder="Search..." class="search-input" />
          <button v-if="search" class="search-clear" @click="search = ''">
            <i class="pi pi-times"></i>
          </button>
        </div>
      </div>

      <div class="host-count">{{ filteredHosts.length }} hosts</div>

      <div v-if="filteredHosts.length === 0" class="empty-state">
        {{ search ? 'No hosts match your search.' : 'No hosts found.' }}
      </div>

      <div v-else class="table-wrapper">
        <table class="hosts-table">
          <thead>
            <tr>
              <th class="sortable" @click="toggleSort('hostname')">
                Hostname
                <i v-if="sortCol === 'hostname'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th>Stage</th>
              <th>Role</th>
              <th>Region</th>
              <th class="col-status">Status</th>
              <th class="col-metric sortable" @click="toggleSort('cpu')">
                CPU %
                <i v-if="sortCol === 'cpu'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th class="col-metric sortable" @click="toggleSort('cpu_pressure')">
                CPU Pressure
                <i v-if="sortCol === 'cpu_pressure'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th class="col-metric sortable" @click="toggleSort('mem_pressure')">
                Mem Pressure
                <i v-if="sortCol === 'mem_pressure'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th class="col-metric sortable" @click="toggleSort('io_pressure')">
                IO Pressure
                <i v-if="sortCol === 'io_pressure'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="h in filteredHosts" :key="h.instance" :class="{ 'host-down': h.up === false }">
              <td class="cell-hostname">{{ h.hostname }}</td>
              <td><span class="badge" :class="'badge-' + h.stage">{{ h.stage || '-' }}</span></td>
              <td><span class="badge badge-role">{{ h.role || '-' }}</span></td>
              <td>{{ h.region || '-' }}</td>
              <td class="col-status">
                <span v-if="h.up === true" class="status-dot status-up" title="Up"></span>
                <span v-else-if="h.up === false" class="status-dot status-down" title="Down"></span>
                <span v-else class="status-dot status-unknown" title="Unknown"></span>
              </td>
              <td class="col-metric">
                <span v-if="h.cpu_percent != null" class="metric-value" :class="cpuClass(h.cpu_percent)">
                  {{ h.cpu_percent.toFixed(1) }}%
                </span>
                <span v-else class="metric-na">-</span>
              </td>
              <td class="col-pressure">
                <a v-if="sparklines[h.instance]?.cpu_pressure?.length"
                   :href="grafanaURL(h.instance, 'cpu_pressure')" target="_blank" rel="noopener"
                   class="sparkline-link" title="View in Grafana">
                  <svg class="sparkline" viewBox="0 0 80 20" preserveAspectRatio="none">
                    <path :d="sparklinePath(sparklines[h.instance]?.cpu_pressure)"
                          :stroke="sparklineColor(sparklines[h.instance]?.cpu_pressure)"
                          fill="none" stroke-width="1.5" vector-effect="non-scaling-stroke" />
                  </svg>
                </a>
                <span v-if="h.cpu_pressure != null" class="metric-value" :class="pressureClass(h.cpu_pressure)">
                  {{ h.cpu_pressure.toFixed(2) }}%
                </span>
                <span v-else class="metric-na">-</span>
              </td>
              <td class="col-pressure">
                <a v-if="sparklines[h.instance]?.memory_pressure?.length"
                   :href="grafanaURL(h.instance, 'memory_pressure')" target="_blank" rel="noopener"
                   class="sparkline-link" title="View in Grafana">
                  <svg class="sparkline" viewBox="0 0 80 20" preserveAspectRatio="none">
                    <path :d="sparklinePath(sparklines[h.instance]?.memory_pressure)"
                          :stroke="sparklineColor(sparklines[h.instance]?.memory_pressure)"
                          fill="none" stroke-width="1.5" vector-effect="non-scaling-stroke" />
                  </svg>
                </a>
                <span v-if="h.memory_pressure != null" class="metric-value" :class="pressureClass(h.memory_pressure)">
                  {{ h.memory_pressure.toFixed(2) }}%
                </span>
                <span v-else class="metric-na">-</span>
              </td>
              <td class="col-pressure">
                <a v-if="sparklines[h.instance]?.io_pressure?.length"
                   :href="grafanaURL(h.instance, 'io_pressure')" target="_blank" rel="noopener"
                   class="sparkline-link" title="View in Grafana">
                  <svg class="sparkline" viewBox="0 0 80 20" preserveAspectRatio="none">
                    <path :d="sparklinePath(sparklines[h.instance]?.io_pressure)"
                          :stroke="sparklineColor(sparklines[h.instance]?.io_pressure)"
                          fill="none" stroke-width="1.5" vector-effect="non-scaling-stroke" />
                  </svg>
                </a>
                <span v-if="h.io_pressure != null" class="metric-value" :class="pressureClass(h.io_pressure)">
                  {{ h.io_pressure.toFixed(2) }}%
                </span>
                <span v-else class="metric-na">-</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, reactive, nextTick, watch } from 'vue'
import { fetchHosts, fetchHostSparklines, type HostMetrics, type HostSparklineData } from '../api/client'

const hosts = ref<HostMetrics[]>([])
const sparklines = ref<Record<string, HostSparklineData>>({})
const loading = ref(true)
const error = ref('')
const search = ref('')
const openDropdown = ref<string | null>(null)
const activeRoles = reactive(new Set<string>())
const sortCol = ref<string>('hostname')
const sortDir = ref<'asc' | 'desc'>('asc')

let refreshTimer: ReturnType<typeof setInterval> | null = null
let sparklineTimer: ReturnType<typeof setInterval> | null = null

onMounted(async () => {
  await Promise.all([loadHosts(), loadSparklines()])
  refreshTimer = setInterval(loadHosts, 30_000)
  sparklineTimer = setInterval(loadSparklines, 60_000)
  document.addEventListener('click', closeDropdowns)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  if (sparklineTimer) clearInterval(sparklineTimer)
  document.removeEventListener('click', closeDropdowns)
})

async function loadSparklines() {
  try {
    sparklines.value = await fetchHostSparklines()
  } catch (_) {
    // Non-critical — sparklines just won't render.
  }
}

async function loadHosts() {
  try {
    hosts.value = await fetchHosts()
    if (loading.value) loading.value = false
  } catch (e: any) {
    error.value = e.message || 'Failed to load hosts'
    loading.value = false
  }
}

const uniqueRoles = computed(() => [...new Set(hosts.value.map(h => h.role).filter(Boolean))].sort())

const filteredHosts = computed(() => {
  let list = hosts.value
  if (activeRoles.size > 0) list = list.filter(h => activeRoles.has(h.role))
  if (search.value) {
    const q = search.value.toLowerCase()
    list = list.filter(h =>
      h.hostname.toLowerCase().includes(q) ||
      h.stage.toLowerCase().includes(q) ||
      h.role.toLowerCase().includes(q) ||
      h.region.toLowerCase().includes(q)
    )
  }

  const col = sortCol.value
  const dir = sortDir.value === 'asc' ? 1 : -1
  return [...list].sort((a, b) => {
    let cmp = 0
    if (col === 'hostname') cmp = a.hostname.localeCompare(b.hostname)
    else if (col === 'cpu') cmp = (a.cpu_percent ?? -1) - (b.cpu_percent ?? -1)
    else if (col === 'cpu_pressure') cmp = (a.cpu_pressure ?? -1) - (b.cpu_pressure ?? -1)
    else if (col === 'mem_pressure') cmp = (a.memory_pressure ?? -1) - (b.memory_pressure ?? -1)
    else if (col === 'io_pressure') cmp = (a.io_pressure ?? -1) - (b.io_pressure ?? -1)
    return cmp * dir
  })
})

function toggleSort(col: string) {
  if (sortCol.value === col) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortCol.value = col
    sortDir.value = col === 'hostname' ? 'asc' : 'desc'
  }
}

function toggleDropdown(name: string) {
  openDropdown.value = openDropdown.value === name ? null : name
}

function closeDropdowns(e: MouseEvent) {
  if (!(e.target as HTMLElement).closest('.filter-dropdown')) {
    openDropdown.value = null
  }
}

function toggleFilter(set: Set<string>, value: string) {
  if (set.has(value)) set.delete(value)
  else set.add(value)
}

function cpuClass(v: number): string {
  if (v >= 80) return 'metric-critical'
  if (v >= 50) return 'metric-warning'
  return 'metric-ok'
}

const GRAFANA_BASE = 'https://grafana.crocodile-vector.ts.net'
const GRAFANA_DS_UID = 'PBFA97CFB590B2093'

const pressureMetrics: Record<string, string> = {
  cpu_pressure: 'node_pressure_cpu_waiting_seconds_total',
  memory_pressure: 'node_pressure_memory_waiting_seconds_total',
  io_pressure: 'node_pressure_io_waiting_seconds_total',
}

function grafanaURL(instance: string, metricKey: string): string {
  const metric = pressureMetrics[metricKey]
  if (!metric) return '#'
  const expr = `rate(${metric}{instance="${instance}"}[$__rate_interval])`
  const panes = {
    sp: {
      datasource: GRAFANA_DS_UID,
      queries: [{
        refId: 'A',
        expr,
        range: true,
        instant: true,
        datasource: { type: 'prometheus', uid: GRAFANA_DS_UID },
        editorMode: 'builder',
        legendFormat: '__auto',
        useBackend: false,
        disableTextWrap: false,
        fullMetaSearch: false,
        includeNullMetadata: false,
      }],
      range: { from: 'now-1h', to: 'now' },
    },
  }
  return `${GRAFANA_BASE}/explore?schemaVersion=1&panes=${encodeURIComponent(JSON.stringify(panes))}&orgId=1`
}

function sparklinePath(points: [number, number][] | undefined): string {
  if (!points || points.length < 2) return ''
  const tMin = points[0][0]
  const tMax = points[points.length - 1][0]
  const tRange = tMax - tMin || 1
  let vMax = 0
  for (const p of points) {
    if (p[1] > vMax) vMax = p[1]
  }
  if (vMax === 0) vMax = 1

  const w = 80
  const h = 20
  const parts: string[] = []
  for (let i = 0; i < points.length; i++) {
    const x = ((points[i][0] - tMin) / tRange) * w
    const y = h - (points[i][1] / vMax) * h
    parts.push(`${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`)
  }
  return parts.join(' ')
}

function sparklineColor(points: [number, number][] | undefined): string {
  if (!points || points.length === 0) return 'var(--text-color-muted)'
  const last = points[points.length - 1][1]
  if (last >= 10) return 'var(--red-400)'
  if (last >= 2) return 'var(--yellow-400)'
  return 'var(--text-color-secondary)'
}

function pressureClass(v: number): string {
  if (v >= 10) return 'metric-critical'
  if (v >= 2) return 'metric-warning'
  return 'metric-ok'
}
</script>

<style scoped>
.hosts-view {
  max-width: 1400px;
}

.page-header {
  margin-bottom: 1.25rem;
}

.page-header h1 {
  font-size: 1.25rem;
  font-weight: 600;
  color: var(--text-color);
}

.page-subtitle {
  font-size: 0.75rem;
  color: var(--text-color-secondary);
  margin-top: 0.15rem;
}

/* Messages */
.message-banner {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.6rem 0.75rem;
  border-radius: 4px;
  font-size: 0.8rem;
  margin-bottom: 1rem;
}

.message-error {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid var(--red-400);
}

.loading-state {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 2rem;
  color: var(--text-color-secondary);
  font-size: 0.85rem;
}

.host-count {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  margin-bottom: 0.5rem;
}

.empty-state {
  text-align: center;
  padding: 3rem 1rem;
  color: var(--text-color-muted);
  font-size: 0.85rem;
}

/* Toolbar */
.toolbar-row {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin-bottom: 0.75rem;
  flex-wrap: wrap;
}

.filter-dropdown {
  position: relative;
}

.dropdown-trigger {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.35rem 0.6rem;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
  color: var(--text-color-secondary);
  font-size: 0.75rem;
  cursor: pointer;
  transition: border-color 0.15s;
  white-space: nowrap;
}

.dropdown-trigger:hover,
.dropdown-trigger.has-selection {
  border-color: var(--primary-color);
  color: var(--text-color);
}

.dropdown-label {
  font-weight: 500;
  color: var(--text-color-muted);
}

.dropdown-chevron {
  font-size: 0.6rem;
  margin-left: 0.15rem;
}

.dropdown-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  min-width: 140px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.35rem;
  z-index: 50;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
}

.dropdown-option {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.3rem 0.4rem;
  border-radius: 3px;
  font-size: 0.75rem;
  cursor: pointer;
  color: var(--text-color);
}

.dropdown-option:hover {
  background: var(--surface-hover);
}

.dropdown-clear {
  display: block;
  width: 100%;
  padding: 0.3rem 0.4rem;
  margin-top: 0.25rem;
  border: none;
  border-top: 1px solid var(--surface-border);
  background: none;
  color: var(--text-color-muted);
  font-size: 0.7rem;
  cursor: pointer;
  text-align: center;
}

.dropdown-clear:hover {
  color: var(--text-color);
}

.search-box {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.35rem 0.6rem;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
  flex: 1;
  max-width: 240px;
}

.search-box i {
  color: var(--text-color-muted);
  font-size: 0.75rem;
}

.search-input {
  border: none;
  background: none;
  color: var(--text-color);
  font-size: 0.75rem;
  outline: none;
  width: 100%;
  font-family: inherit;
}

.search-clear {
  border: none;
  background: none;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 0.65rem;
  padding: 0;
}

/* Table */
.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
}

.hosts-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.78rem;
}

.hosts-table th,
.hosts-table td {
  padding: 0.45rem 0.65rem;
  text-align: left;
  border-bottom: 1px solid var(--surface-border);
  white-space: nowrap;
}

.hosts-table th {
  background: var(--surface-card);
  color: var(--text-color-secondary);
  font-weight: 500;
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  position: sticky;
  top: 0;
  z-index: 1;
}

.hosts-table th.sortable {
  cursor: pointer;
  user-select: none;
}

.hosts-table th.sortable:hover {
  color: var(--text-color);
}

.hosts-table tbody tr:hover {
  background: var(--surface-hover);
}

.hosts-table tbody tr:last-child td {
  border-bottom: none;
}

.host-down {
  opacity: 0.5;
}

.cell-hostname {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.col-status {
  text-align: center;
  width: 60px;
}

.col-metric {
  text-align: right;
  width: 100px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

/* Badges */
.badge {
  display: inline-block;
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
  font-size: 0.68rem;
  font-weight: 500;
}

.badge-production {
  background: var(--green-subtle);
  color: var(--green-400);
}

.badge-staging {
  background: var(--yellow-subtle);
  color: var(--yellow-400);
}

.badge-role {
  background: var(--primary-50);
  color: var(--primary-color);
}

/* Status dots */
.status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.status-up {
  background: var(--green-400);
  box-shadow: 0 0 4px var(--green-400);
}

.status-down {
  background: var(--red-400);
  box-shadow: 0 0 4px var(--red-400);
}

.status-unknown {
  background: var(--text-color-muted);
}

/* Metric values */
.metric-value {
  font-weight: 500;
}

.metric-ok {
  color: var(--text-color);
}

.metric-warning {
  color: var(--yellow-400);
}

.metric-critical {
  color: var(--red-400);
}

.metric-na {
  color: var(--text-color-muted);
}

/* Pressure cells with sparklines */
.col-pressure {
  text-align: right;
  width: 160px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  white-space: nowrap;
}

.sparkline-link {
  display: inline-block;
  vertical-align: middle;
  margin-right: 0.4rem;
  opacity: 0.8;
  transition: opacity 0.15s;
}

.sparkline-link:hover {
  opacity: 1;
}

.sparkline {
  width: 60px;
  height: 18px;
  display: inline-block;
  vertical-align: middle;
}

@media (max-width: 991px) {
  .col-metric {
    width: 80px;
  }
  .col-pressure {
    width: 120px;
  }
  .sparkline {
    width: 40px;
  }
}
</style>

<template>
  <div class="components-view">
    <div class="page-header">
      <div>
        <h1>Components</h1>
        <p class="page-subtitle">Version matrix for exe components</p>
      </div>
      <div class="header-right">
        <button
          v-if="componentNames.length > 1 || uniqueRegions.length > 1 || uniqueEnvs.length > 1"
          class="mobile-filter-toggle"
          :class="{ 'has-active': activeComponents.size > 0 || activeRegions.size > 0 || activeEnvs.size > 0 }"
          @click="showFilters = !showFilters"
        >
          <i class="pi pi-filter"></i>
        </button>
        <div class="filter-groups" v-if="componentNames.length > 1 || uniqueRegions.length > 1 || uniqueEnvs.length > 1">
          <div class="filter-group" v-if="componentNames.length > 1">
            <span class="filter-label">Component</span>
            <div class="filter-buttons">
              <button
                v-for="c in componentNames"
                :key="'comp-' + c"
                class="filter-btn"
                :class="{ active: activeComponents.has(c) }"
                @click="toggleComponentFilter(c)"
              >{{ c }}</button>
            </div>
          </div>
          <div class="filter-group" v-if="uniqueRegions.length > 1">
            <span class="filter-label">Region</span>
            <div class="filter-buttons">
              <button
                v-for="r in uniqueRegions"
                :key="'region-' + r"
                class="filter-btn"
                :class="{ active: activeRegions.has(r) }"
                @click="toggleRegionFilter(r)"
              >{{ r }}</button>
            </div>
          </div>
          <div class="filter-group" v-if="uniqueEnvs.length > 1">
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
    <!-- Mobile filter panel -->
    <div v-if="showFilters" class="mobile-filter-panel">
      <div class="filter-group" v-if="componentNames.length > 1">
        <span class="filter-label">Component</span>
        <div class="filter-buttons">
          <button
            v-for="c in componentNames"
            :key="'mcomp-' + c"
            class="filter-btn"
            :class="{ active: activeComponents.has(c) }"
            @click="toggleComponentFilter(c)"
          >{{ c }}</button>
        </div>
      </div>
      <div class="filter-group" v-if="uniqueRegions.length > 1">
        <span class="filter-label">Region</span>
        <div class="filter-buttons">
          <button
            v-for="r in uniqueRegions"
            :key="'mregion-' + r"
            class="filter-btn"
            :class="{ active: activeRegions.has(r) }"
            @click="toggleRegionFilter(r)"
          >{{ r }}</button>
        </div>
      </div>
      <div class="filter-group" v-if="uniqueEnvs.length > 1">
        <span class="filter-label">Env</span>
        <div class="filter-buttons">
          <button
            v-for="e in uniqueEnvs"
            :key="'menv-' + e"
            class="filter-btn"
            :class="{ active: activeEnvs.has(e) }"
            @click="toggleEnvFilter(e)"
          >{{ e }}</button>
        </div>
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
      <div v-if="componentNames.length === 0" class="empty-state">
        No component data available.
      </div>

      <!-- Fleet-wide summary -->
      <div class="summary-row" v-if="componentNames.length > 0">
        <div class="summary-card">
          <div class="summary-value">{{ serversWithFilteredComponents.length }}</div>
          <div class="summary-label">Total Servers</div>
        </div>
        <div class="summary-card">
          <div class="summary-value">{{ filteredComponentNames.length }}</div>
          <div class="summary-label">Components Tracked</div>
        </div>
        <div class="summary-card">
          <div class="summary-value" :class="fleetConsistencyClass">{{ fleetConsistencyPct }}%</div>
          <div class="summary-label">Fleet Consistency</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-green">{{ totalUpToDate }}</div>
          <div class="summary-label">Up to Date</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-green">{{ totalAllActive }}</div>
          <div class="summary-label">All Active</div>
        </div>
        <div class="summary-card">
          <div class="summary-value" :class="totalOutdated > 0 ? 'value-yellow' : 'value-green'">{{ totalOutdated }}</div>
          <div class="summary-label">Outdated</div>
        </div>
        <div class="summary-card" v-if="totalNotFound > 0">
          <div class="summary-value value-red">{{ totalNotFound }}</div>
          <div class="summary-label">Not Found</div>
        </div>
      </div>

      <!-- Component tabs -->
      <div v-if="componentNames.length > 0" class="tab-bar">
        <button
          v-for="name in componentNames"
          :key="'tab-' + name"
          class="tab-btn"
          :class="{ active: activeTab === name }"
          @click="activeTab = name"
        >
          {{ name }}
          <span class="tab-count">{{ serversWithComponent(name) }}</span>
        </button>
      </div>

      <!-- Active component content -->
      <template v-if="activeTab && componentNames.includes(activeTab)">
        <!-- Per-component stat cards -->
        <div class="summary-row">
          <div class="summary-card">
            <div class="summary-value mono">{{ latestVersion(activeTab) || '-' }}</div>
            <div class="summary-label">Latest Version</div>
          </div>
          <div class="summary-card">
            <div class="summary-value value-green">{{ serversOnLatest(activeTab) }}<span class="stat-of">/{{ serversWithComponent(activeTab) }}</span></div>
            <div class="summary-label">On Latest</div>
          </div>
          <div class="summary-card">
            <div class="summary-value value-green">{{ activeCount(activeTab) }}<span class="stat-of">/{{ serversWithComponent(activeTab) }}</span></div>
            <div class="summary-label">Active</div>
          </div>
          <div class="summary-card">
            <div class="summary-value" :class="rolloutPctClass(activeTab)">{{ rolloutPct(activeTab) }}%</div>
            <div class="summary-label">Rollout</div>
          </div>
          <div class="summary-card">
            <div class="summary-value">{{ versionSpread(activeTab) }}</div>
            <div class="summary-label">Versions in Fleet</div>
          </div>
        </div>

        <!-- Server table -->
        <div class="component-section">
          <div class="section-header">
            <span class="version-summary" v-if="latestVersion(activeTab)">
              {{ serversOnLatest(activeTab) }} of {{ serversWithComponent(activeTab) }} on latest ({{ latestVersion(activeTab) }})
            </span>
          </div>

          <div class="table-wrapper">
            <table class="component-table">
              <thead>
                <tr>
                  <th>Server</th>
                  <th>Version</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="row in componentRows(activeTab)"
                  :key="row.server"
                  class="component-row"
                  @click="$router.push(`/servers/${row.server}`)"
                >
                  <td class="col-server">{{ row.server }}</td>
                  <td class="col-version">
                    <span :class="{ 'version-outdated': latestVersion(activeTab) && row.version !== latestVersion(activeTab) }">
                      {{ row.version || '-' }}
                    </span>
                  </td>
                  <td>
                    <span class="status-badge" :class="statusClass(row.status)">{{ row.status }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { fetchFleet, type FleetServer } from '../api/client'

const servers = ref<FleetServer[]>([])
const loading = ref(true)
const error = ref('')
const activeRegions = reactive(new Set<string>())
const activeEnvs = reactive(new Set<string>())
const activeComponents = reactive(new Set<string>())
const activeTab = ref('')
const showFilters = ref(false)
let pollTimer: ReturnType<typeof setInterval> | null = null

try {
  const savedRegions = sessionStorage.getItem('exe-ops-region-filter')
  if (savedRegions) for (const r of JSON.parse(savedRegions)) activeRegions.add(r)
  const savedEnvs = sessionStorage.getItem('exe-ops-env-filter')
  if (savedEnvs) for (const e of JSON.parse(savedEnvs)) activeEnvs.add(e)
} catch {}

watch(activeRegions, () => {
  if (activeRegions.size > 0) {
    sessionStorage.setItem('exe-ops-region-filter', JSON.stringify([...activeRegions]))
  } else {
    sessionStorage.removeItem('exe-ops-region-filter')
  }
})

watch(activeEnvs, () => {
  if (activeEnvs.size > 0) {
    sessionStorage.setItem('exe-ops-env-filter', JSON.stringify([...activeEnvs]))
  } else {
    sessionStorage.removeItem('exe-ops-env-filter')
  }
})

function toggleRegionFilter(value: string) {
  if (activeRegions.has(value)) {
    activeRegions.delete(value)
  } else {
    activeRegions.add(value)
  }
}

function toggleEnvFilter(value: string) {
  if (activeEnvs.has(value)) {
    activeEnvs.delete(value)
  } else {
    activeEnvs.add(value)
  }
}

function toggleComponentFilter(value: string) {
  if (activeComponents.has(value)) {
    activeComponents.delete(value)
  } else {
    activeComponents.add(value)
  }
}

const uniqueRegions = computed(() =>
  [...new Set(servers.value.map(s => s.region).filter(Boolean))].sort()
)

const uniqueEnvs = computed(() =>
  [...new Set(servers.value.map(s => s.env).filter(Boolean))].sort()
)

const filteredServers = computed(() => {
  return servers.value.filter(s => {
    if (activeRegions.size > 0 && !activeRegions.has(s.region)) return false
    if (activeEnvs.size > 0 && !activeEnvs.has(s.env)) return false
    return true
  })
})

interface ComponentRow {
  server: string
  version: string
  status: string
}

const componentNames = computed(() => {
  const names = new Set<string>()
  for (const s of filteredServers.value) {
    if (s.components) {
      for (const c of s.components) {
        names.add(c.name)
      }
    }
  }
  return [...names].sort()
})

// Components to consider in fleet-wide stats (respects component filter)
const filteredComponentNames = computed(() => {
  if (activeComponents.size === 0) return componentNames.value
  return componentNames.value.filter(n => activeComponents.has(n))
})

watch(componentNames, (names) => {
  if (names.length > 0 && !names.includes(activeTab.value)) {
    activeTab.value = names[0]
  }
}, { immediate: true })

function componentRows(name: string): ComponentRow[] {
  const rows: ComponentRow[] = []
  for (const s of filteredServers.value) {
    if (s.components) {
      const c = s.components.find(c => c.name === name)
      if (c) {
        rows.push({ server: s.name, version: c.version, status: c.status })
      }
    }
  }
  return rows.sort((a, b) => a.server.localeCompare(b.server))
}

function latestVersion(name: string): string {
  const versions = new Map<string, number>()
  for (const s of filteredServers.value) {
    if (s.components) {
      const c = s.components.find(c => c.name === name)
      if (c && c.version) {
        versions.set(c.version, (versions.get(c.version) || 0) + 1)
      }
    }
  }
  // Most common version is considered "latest"
  let best = ''
  let bestCount = 0
  for (const [v, count] of versions) {
    if (count > bestCount) {
      best = v
      bestCount = count
    }
  }
  return best
}

function serversWithComponent(name: string): number {
  return componentRows(name).length
}

function serversOnLatest(name: string): number {
  const latest = latestVersion(name)
  if (!latest) return 0
  return componentRows(name).filter(r => r.version === latest).length
}

// Servers that have at least one of the filtered components
const serversWithFilteredComponents = computed(() => {
  const names = filteredComponentNames.value
  return filteredServers.value.filter(s => {
    if (!s.components || s.components.length === 0) return false
    return s.components.some(c => names.includes(c.name))
  })
})

// Fleet-wide: servers where ALL filtered components match latest
const fleetConsistencyPct = computed(() => {
  const relevant = serversWithFilteredComponents.value
  if (relevant.length === 0) return '0'
  const names = filteredComponentNames.value
  const consistent = relevant.filter(s => {
    const matched = s.components!.filter(c => names.includes(c.name))
    return matched.every(c => {
      const latest = latestVersion(c.name)
      return !latest || c.version === latest
    })
  }).length
  return ((consistent / relevant.length) * 100).toFixed(0)
})

const fleetConsistencyClass = computed(() => {
  const pct = parseFloat(fleetConsistencyPct.value)
  if (pct >= 90) return 'value-green'
  if (pct >= 50) return 'value-yellow'
  return 'value-red'
})

// Version health: servers with all filtered components on latest
const totalUpToDate = computed(() => {
  const names = filteredComponentNames.value
  return serversWithFilteredComponents.value.filter(s => {
    const relevant = s.components!.filter(c => names.includes(c.name))
    return relevant.every(c => {
      const latest = latestVersion(c.name)
      return !latest || c.version === latest
    })
  }).length
})

const totalOutdated = computed(() => {
  const names = filteredComponentNames.value
  return serversWithFilteredComponents.value.filter(s => {
    const relevant = s.components!.filter(c => names.includes(c.name))
    return relevant.some(c => {
      const latest = latestVersion(c.name)
      return latest && c.version !== latest
    })
  }).length
})

const totalAllActive = computed(() => {
  const names = filteredComponentNames.value
  return serversWithFilteredComponents.value.filter(s => {
    const relevant = s.components!.filter(c => names.includes(c.name))
    return relevant.every(c => c.status === 'active')
  }).length
})

const totalNotFound = computed(() => {
  const names = filteredComponentNames.value
  return serversWithFilteredComponents.value.filter(s => {
    const relevant = s.components!.filter(c => names.includes(c.name))
    return relevant.some(c => c.status === 'not-found')
  }).length
})

// Per-component helpers
function activeCount(name: string): number {
  return componentRows(name).filter(r => r.status === 'active').length
}

function versionSpread(name: string): number {
  const versions = new Set<string>()
  for (const s of filteredServers.value) {
    if (s.components) {
      const c = s.components.find(c => c.name === name)
      if (c && c.version) versions.add(c.version)
    }
  }
  return versions.size
}

function rolloutPct(name: string): string {
  const total = serversWithComponent(name)
  if (total === 0) return '0'
  return ((serversOnLatest(name) / total) * 100).toFixed(0)
}

function rolloutPctClass(name: string): string {
  const pct = parseFloat(rolloutPct(name))
  if (pct >= 90) return 'value-green'
  if (pct >= 50) return 'value-yellow'
  return 'value-red'
}

function statusClass(status: string): string {
  if (status === 'active') return 'status-active'
  if (status === 'inactive') return 'status-inactive'
  return 'status-notfound'
}

async function load() {
  try {
    servers.value = await fetchFleet()
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load fleet data'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  pollTimer = setInterval(load, 30000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
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

/* ── Filters ── */
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

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
}

.component-section {
  margin-bottom: 2rem;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.75rem;
}

.section-title {
  font-size: 0.9rem;
  font-weight: 500;
}

.version-summary {
  font-size: 0.75rem;
  color: var(--text-color-muted);
}

.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
}

.component-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.component-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.component-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
}

.component-row {
  cursor: pointer;
  transition: background 0.15s;
}

.component-row:hover {
  background: var(--surface-hover);
}

.component-row:last-child td {
  border-bottom: none;
}

.col-server {
  font-weight: 600;
  color: var(--text-color);
}

.col-version {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.version-outdated {
  color: var(--yellow-400);
}

.status-badge {
  display: inline-flex;
  padding: 0.15rem 0.4rem;
  border-radius: 3px;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.status-active {
  background: var(--green-subtle);
  color: var(--green-400);
}

.status-inactive {
  background: var(--yellow-subtle);
  color: var(--yellow-400);
}

.status-notfound {
  background: var(--red-subtle);
  color: var(--red-400);
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

.empty-state {
  text-align: center;
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

/* ── Summary Row ── */
.summary-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.summary-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.75rem 1rem;
  flex: 1;
  text-align: center;
}

.summary-value {
  font-size: 1.25rem;
  font-weight: 600;
  letter-spacing: -0.02em;
  line-height: 1;
}

.summary-value.mono {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  word-break: break-all;
}

.summary-label {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  margin-top: 0.35rem;
}

.stat-of {
  font-size: 0.8rem;
  font-weight: 400;
  color: var(--text-color-muted);
}

.value-green { color: var(--green-400); }
.value-yellow { color: var(--yellow-400); }
.value-red { color: var(--red-400); }

/* ── Tabs ── */
.tab-bar {
  display: flex;
  gap: 0.25rem;
  margin-bottom: 1.25rem;
  border-bottom: 1px solid var(--surface-border);
  padding-bottom: 0;
}

.tab-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.5rem 0.75rem;
  font-size: 0.75rem;
  font-family: inherit;
  font-weight: 500;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-color-muted);
  cursor: pointer;
  transition: all 0.15s;
  margin-bottom: -1px;
}

.tab-btn:hover {
  color: var(--text-color);
}

.tab-btn.active {
  color: var(--text-color);
  border-bottom-color: var(--primary-color);
}

.tab-count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 1.25rem;
  height: 1.125rem;
  padding: 0 0.375rem;
  font-size: 0.65rem;
  font-weight: 600;
  border-radius: 9rem;
  background: var(--surface-border);
  color: var(--text-color-secondary);
}

.tab-btn.active .tab-count {
  background: var(--primary-50);
  color: var(--primary-color);
}

.header-right {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.mobile-filter-toggle {
  display: none;
}

.mobile-filter-panel {
  display: none;
}

@media (max-width: 768px) {
  .filter-groups {
    display: none !important;
  }

  .mobile-filter-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    background: var(--surface-card);
    border: 1px solid var(--surface-border);
    border-radius: 4px;
    color: var(--text-color-muted);
    cursor: pointer;
    font-size: 0.8rem;
    transition: all 0.15s;
  }

  .mobile-filter-toggle:hover,
  .mobile-filter-toggle.has-active {
    color: var(--primary-color);
    border-color: var(--primary-color);
  }

  .mobile-filter-panel {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    background: var(--surface-card);
    border: 1px solid var(--surface-border);
    border-radius: 4px;
    padding: 0.75rem;
    margin-bottom: 1rem;
  }

  .mobile-filter-panel .filter-group {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }

  .mobile-filter-panel .filter-buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }

  .summary-row {
    gap: 0.5rem;
  }

  .summary-card {
    width: fit-content;
    flex: 1;
  }
}
</style>

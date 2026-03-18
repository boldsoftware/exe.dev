<template>
  <div class="updates-view">
    <div class="page-header">
      <div>
        <h1>Updates</h1>
        <p class="page-subtitle">Pending package updates fleet-wide</p>
      </div>
      <div class="header-controls">
        <label class="filter-toggle">
          <input type="checkbox" v-model="showOnlyWithUpdates" />
          <span>Only servers with updates</span>
        </label>
        <button
          v-if="uniqueRegions.length > 1 || uniqueEnvs.length > 1"
          class="mobile-filter-toggle"
          :class="{ 'has-active': activeRegions.size > 0 || activeEnvs.size > 0 }"
          @click="showFilters = !showFilters"
        >
          <i class="pi pi-filter"></i>
        </button>
        <div class="filter-groups" v-if="uniqueRegions.length > 1 || uniqueEnvs.length > 1">
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
      <!-- Summary -->
      <div class="summary-row" v-if="envFilteredServers.length > 0">
        <div class="summary-card">
          <div class="summary-value">{{ envFilteredServers.length }}</div>
          <div class="summary-label">Total Servers</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-yellow">{{ serversWithUpdates }}</div>
          <div class="summary-label">Servers with updates</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-green">{{ upToDate }}</div>
          <div class="summary-label">Up to Date</div>
        </div>
        <div class="summary-card">
          <div class="summary-value" :class="patchRateClass">{{ patchRate }}%</div>
          <div class="summary-label">Patch Rate</div>
        </div>
        <div class="summary-card">
          <div class="summary-value">{{ totalPackages }}</div>
          <div class="summary-label">Total packages</div>
        </div>
        <div class="summary-card" v-if="serversWithUpdates > 0">
          <div class="summary-value">{{ avgPackages }}</div>
          <div class="summary-label">Avg Packages</div>
        </div>
        <div class="summary-card" v-if="serversWithUpdates > 0">
          <div class="summary-value value-yellow">{{ maxPending }}</div>
          <div class="summary-label">Max Pending</div>
        </div>
      </div>

      <div v-if="envFilteredServers.length > 0" class="toolbar">
        <div class="search-box">
          <i class="pi pi-search"></i>
          <input
            v-model="search"
            type="text"
            placeholder="Search servers or packages..."
            class="search-input"
          />
          <button v-if="search" class="search-clear" @click="search = ''">
            <i class="pi pi-times"></i>
          </button>
        </div>
      </div>

      <div v-if="filteredServers.length === 0" class="empty-state">
        {{ search ? 'No servers match your search.' : showOnlyWithUpdates ? 'No servers have pending updates.' : 'No servers found.' }}
      </div>

      <template v-else>
      <div class="table-wrapper desktop-only">
        <table class="updates-table">
          <thead>
            <tr>
              <th class="sortable" @click="toggleSort('server')">
                Server
                <i v-if="sortCol === 'server'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th class="sortable" @click="toggleSort('pending')">
                Pending
                <i v-if="sortCol === 'pending'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
              </th>
              <th>Packages</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="s in filteredServers"
              :key="s.name"
              class="update-row"
              @click="$router.push(`/servers/${s.name}`)"
            >
              <td class="col-server">{{ s.name }}</td>
              <td class="col-count">
                <span v-if="(s.updates?.length ?? 0) > 0" class="pending-badge">{{ s.updates!.length }}</span>
                <span v-else class="metric-blank">&mdash;</span>
              </td>
              <td class="col-packages">
                <template v-if="s.updates && s.updates.length > 0">
                  <span class="pkg-list">
                    <span
                      v-for="pkg in s.updates"
                      :key="pkg"
                      class="pkg-tag"
                    >{{ parsePkg(pkg).name }}</span>
                  </span>
                  <button
                    class="expand-btn"
                    @click.stop="pkgDialogServer = s.name"
                  >
                    {{ s.updates.length }} pkg{{ s.updates.length !== 1 ? 's' : '' }}
                  </button>
                </template>
                <span v-else class="metric-blank">Up to date</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Mobile cards -->
      <div class="mobile-cards mobile-only">
        <div
          v-for="s in filteredServers"
          :key="s.name"
          class="mobile-card"
          @click="$router.push(`/servers/${s.name}`)"
        >
          <div class="mobile-card-header">
            <span class="col-server">{{ s.name }}</span>
            <div class="mobile-card-right">
              <button
                v-if="s.updates && s.updates.length > 0"
                class="expand-btn"
                @click.stop="pkgDialogServer = s.name"
              >
                {{ s.updates.length }} pkg{{ s.updates.length !== 1 ? 's' : '' }}
              </button>
              <span v-if="(s.updates?.length ?? 0) > 0" class="pending-badge">{{ s.updates!.length }}</span>
              <span v-else class="metric-blank">Up to date</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Mobile package dialog -->
      <div v-if="pkgDialogServer" class="pkg-dialog-overlay" @click="pkgDialogServer = null">
        <div class="pkg-dialog" @click.stop>
          <div class="pkg-dialog-header">
            <span class="pkg-dialog-title">{{ pkgDialogServer }}</span>
            <button class="pkg-dialog-close" @click="pkgDialogServer = null">
              <i class="pi pi-times"></i>
            </button>
          </div>
          <div class="pkg-dialog-body">
            <span
              v-for="pkg in pkgDialogPackages"
              :key="pkg"
              class="pkg-tag"
            >{{ formatPkg(pkg) }}</span>
          </div>
        </div>
      </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch, onMounted, onUnmounted } from 'vue'
import { fetchFleet, type FleetServer } from '../api/client'

const servers = ref<FleetServer[]>([])
const loading = ref(true)
const error = ref('')
const showOnlyWithUpdates = ref(true)
const expanded = reactive(new Set<string>())
const sortCol = ref<'server' | 'pending'>('server')
const sortDir = ref<'asc' | 'desc'>('asc')
const search = ref('')
const showFilters = ref(false)
const pkgDialogServer = ref<string | null>(null)
const activeRegions = reactive(new Set<string>())
const activeEnvs = reactive(new Set<string>())
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

const uniqueRegions = computed(() =>
  [...new Set(servers.value.map(s => s.region).filter(Boolean))].sort()
)

const uniqueEnvs = computed(() =>
  [...new Set(servers.value.map(s => s.env).filter(Boolean))].sort()
)

const envFilteredServers = computed(() => {
  return servers.value.filter(s => {
    if (activeRegions.size > 0 && !activeRegions.has(s.region)) return false
    if (activeEnvs.size > 0 && !activeEnvs.has(s.env)) return false
    return true
  })
})

const filteredServers = computed(() => {
  let list = [...envFilteredServers.value]
  if (showOnlyWithUpdates.value) {
    list = list.filter(s => s.updates && s.updates.length > 0)
  }
  if (search.value) {
    const q = search.value.toLowerCase()
    list = list.filter(s =>
      s.name.toLowerCase().includes(q) ||
      (s.updates && s.updates.some(p => p.toLowerCase().includes(q)))
    )
  }
  const dir = sortDir.value === 'asc' ? 1 : -1
  if (sortCol.value === 'server') {
    list.sort((a, b) => dir * a.name.localeCompare(b.name))
  } else {
    list.sort((a, b) => dir * ((a.updates?.length ?? 0) - (b.updates?.length ?? 0)))
  }
  return list
})

function toggleSort(col: 'server' | 'pending') {
  if (sortCol.value === col) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortCol.value = col
    sortDir.value = col === 'server' ? 'asc' : 'desc'
  }
}

const serversWithUpdates = computed(() => envFilteredServers.value.filter(s => s.updates && s.updates.length > 0).length)
const upToDate = computed(() => envFilteredServers.value.length - serversWithUpdates.value)
const totalPackages = computed(() => envFilteredServers.value.reduce((sum, s) => sum + (s.updates?.length ?? 0), 0))

const patchRate = computed(() => {
  if (envFilteredServers.value.length === 0) return '0'
  return ((upToDate.value / envFilteredServers.value.length) * 100).toFixed(0)
})

const patchRateClass = computed(() => {
  const pct = parseFloat(patchRate.value)
  if (pct >= 90) return 'value-green'
  if (pct >= 50) return 'value-yellow'
  return 'value-red'
})

const avgPackages = computed(() => {
  if (serversWithUpdates.value === 0) return '0'
  return Math.round(totalPackages.value / serversWithUpdates.value).toString()
})

const maxPending = computed(() => {
  return envFilteredServers.value.reduce((max, s) => Math.max(max, s.updates?.length ?? 0), 0)
})

// Parse apt upgrade line: "pkg/repo version arch [upgradable from: old]"
function parsePkg(raw: string): { name: string; version: string; from: string } {
  const slash = raw.indexOf('/')
  const name = slash > 0 ? raw.substring(0, slash) : raw.split(' ')[0]
  const parts = raw.split(' ')
  // version is typically the second space-separated token after repo
  let version = ''
  let from = ''
  if (slash > 0 && parts.length >= 2) {
    version = parts[1]
  }
  const fromMatch = raw.match(/\[upgradable from: ([^\]]+)\]/)
  if (fromMatch) from = fromMatch[1]
  return { name, version, from }
}

function formatPkg(raw: string): string {
  const p = parsePkg(raw)
  if (p.from && p.version) return `${p.name} ${p.from} → ${p.version}`
  if (p.version) return `${p.name} ${p.version}`
  return p.name
}

function toggleExpand(name: string) {
  if (expanded.has(name)) {
    expanded.delete(name)
  } else {
    expanded.add(name)
  }
}

const pkgDialogPackages = computed(() => {
  if (!pkgDialogServer.value) return []
  const s = filteredServers.value.find(s => s.name === pkgDialogServer.value)
  return s?.updates ?? []
})

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

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
}

.header-controls {
  display: flex;
  align-items: center;
  gap: 0.75rem;
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

.filter-toggle {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.8rem;
  color: var(--text-color-secondary);
  cursor: pointer;
}

.filter-toggle input {
  accent-color: var(--primary-color);
}

.toolbar {
  margin-bottom: 1rem;
}

.search-box {
  position: relative;
  max-width: 320px;
}

.search-box .pi-search {
  position: absolute;
  left: 0.75rem;
  top: 50%;
  transform: translateY(-50%);
  color: var(--text-color-muted);
  font-size: 0.8rem;
}

.search-input {
  width: 100%;
  padding: 0.5rem 2rem 0.5rem 2.25rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color);
  font-size: 0.8rem;
  font-family: inherit;
  outline: none;
  transition: border-color 0.15s;
}

.search-input::placeholder {
  color: var(--text-color-muted);
}

.search-input:focus {
  border-color: var(--primary-color);
}

.search-clear {
  position: absolute;
  right: 0.5rem;
  top: 50%;
  transform: translateY(-50%);
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  padding: 0.25rem;
  font-size: 0.7rem;
  display: flex;
  align-items: center;
}

.search-clear:hover {
  color: var(--text-color);
}

.summary-row {
  display: flex;
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.summary-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1rem 1.5rem;
  flex: 1;
  text-align: center;
}

.summary-value {
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--text-color);
}

.summary-label {
  font-size: 0.7rem;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-top: 0.25rem;
}

.value-green { color: var(--green-400); }
.value-yellow { color: var(--yellow-400); }
.value-red { color: var(--red-400); }

.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
}

.updates-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.updates-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.updates-table th.sortable {
  cursor: pointer;
  user-select: none;
  transition: color 0.15s;
}

.updates-table th.sortable:hover {
  color: var(--text-color);
}

.updates-table th.sortable .pi {
  font-size: 0.6rem;
  margin-left: 0.25rem;
  vertical-align: middle;
}

.updates-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
}

.update-row {
  cursor: pointer;
  transition: background 0.15s;
}

.update-row:hover {
  background: var(--surface-hover);
}

.update-row:last-child td {
  border-bottom: none;
}

.col-server {
  font-weight: 600;
  color: var(--text-color);
  white-space: nowrap;
}

.col-count {
  white-space: nowrap;
}

.pending-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 24px;
  padding: 0.15rem 0.4rem;
  border-radius: 3px;
  font-size: 0.7rem;
  font-weight: 600;
  background: var(--yellow-subtle);
  color: var(--yellow-400);
}

.col-packages {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  max-width: 600px;
  overflow: hidden;
}

.pkg-list {
  display: flex;
  gap: 0.25rem;
  align-items: center;
  overflow: hidden;
  flex: 1;
  min-width: 0;
}

.pkg-tag {
  display: inline-flex;
  padding: 0.15rem 0.4rem;
  background: var(--surface-overlay);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  font-size: 0.65rem;
  font-family: 'JetBrains Mono', monospace;
  color: var(--text-color-secondary);
  white-space: nowrap;
  flex-shrink: 0;
}

.expand-btn {
  padding: 0.15rem 0.4rem;
  background: none;
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  font-size: 0.65rem;
  font-family: inherit;
  color: var(--primary-color);
  cursor: pointer;
  transition: all 0.15s;
  flex-shrink: 0;
  white-space: nowrap;
}

.expand-btn:hover {
  border-color: var(--primary-color);
}

.metric-blank {
  color: var(--text-color-muted);
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

.mobile-filter-toggle {
  display: none;
}

.mobile-filter-panel {
  display: none;
}

.pkg-dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1rem;
}

.pkg-dialog {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: calc(100% - 2rem);
  max-width: 600px;
  max-height: 60vh;
  display: flex;
  flex-direction: column;
  margin: auto;
}

.pkg-dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  flex-shrink: 0;
}

.pkg-dialog-title {
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--text-color);
}

.pkg-dialog-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.75rem;
  height: 1.75rem;
  background: none;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 0.7rem;
  transition: all 0.15s;
}

.pkg-dialog-close:hover {
  color: var(--text-color);
  border-color: var(--text-color-muted);
}

.pkg-dialog-body {
  padding: 0.75rem 1rem;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.pkg-dialog-body .pkg-tag {
  white-space: normal;
  word-break: break-word;
  flex-shrink: initial;
}

.mobile-only {
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

  .page-header {
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .desktop-only {
    display: none;
  }

  .mobile-only {
    display: block;
  }

  .summary-row {
    flex-wrap: wrap;
  }

  .summary-card {
    flex: 1 1 calc(50% - 0.5rem);
    min-width: 0;
    padding: 0.75rem;
  }

  .summary-value {
    font-size: 1.25rem;
  }

  .mobile-cards {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .mobile-card {
    background: var(--surface-card);
    border: 1px solid var(--surface-border);
    border-radius: 4px;
    padding: 0.75rem;
    cursor: pointer;
    transition: background 0.15s;
  }

  .mobile-card:hover {
    background: var(--surface-hover);
  }

  .mobile-card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .mobile-card-right {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

}
</style>

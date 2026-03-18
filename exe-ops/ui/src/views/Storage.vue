<template>
  <div class="storage-view">
    <div class="page-header">
      <div>
        <h1>Storage</h1>
        <p class="page-subtitle">Fleet-wide ZFS overview</p>
      </div>
      <div class="header-right">
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
      <!-- Summary cards -->
      <div class="summary-row" v-if="allPools.length > 0">
        <div class="summary-card">
          <div class="summary-value">{{ allPools.length }}</div>
          <div class="summary-label">Total Pools</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-green">{{ healthyCount }}</div>
          <div class="summary-label">Healthy</div>
        </div>
        <div class="summary-card" v-if="degradedCount > 0">
          <div class="summary-value value-yellow">{{ degradedCount }}</div>
          <div class="summary-label">Degraded</div>
        </div>
        <div class="summary-card" v-if="faultedCount > 0">
          <div class="summary-value value-red">{{ faultedCount }}</div>
          <div class="summary-label">Faulted</div>
        </div>
        <div class="summary-card" v-if="fleetUsagePct != null">
          <div class="summary-value" :class="usagePctClass(fleetUsagePct)">{{ fleetUsagePct.toFixed(1) }}%</div>
          <div class="summary-label">Fleet Used</div>
        </div>
        <div class="summary-card" v-if="avgFragPct != null">
          <div class="summary-value">{{ avgFragPct.toFixed(0) }}%</div>
          <div class="summary-label">
            Avg Frag
            <span class="header-hint" tabindex="0">
              <i class="pi pi-question-circle"></i>
              <span class="hint-popup">Average ZFS fragmentation across all pools. High fragmentation (>50%) can degrade write performance as free space becomes scattered.</span>
            </span>
          </div>
        </div>
        <div class="summary-card" v-if="arcServers.length > 0 && fleetArcHitRate != null">
          <div class="summary-value">{{ fleetArcHitRate.toFixed(1) }}%</div>
          <div class="summary-label">
            ARC Hit Rate
            <span class="header-hint" tabindex="0">
              <i class="pi pi-question-circle"></i>
              <span class="hint-popup">Percentage of read requests served from the ARC cache rather than disk. Higher is better — values above 90% indicate effective caching.</span>
            </span>
          </div>
        </div>
        <div class="summary-card" v-if="arcServers.length > 0 && fleetArcSize != null">
          <div class="summary-value">{{ formatBytes(fleetArcSize) }}</div>
          <div class="summary-label">
            ARC Size
            <span class="header-hint" tabindex="0">
              <i class="pi pi-question-circle"></i>
              <span class="hint-popup">The Adaptive Replacement Cache (ARC) is ZFS's in-memory read cache. ARC size shows how much RAM is currently used by the cache across the fleet.</span>
            </span>
          </div>
        </div>
      </div>

      <div v-if="allPools.length === 0" class="empty-state">
        No ZFS pools found across the fleet.
      </div>

      <!-- Tabs -->
      <template v-if="allPools.length > 0">
      <div class="tab-bar">
        <button
          class="tab-btn"
          :class="{ active: activeTab === 'servers' }"
          @click="activeTab = 'servers'"
        >
          Servers
          <span class="tab-count">{{ allPools.length }}</span>
        </button>
        <button
          v-if="arcServers.length > 0"
          class="tab-btn"
          :class="{ active: activeTab === 'stats' }"
          @click="activeTab = 'stats'"
        >
          Stats
          <span class="tab-count">{{ arcServers.length }}</span>
        </button>
      </div>

      <!-- Servers tab: Pools table -->
      <div v-show="activeTab === 'servers'">
      <div class="table-wrapper desktop-only">
        <table class="pool-table">
          <thead>
            <tr>
              <th>Server</th>
              <th>Pool</th>
              <th>Health</th>
              <th>Capacity</th>
              <th>Frag %</th>
              <th>Errors (R/W/C)</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in allPools" :key="p.key" class="pool-row" @click="$router.push(`/servers/${p.server}`)">
              <td class="col-server">{{ p.server }}</td>
              <td>{{ p.pool.name }}</td>
              <td>
                <span class="health-badge" :class="healthClass(p.pool.health)">{{ p.pool.health }}</span>
              </td>
              <td class="col-capacity">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill" :class="barClass(p.pool.cap_pct)" :style="{ width: p.pool.cap_pct + '%' }"></div>
                  </div>
                  <span class="inline-bar-value" :class="barClass(p.pool.cap_pct)">{{ p.pool.cap_pct }}%</span>
                </div>
              </td>
              <td class="col-mono">{{ p.pool.frag_pct >= 0 ? p.pool.frag_pct + '%' : 'N/A' }}</td>
              <td class="col-mono" :class="{ 'value-red': p.pool.read_errors + p.pool.write_errors + p.pool.cksum_errors > 0 }">
                {{ p.pool.read_errors }}/{{ p.pool.write_errors }}/{{ p.pool.cksum_errors }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Pools mobile cards -->
      <div class="mobile-cards mobile-only">
        <div v-for="p in allPools" :key="p.key" class="mobile-card" @click="$router.push(`/servers/${p.server}`)">
          <div class="mobile-card-header">
            <span class="col-server">{{ p.server }}</span>
            <span class="health-badge" :class="healthClass(p.pool.health)">{{ p.pool.health }}</span>
          </div>
          <div class="mobile-card-pool-name">{{ p.pool.name }}</div>
          <div class="mobile-card-metrics">
            <div class="mobile-metric">
              <span class="mobile-metric-label">Capacity</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill" :class="barClass(p.pool.cap_pct)" :style="{ width: p.pool.cap_pct + '%' }"></div>
                </div>
                <span class="inline-bar-value" :class="barClass(p.pool.cap_pct)">{{ p.pool.cap_pct }}%</span>
              </div>
            </div>
          </div>
          <div class="mobile-card-details">
            <span class="mobile-detail">
              <span class="mobile-metric-label">Frag</span>
              <span class="col-mono">{{ p.pool.frag_pct >= 0 ? p.pool.frag_pct + '%' : 'N/A' }}</span>
            </span>
            <span class="mobile-detail">
              <span class="mobile-metric-label">Errors (R/W/C)</span>
              <span class="col-mono" :class="{ 'value-red': p.pool.read_errors + p.pool.write_errors + p.pool.cksum_errors > 0 }">
                {{ p.pool.read_errors }}/{{ p.pool.write_errors }}/{{ p.pool.cksum_errors }}
              </span>
            </span>
          </div>
        </div>
      </div>
      </div>

      <!-- Stats tab: ARC stats -->
      <div v-show="activeTab === 'stats'" v-if="arcServers.length > 0">
        <div class="table-wrapper desktop-only">
          <table class="pool-table">
            <thead>
              <tr>
                <th>Server</th>
                <th>ARC Size</th>
                <th>Hit Rate</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in arcServers" :key="s.name" class="pool-row" @click="$router.push(`/servers/${s.name}`)">
                <td class="col-server">{{ s.name }}</td>
                <td class="col-mono">{{ formatBytes(s.zfs_arc_size!) }}</td>
                <td class="col-mono">{{ s.zfs_arc_hit_rate!.toFixed(1) }}%</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="mobile-cards mobile-only">
          <div v-for="s in arcServers" :key="s.name" class="mobile-card" @click="$router.push(`/servers/${s.name}`)">
            <div class="mobile-card-header">
              <span class="col-server">{{ s.name }}</span>
            </div>
            <div class="mobile-card-details">
              <span class="mobile-detail">
                <span class="mobile-metric-label">ARC Size</span>
                <span class="col-mono">{{ formatBytes(s.zfs_arc_size!) }}</span>
              </span>
              <span class="mobile-detail">
                <span class="mobile-metric-label">Hit Rate</span>
                <span class="col-mono">{{ s.zfs_arc_hit_rate!.toFixed(1) }}%</span>
              </span>
            </div>
          </div>
        </div>
      </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { fetchFleet, type FleetServer, type ZFSPool } from '../api/client'

const servers = ref<FleetServer[]>([])
const loading = ref(true)
const error = ref('')
const activeRegions = reactive(new Set<string>())
const activeEnvs = reactive(new Set<string>())
const activeTab = ref<'servers' | 'stats'>('servers')
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

interface PoolRow {
  key: string
  server: string
  pool: ZFSPool
}

const allPools = computed((): PoolRow[] => {
  const pools: PoolRow[] = []
  for (const s of filteredServers.value) {
    if (s.zfs_pools) {
      for (const p of s.zfs_pools) {
        pools.push({ key: `${s.name}-${p.name}`, server: s.name, pool: p })
      }
    }
  }
  return pools
})

const healthyCount = computed(() => allPools.value.filter(p => p.pool.health === 'ONLINE').length)
const degradedCount = computed(() => allPools.value.filter(p => p.pool.health === 'DEGRADED').length)
const faultedCount = computed(() => allPools.value.filter(p => p.pool.health !== 'ONLINE' && p.pool.health !== 'DEGRADED').length)

const arcServers = computed(() => filteredServers.value.filter(s => s.zfs_arc_size != null && s.zfs_arc_hit_rate != null))

const fleetUsagePct = computed((): number | null => {
  const pools = allPools.value
  if (pools.length === 0) return null
  let totalUsed = 0
  let totalSize = 0
  for (const p of pools) {
    totalUsed += p.pool.used
    totalSize += p.pool.used + p.pool.free
  }
  if (totalSize === 0) return null
  return (totalUsed / totalSize) * 100
})

const fleetArcHitRate = computed((): number | null => {
  const srvs = arcServers.value
  if (srvs.length === 0) return null
  let weightedSum = 0
  let totalArc = 0
  for (const s of srvs) {
    weightedSum += s.zfs_arc_size! * s.zfs_arc_hit_rate!
    totalArc += s.zfs_arc_size!
  }
  if (totalArc === 0) return null
  return weightedSum / totalArc
})

const fleetArcSize = computed((): number | null => {
  const srvs = arcServers.value
  if (srvs.length === 0) return null
  let total = 0
  for (const s of srvs) {
    total += s.zfs_arc_size!
  }
  return total
})

const avgFragPct = computed((): number | null => {
  const valid = allPools.value.filter(p => p.pool.frag_pct >= 0)
  if (valid.length === 0) return null
  let sum = 0
  for (const p of valid) {
    sum += p.pool.frag_pct
  }
  return sum / valid.length
})

function healthClass(health: string): string {
  if (health === 'ONLINE') return 'health-online'
  if (health === 'DEGRADED') return 'health-degraded'
  return 'health-faulted'
}

function barClass(percent: number): string {
  if (percent >= 90) return 'bar-danger'
  if (percent >= 70) return 'bar-warning'
  return 'bar-normal'
}

function usagePctClass(percent: number): string {
  if (percent >= 90) return 'value-red'
  if (percent >= 70) return 'value-yellow'
  return 'value-green'
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i]
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

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
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

.summary-row {
  display: flex;
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.summary-card {
  flex: 1 1 0;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1rem 1.5rem;
  min-width: 0;
  text-align: center;
  position: relative;
  overflow: visible;
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

.pool-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.pool-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.pool-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
}

.pool-row {
  cursor: pointer;
  transition: background 0.15s;
}

.pool-row:hover {
  background: var(--surface-hover);
}

.pool-row:last-child td {
  border-bottom: none;
}

.col-server {
  font-weight: 600;
  color: var(--text-color);
}

.col-mono {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.col-capacity {
  min-width: 140px;
}

.health-badge {
  display: inline-flex;
  padding: 0.15rem 0.4rem;
  border-radius: 3px;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.health-online {
  background: var(--green-subtle);
  color: var(--green-400);
}

.health-degraded {
  background: var(--yellow-subtle);
  color: var(--yellow-400);
}

.health-faulted {
  background: var(--red-subtle);
  color: var(--red-400);
}

.inline-bar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.inline-bar-track {
  flex: 1;
  height: 6px;
  background: var(--surface-overlay);
  border-radius: 99px;
  overflow: hidden;
  min-width: 50px;
}

.inline-bar-fill {
  height: 100%;
  border-radius: 99px;
  transition: width 0.4s ease;
}

.inline-bar-fill.bar-normal { background: var(--green-500); }
.inline-bar-fill.bar-warning { background: var(--yellow-400); }
.inline-bar-fill.bar-danger { background: var(--red-400); }

.inline-bar-value {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  font-weight: 600;
  min-width: 3ch;
  text-align: right;
}

.inline-bar-value.bar-normal { color: var(--green-500); }
.inline-bar-value.bar-warning { color: var(--yellow-400); }
.inline-bar-value.bar-danger { color: var(--red-400); }

.header-hint {
  position: relative;
  display: inline-flex;
  align-items: center;
  margin-left: 0.35rem;
  cursor: help;
  vertical-align: middle;
}

.header-hint .pi-question-circle {
  font-size: 0.7rem;
  color: var(--text-color-muted);
  transition: color 0.15s;
}

.header-hint:hover .pi-question-circle,
.header-hint:focus .pi-question-circle {
  color: var(--primary-color);
}

.hint-popup {
  display: none;
  position: absolute;
  bottom: calc(100% + 8px);
  left: 50%;
  transform: translateX(-50%);
  width: 260px;
  padding: 0.6rem 0.75rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3);
  font-size: 0.75rem;
  font-weight: 400;
  text-transform: none;
  letter-spacing: normal;
  line-height: 1.5;
  color: var(--text-color-secondary);
  white-space: normal;
  z-index: 1000;
  pointer-events: none;
}

.header-hint:hover .hint-popup,
.header-hint:focus .hint-popup {
  display: block;
}

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
    margin-bottom: 0.25rem;
  }

  .mobile-card-pool-name {
    font-size: 0.8rem;
    color: var(--text-color-secondary);
    margin-bottom: 0.5rem;
  }

  .mobile-card-metrics {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    margin-bottom: 0.5rem;
  }

  .mobile-metric {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .mobile-metric-label {
    font-size: 0.65rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-color-muted);
    flex-shrink: 0;
  }

  .mobile-metric .inline-bar {
    flex: 1;
  }

  .mobile-card-details {
    display: flex;
    gap: 1rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--surface-border);
  }

  .mobile-detail {
    display: flex;
    align-items: center;
    gap: 0.375rem;
  }
}
</style>

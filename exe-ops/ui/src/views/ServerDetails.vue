<template>
  <div class="server-details" v-if="server">
    <div class="page-header">
      <div>
        <router-link to="/servers" class="back-link">
          <i class="pi pi-arrow-left"></i> Servers
        </router-link>
        <h1>{{ server.name }}</h1>
        <div class="page-meta">
          <span class="meta-badge" v-if="server.region">
            <i class="pi pi-map-marker"></i>
            {{ server.region }}
          </span>
          <span class="meta-badge" v-if="server.env">{{ server.env }}</span>
          <span class="meta-badge role" v-if="server.role">{{ server.role }}</span>
          <span class="meta-badge" v-if="server.arch">{{ server.arch }}</span>
          <span class="meta-badge" v-if="server.agent_version && server.agent_version !== 'dev'">{{ server.agent_version }}</span>
          <TagList v-if="server.tags && server.tags.length > 0" :tags="server.tags" />
        </div>
      </div>
      <div class="header-right">
        <span class="header-uptime">Up {{ formatUptime(server.uptime_secs) }}</span>
        <span class="header-lastseen">Last seen {{ formatTimeRelative(server.last_seen) }}</span>
        <span class="status-pill" :class="isOnline ? 'online' : 'offline'">
          <span class="status-dot"></span>
          {{ isOnline ? 'Online' : 'Offline' }}
        </span>
      </div>
    </div>

    <!-- Time Period Selector + Charts -->
    <div class="charts-section" v-if="server.history && server.history.length > 0">
      <div class="charts-toolbar">
        <span class="toolbar-label">
          <i class="pi pi-chart-line"></i>
          History
          <a
            :href="`https://grafana.crocodile-vector.ts.net/d/hosts-dashboard/hosts-dashboard?orgId=1&from=now-6h&to=now&timezone=browser&var-instance=${server.name}:9100&var-role=$__all&var-stage=$__all&refresh=1m`"
            target="_blank"
            rel="noopener noreferrer"
            class="grafana-link"
            title="View in Grafana"
          >
            <i class="pi pi-external-link"></i>
            Grafana
          </a>
        </span>
        <div class="period-selector">
          <button
            v-for="p in periods" :key="p.value"
            class="period-btn" :class="{ active: selectedPeriod === p.value }"
            @click="selectedPeriod = p.value"
          >{{ p.label }}</button>
        </div>
      </div>
      <div class="charts-grid">
        <MetricsChart
          title="CPU"
          :series="cpuSeries"
          :maxValue="100"
          :warningThreshold="70"
          :criticalThreshold="90"
          :periodMinutes="selectedPeriod"
        />
        <MetricsChart
          title="Memory"
          :series="memSeries"
          unit=" GB"
          :maxValue="memMaxGB"
          :periodMinutes="selectedPeriod"
        />
        <MetricsChart
          title="Disk"
          :series="diskSeries"
          unit=" GB"
          :maxValue="diskMaxGB"
          :periodMinutes="selectedPeriod"
        />
        <MetricsChart
          title="Network"
          :series="netSeries"
          unit=" MB"
          :periodMinutes="selectedPeriod"
        />
      </div>
    </div>

    <!-- Stat Tiles -->
    <div class="stat-tiles">
      <div class="stat-tile">
        <span class="stat-label">Swap</span>
        <span class="stat-value">{{ formatBytes(server.mem_swap) }} / {{ formatBytes(server.mem_swap_total) }}</span>
      </div>
      <div class="stat-tile">
        <span class="stat-label">Net TX</span>
        <span class="stat-value">{{ formatBytes(server.net_send) }}</span>
      </div>
      <div class="stat-tile">
        <span class="stat-label">Net RX</span>
        <span class="stat-value">{{ formatBytes(server.net_recv) }}</span>
      </div>
      <div class="stat-tile">
        <span class="stat-label">Load Avg</span>
        <span class="stat-value">{{ server.load_avg_1.toFixed(2) }} / {{ server.load_avg_5.toFixed(2) }} / {{ server.load_avg_15.toFixed(2) }}</span>
      </div>
      <div class="stat-tile">
        <span class="stat-label">File Descriptors</span>
        <span class="stat-value">{{ formatCompact(server.fd_allocated) }} / {{ fdMaxDisplay }}</span>
        <span v-if="!fdUnlimited && fdUsagePercent > 50" class="stat-muted">{{ fdUsagePercent.toFixed(1) }}% used</span>
      </div>
    </div>

    <!-- Exelet Capacity -->
    <div class="sections-row sections-row-single" v-if="hasExeletCapacity">
      <div class="section">
        <div class="section-heading">
          <i class="pi pi-server"></i>
          <span>Exelet Capacity</span>
          <span class="meta-badge" v-if="latestCapacity">{{ latestCapacity.instances }} / {{ latestCapacity.capacity }}</span>
        </div>
        <div class="exelet-capacity-grid">
          <div class="exelet-capacity-left">
            <MetricsChart
              title="Instances"
              :series="instancesSeries"
              unit=""
              :integerValues="true"
              :maxValue="instancesMax"
              :periodMinutes="selectedPeriod"
            />
          </div>
          <div class="exelet-gauge-wrapper" v-if="latestCapacity">
            <div class="exelet-gauge-header">
              <span class="exelet-gauge-title">Current</span>
            </div>
            <div class="exelet-gauge-body">
              <div class="exelet-gauge">
                <svg viewBox="0 0 160 160" class="gauge-svg">
                  <circle cx="80" cy="80" r="64" fill="none" stroke="var(--surface-overlay)" stroke-width="14" />
                  <circle cx="80" cy="80" r="64" fill="none" stroke="var(--primary-color)" stroke-width="14"
                    stroke-linecap="round"
                    :stroke-dasharray="gaugeCircumference"
                    :stroke-dashoffset="gaugeOffset"
                    transform="rotate(-90 80 80)"
                  />
                  <text x="80" y="76" text-anchor="middle" font-size="24" font-weight="700" fill="var(--text-color)" font-family="'JetBrains Mono', monospace">
                    {{ latestCapacity.instances }}
                  </text>
                  <text x="80" y="94" text-anchor="middle" font-size="11" fill="var(--text-color-muted)" font-family="'JetBrains Mono', monospace">
                    / {{ latestCapacity.capacity }}
                  </text>
                </svg>
              </div>
              <div class="exelet-stat-cards">
                <div class="exelet-stat-card">
                  <span class="exelet-stat-label">Instances</span>
                  <span class="exelet-stat-value">{{ formatNumber(latestCapacity.instances) }}</span>
                </div>
                <div class="exelet-stat-card">
                  <span class="exelet-stat-label">Capacity</span>
                  <span class="exelet-stat-value">{{ formatNumber(latestCapacity.capacity) }}</span>
                </div>
                <div class="exelet-stat-card">
                  <span class="exelet-stat-label">Utilization</span>
                  <span class="exelet-stat-value">{{ capacityUtilPct }}%</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ZFS Storage (standalone, only shown when present) -->
    <div class="sections-row sections-row-single" v-if="hasZFS">
      <div class="section">
        <div class="section-heading">
          <i class="pi pi-database"></i>
          <span>ZFS Storage</span>
          <span v-if="!hasZFSPools && server.zfs_pool_health != null" class="health-badge" :class="healthBadgeClass">{{ server.zfs_pool_health }}</span>
        </div>
        <!-- Per-pool view (new agent) -->
        <div v-if="hasZFSPools" class="zfs-pools-grid">
          <div v-for="pool in sortedZFSPools" :key="pool.name" class="zfs-pool">
            <div class="zfs-pool-header">
              <span class="zfs-pool-name">{{ pool.name }}</span>
              <span class="health-badge" :class="poolHealthClass(pool.health)">{{ pool.health }}</span>
            </div>
            <MetricBar :label="pool.name" :used="pool.used" :total="pool.used + pool.free" format="bytes" />
            <div class="section-inline">
              <span class="stat-label">Used: <span class="stat-value">{{ formatBytes(pool.used) }}</span></span>
              <span class="stat-label">Free: <span class="stat-value">{{ formatBytes(pool.free) }}</span></span>
            </div>
            <div class="zfs-pool-stats">
              <span v-if="pool.frag_pct >= 0" class="stat-label">Frag: <span class="stat-value" :class="fragClass(pool.frag_pct)">{{ pool.frag_pct }}%</span></span>
              <span class="stat-label">Cap: <span class="stat-value" :class="capClass(pool.cap_pct)">{{ pool.cap_pct }}%</span></span>
              <span class="stat-label">Read Err: <span class="stat-value" :class="{ 'error-count-warn': pool.read_errors > 0 }">{{ formatNumber(pool.read_errors) }}</span></span>
              <span class="stat-label">Write Err: <span class="stat-value" :class="{ 'error-count-warn': pool.write_errors > 0 }">{{ formatNumber(pool.write_errors) }}</span></span>
              <span class="stat-label">Cksum Err: <span class="stat-value" :class="{ 'error-count-warn': pool.cksum_errors > 0 }">{{ formatNumber(pool.cksum_errors) }}</span></span>
            </div>
          </div>
        </div>
        <!-- Legacy view (old agent without zfs_pools) -->
        <div v-else class="zfs-pools-grid">
          <div v-if="server.zfs_used != null" class="zfs-pool">
            <MetricBar label="Tank" :used="server.zfs_used!" :total="(server.zfs_used! + server.zfs_free!)" format="bytes" />
            <div class="section-inline">
              <span class="stat-label">Used: <span class="stat-value">{{ formatBytes(server.zfs_used!) }}</span></span>
              <span class="stat-label">Free: <span class="stat-value">{{ formatBytes(server.zfs_free!) }}</span></span>
            </div>
          </div>
          <div v-if="server.backup_zfs_used != null" class="zfs-pool">
            <MetricBar label="Backup" :used="server.backup_zfs_used!" :total="(server.backup_zfs_used! + server.backup_zfs_free!)" format="bytes" />
            <div class="section-inline">
              <span class="stat-label">Used: <span class="stat-value">{{ formatBytes(server.backup_zfs_used!) }}</span></span>
              <span class="stat-label">Free: <span class="stat-value">{{ formatBytes(server.backup_zfs_free!) }}</span></span>
            </div>
          </div>
        </div>
        <div v-if="server.zfs_arc_size != null" class="section-inline zfs-arc-stats">
          <span class="stat-label">ARC Size: <span class="stat-value">{{ formatBytes(server.zfs_arc_size!) }}</span></span>
          <span class="stat-label">Hit Rate: <span class="stat-value" :class="arcHitRateClass">{{ server.zfs_arc_hit_rate != null ? server.zfs_arc_hit_rate.toFixed(1) + '%' : '-' }}</span></span>
        </div>
      </div>
    </div>

    <!-- Network + Components + Failed Services row -->
    <div class="sections-row sections-row-auto">
      <!-- Network -->
      <div class="section" v-if="hasNetworkHealth">
        <div class="section-heading" :class="{ 'updates-toggle': !isDesktop }" @click="!isDesktop && (networkExpanded = !networkExpanded)">
          <i class="pi pi-link"></i>
          <span>Network</span>
          <span v-if="networkErrorCount > 0" class="title-badge badge-danger">{{ formatCompact(networkErrorCount) }}</span>
          <i v-if="!isDesktop" class="pi toggle-icon" :class="networkExpanded ? 'pi-chevron-up' : 'pi-chevron-down'"></i>
        </div>
        <template v-if="isDesktop || networkExpanded">
          <div class="section-inline health-stats">
            <span class="health-stat" :class="{ 'health-stat-warn': server.net_rx_errors > 0 }">RX Errors: {{ formatNumber(server.net_rx_errors) }}</span>
            <span class="health-stat" :class="{ 'health-stat-warn': server.net_rx_dropped > 0 }">RX Drops: {{ formatNumber(server.net_rx_dropped) }}</span>
            <span class="health-stat" :class="{ 'health-stat-warn': server.net_tx_errors > 0 }">TX Errors: {{ formatNumber(server.net_tx_errors) }}</span>
            <span class="health-stat" :class="{ 'health-stat-warn': server.net_tx_dropped > 0 }">TX Drops: {{ formatNumber(server.net_tx_dropped) }}</span>
          </div>
          <div v-if="server.conntrack_count != null" class="conntrack-section">
            <MetricBar label="Conntrack" :used="server.conntrack_count!" :total="server.conntrack_max!" />
            <div class="section-inline">
              <span class="stat-label"><span class="stat-value">{{ formatNumber(server.conntrack_count!) }} / {{ formatNumber(server.conntrack_max!) }}</span> connections</span>
            </div>
          </div>
        </template>
        <span v-else class="updates-summary">
          <template v-if="networkErrorCount > 0">{{ formatCompact(networkErrorCount) }} errors/drops</template>
          <template v-else-if="server.conntrack_count != null">{{ formatNumber(server.conntrack_count!) }} connections tracked</template>
          <template v-else>No issues</template>
        </span>
      </div>

      <!-- Components -->
      <div class="section" v-if="server.components?.length">
        <div class="section-heading">
          <i class="pi pi-box"></i>
          <span>Components</span>
        </div>
        <div class="component-row">
          <div class="component-item" v-for="comp in server.components" :key="comp.name">
            <span class="component-name">{{ comp.name }}</span>
            <span class="component-version">{{ comp.version }}</span>
            <span class="component-status" :class="'status-' + comp.status">{{ comp.status }}</span>
          </div>
        </div>
      </div>

      <!-- Failed Services -->
      <div class="section">
        <div class="section-heading" :class="{ 'updates-toggle': !isDesktop && failedUnitsCount > 0 }" @click="!isDesktop && failedUnitsCount > 0 && (failedUnitsExpanded = !failedUnitsExpanded)">
          <i class="pi pi-exclamation-circle" :style="{ color: failedUnitsCount > 0 ? 'var(--red-400)' : undefined }"></i>
          <span>Failed Services</span>
          <span class="title-badge" :class="{ 'badge-danger': failedUnitsCount > 0 }">{{ failedUnitsCount }}</span>
          <i v-if="!isDesktop && failedUnitsCount > 0" class="pi toggle-icon" :class="failedUnitsExpanded ? 'pi-chevron-up' : 'pi-chevron-down'"></i>
        </div>
        <template v-if="failedUnitsCount > 0">
          <div v-if="isDesktop || failedUnitsExpanded" class="failed-units-list failed-units-scroll">
            <div class="failed-unit-item" v-for="unit in server.failed_units" :key="unit">
              <i class="pi pi-times-circle"></i>
              <span>{{ unit }}</span>
            </div>
          </div>
          <span v-else class="updates-summary" style="color: var(--red-400)">{{ failedUnitsCount }} failed {{ failedUnitsCount === 1 ? 'service' : 'services' }}</span>
        </template>
        <span v-else class="section-ok">All services running</span>
      </div>
    </div>

    <!-- Updates -->
    <div class="section" v-if="server.updates?.length">
      <div class="section-heading updates-toggle" @click="updatesExpanded = !updatesExpanded">
        <i class="pi pi-download"></i>
        <span>Available Updates</span>
        <span class="title-badge">{{ server.updates.length }}</span>
        <i class="pi toggle-icon" :class="updatesExpanded ? 'pi-chevron-up' : 'pi-chevron-down'"></i>
      </div>
      <ul class="updates-list" v-if="updatesExpanded">
        <li v-for="update in server.updates" :key="update">{{ update }}</li>
      </ul>
      <span class="updates-summary" v-else>{{ server.updates.length }} updates available</span>
    </div>
  </div>

  <div v-else-if="loading" class="loading-state">
    <i class="pi pi-spin pi-spinner"></i>
    <span>Loading server details...</span>
  </div>

  <div v-else-if="error" class="message-banner message-error">
    <i class="pi pi-exclamation-triangle"></i>
    <span>{{ error }}</span>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { fetchServer, type ServerDetail, type ZFSPool, type ExeletCapacityRow } from '../api/client'
import MetricBar from '../components/MetricBar.vue'
import MetricsChart from '../components/MetricsChart.vue'
import TagList from '../components/TagList.vue'

const route = useRoute()
const server = ref<ServerDetail | null>(null)
const loading = ref(true)
const error = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

const periods = [
  { label: '1h', value: 60 },
  { label: '6h', value: 360 },
  { label: '24h', value: 1440 },
  { label: '3d', value: 4320 },
  { label: '7d', value: 10080 },
]
const selectedPeriod = ref(parseInt(sessionStorage.getItem('serverDetailsPeriod') || '60', 10))
watch(selectedPeriod, (v) => sessionStorage.setItem('serverDetailsPeriod', String(v)))
const updatesExpanded = ref(false)
const failedUnitsExpanded = ref(false)
const networkExpanded = ref(false)

const isDesktop = ref(window.innerWidth > 991)
function onResize() { isDesktop.value = window.innerWidth > 991 }

const failedUnitsCount = computed(() => server.value?.failed_units?.length ?? 0)

// fd_max at or above 2^53 is effectively "unlimited" (kernel returns MaxInt64)
const fdUnlimited = computed(() => {
  if (!server.value) return false
  return server.value.fd_max >= 9_007_199_254_740_992
})

const fdMaxDisplay = computed(() => {
  if (!server.value) return '-'
  if (fdUnlimited.value) return '∞'
  return formatCompact(server.value.fd_max)
})

const fdUsagePercent = computed(() => {
  if (!server.value || server.value.fd_max === 0 || fdUnlimited.value) return 0
  return (server.value.fd_allocated / server.value.fd_max) * 100
})

const networkErrorCount = computed(() => {
  if (!server.value) return 0
  return server.value.net_rx_errors + server.value.net_rx_dropped +
    server.value.net_tx_errors + server.value.net_tx_dropped
})

const hasNetworkHealth = computed(() => {
  if (!server.value) return false
  return networkErrorCount.value > 0 || server.value.conntrack_count != null
})

const healthBadgeClass = computed(() => {
  const health = server.value?.zfs_pool_health
  if (health === 'ONLINE') return 'health-badge-green'
  if (health === 'DEGRADED') return 'health-badge-yellow'
  if (health === 'FAULTED') return 'health-badge-red'
  return ''
})

const arcHitRateClass = computed(() => {
  const rate = server.value?.zfs_arc_hit_rate
  if (rate == null) return ''
  if (rate >= 90) return 'arc-rate-green'
  if (rate >= 70) return 'arc-rate-yellow'
  return 'arc-rate-red'
})

const hasZFSPools = computed(() => {
  return server.value?.zfs_pools && server.value.zfs_pools.length > 0
})

const sortedZFSPools = computed(() => {
  if (!server.value?.zfs_pools) return []
  return [...server.value.zfs_pools].sort((a, b) => {
    if (a.name === 'tank') return -1
    if (b.name === 'tank') return 1
    return a.name.localeCompare(b.name)
  })
})

const hasZFS = computed(() => {
  return hasZFSPools.value || server.value?.zfs_used != null || server.value?.backup_zfs_used != null
})

function poolHealthClass(health: string): string {
  if (health === 'ONLINE') return 'health-badge-green'
  if (health === 'DEGRADED') return 'health-badge-yellow'
  if (health === 'FAULTED') return 'health-badge-red'
  return ''
}

function fragClass(pct: number): string {
  if (pct > 70) return 'zfs-warn-red'
  if (pct > 50) return 'zfs-warn-yellow'
  return ''
}

function capClass(pct: number): string {
  if (pct > 90) return 'zfs-warn-red'
  if (pct > 80) return 'zfs-warn-yellow'
  return ''
}

const isOnline = computed(() => {
  if (!server.value) return false
  const ago = Date.now() - new Date(server.value.last_seen).getTime()
  return ago < 120_000
})

const filteredHistory = computed(() => {
  if (!server.value?.history) return []
  const cutoff = Date.now() - selectedPeriod.value * 60 * 1000
  return server.value.history.filter(r => new Date(r.timestamp).getTime() >= cutoff)
})

const cpuSeries = computed(() => [{
  name: 'CPU',
  color: 'var(--primary-color)',
  data: filteredHistory.value.map(r => ({ timestamp: r.timestamp, value: r.cpu_percent }))
}])

const bytesToGB = (b: number) => b / (1024 * 1024 * 1024)

const memMaxGB = computed(() => {
  if (!server.value) return 10
  return Math.ceil(bytesToGB(server.value.mem_total))
})

const memSeries = computed(() => [{
  name: 'Memory',
  color: 'var(--green-400)',
  data: filteredHistory.value.map(r => ({ timestamp: r.timestamp, value: parseFloat(bytesToGB(r.mem_used).toFixed(2)) }))
}])

const diskMaxGB = computed(() => {
  if (!server.value) return 10
  return Math.ceil(bytesToGB(server.value.disk_total))
})

const diskSeries = computed(() => [{
  name: 'Disk',
  color: 'var(--yellow-400)',
  data: filteredHistory.value.map(r => ({ timestamp: r.timestamp, value: parseFloat(bytesToGB(r.disk_used).toFixed(2)) }))
}])

const bytesToMB = (b: number) => b / (1024 * 1024)

const netSeries = computed(() => [
  {
    name: 'Send',
    color: 'var(--blue-400)',
    data: filteredHistory.value.map(r => ({ timestamp: r.timestamp, value: parseFloat(bytesToMB(r.net_send).toFixed(2)) }))
  },
  {
    name: 'Recv',
    color: 'var(--primary-color)',
    data: filteredHistory.value.map(r => ({ timestamp: r.timestamp, value: parseFloat(bytesToMB(r.net_recv).toFixed(2)) }))
  }
])

const hasExeletCapacity = computed(() => {
  return server.value?.exelet_capacity && server.value.exelet_capacity.length > 0
})

const filteredCapacity = computed(() => {
  if (!server.value?.exelet_capacity) return []
  const cutoff = Date.now() - selectedPeriod.value * 60 * 1000
  return server.value.exelet_capacity.filter(r => new Date(r.timestamp).getTime() >= cutoff)
})

const instancesMax = computed(() => {
  if (!filteredCapacity.value.length) return 100
  const maxCap = Math.max(...filteredCapacity.value.map(r => r.capacity))
  return maxCap > 0 ? maxCap : 100
})

const latestCapacity = computed(() => {
  if (!server.value?.exelet_capacity?.length) return null
  return server.value.exelet_capacity[0]
})

const capacityUtilPct = computed(() => {
  if (!latestCapacity.value || latestCapacity.value.capacity === 0) return '0.0'
  return ((latestCapacity.value.instances / latestCapacity.value.capacity) * 100).toFixed(1)
})

const gaugeCircumference = 2 * Math.PI * 64 // r=64

const gaugeOffset = computed(() => {
  if (!latestCapacity.value || latestCapacity.value.capacity === 0) return gaugeCircumference
  const pct = Math.min(1, latestCapacity.value.instances / latestCapacity.value.capacity)
  return gaugeCircumference * (1 - pct)
})

const instancesSeries = computed(() => [{
  name: 'Instances',
  color: 'var(--primary-color)',
  data: filteredCapacity.value.map(r => ({ timestamp: r.timestamp, value: r.instances }))
}])

async function load() {
  try {
    const name = route.params.name as string
    server.value = await fetchServer(name)
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load server'
  } finally {
    loading.value = false
  }
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i]
}

function formatNumber(n: number): string {
  return n.toLocaleString()
}

function formatCompact(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(1).replace(/\.0$/, '') + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, '') + 'M'
  if (n >= 10_000) return (n / 1_000).toFixed(1).replace(/\.0$/, '') + 'K'
  return n.toLocaleString()
}

function formatUptime(secs: number): string {
  const days = Math.floor(secs / 86400)
  const hours = Math.floor((secs % 86400) / 3600)
  const mins = Math.floor((secs % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h ${mins}m`
  if (hours > 0) return `${hours}h ${mins}m`
  return `${mins}m`
}

function formatTime(ts: string): string {
  if (!ts) return '-'
  return new Date(ts).toLocaleString()
}

function formatTimeRelative(ts: string): string {
  if (!ts) return '-'
  const diff = Date.now() - new Date(ts).getTime()
  const secs = Math.floor(diff / 1000)
  if (secs < 60) return `${secs}s ago`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ${mins % 60}m ago`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h ago`
}

onMounted(() => {
  load()
  pollTimer = setInterval(load, 30000)
  window.addEventListener('resize', onResize)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  window.removeEventListener('resize', onResize)
})
</script>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 1.75rem;
}

.back-link {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-bottom: 0.5rem;
  transition: color 0.15s;
}

.back-link:hover {
  color: var(--primary-color);
}

.page-header h1 {
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.025em;
  margin-bottom: 0.5rem;
}

.page-meta {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  flex-wrap: wrap;
}

.meta-badge {
  font-size: 0.7rem;
  font-weight: 500;
  color: var(--text-color-secondary);
  background: var(--surface-overlay);
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
}

.meta-badge.role {
  background: var(--primary-50);
  color: var(--primary-color);
}

.header-right {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0.35rem;
}

.header-uptime,
.header-lastseen {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  font-weight: 500;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.75rem;
  font-weight: 600;
  padding: 0.35rem 0.75rem;
  border-radius: 20px;
}

.status-pill.online {
  background: var(--green-subtle);
  color: var(--green-400);
}

.status-pill.offline {
  background: var(--red-subtle);
  color: var(--red-400);
}

.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: currentColor;
}

.status-pill.online .status-dot {
  box-shadow: 0 0 6px rgba(51, 170, 51, 0.5);
}

/* ── Charts Section ── */
.charts-section {
  margin-bottom: 1.25rem;
}

.charts-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.75rem;
}

.toolbar-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--text-color-secondary);
}

.toolbar-label i {
  color: var(--primary-color);
}

.grafana-link {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  margin-left: 0.5rem;
  padding: 0.15rem 0.5rem;
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--primary-color);
  text-decoration: none;
  border: 1px solid var(--primary-color);
  border-radius: 4px;
  transition: background 0.15s, color 0.15s;
}

.grafana-link:hover {
  background: var(--primary-color);
  color: var(--primary-color-text, #fff);
}

.grafana-link i {
  font-size: 0.7rem;
}

.period-selector {
  display: flex;
  gap: 2px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 2px;
}

.period-btn {
  border: none;
  background: none;
  color: var(--text-color-muted);
  font-family: inherit;
  font-size: 0.7rem;
  font-weight: 500;
  padding: 0.3rem 0.6rem;
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s;
}

.period-btn:hover {
  color: var(--text-color-secondary);
}

.period-btn.active {
  background: var(--primary-50);
  color: var(--primary-color);
}

.charts-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 0.75rem;
}

/* ── Exelet Capacity ── */
.exelet-capacity-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1.25rem;
  align-items: stretch;
}

.exelet-capacity-left :deep(.chart-wrapper) {
  height: 100%;
  display: flex;
  flex-direction: column;
}

.exelet-capacity-left :deep(.chart-body) {
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.exelet-gauge-wrapper {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.exelet-gauge-header {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--surface-border);
}

.exelet-gauge-title {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-secondary);
}

.exelet-gauge-body {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 1.5rem;
  padding: 0.75rem 1rem;
}

.exelet-gauge {
  flex: 3;
  display: flex;
  align-items: center;
  justify-content: center;
}

.gauge-svg {
  width: 100%;
  max-width: 220px;
  height: auto;
}

.exelet-stat-cards {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 1px;
  background: var(--surface-border);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
}

.exelet-stat-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.2rem;
  padding: 0.6rem 1rem;
  background: var(--surface-card);
}

.exelet-stat-card:first-child {
  border-radius: 4px 4px 0 0;
}

.exelet-stat-card:last-child {
  border-radius: 0 0 4px 4px;
}

.exelet-stat-label {
  font-size: 0.65rem;
  font-weight: 500;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.03em;
}

.exelet-stat-value {
  font-size: 1.1rem;
  font-weight: 700;
  color: var(--text-color);
  font-family: 'JetBrains Mono', monospace;
}

/* ── Stat Tiles ── */
.stat-tiles {
  display: flex;
  flex-wrap: wrap;
  gap: 1px;
  background: var(--surface-border);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  margin-bottom: 1.25rem;
}

.stat-tile {
  flex: 1 1 0;
  min-width: 120px;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.75rem 1rem;
  background: var(--surface-card);
}

.stat-tile:first-child {
  border-radius: 4px 0 0 4px;
}

.stat-tile:last-child {
  border-radius: 0 4px 4px 0;
}

.stat-label {
  font-size: 0.7rem;
  font-weight: 500;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.03em;
}

.stat-value {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--text-color);
  font-family: 'JetBrains Mono', monospace;
}

/* ── Section Rows (side-by-side on desktop) ── */
.sections-row {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 1.25rem;
  margin-bottom: 1.25rem;
}

.sections-row-single {
  grid-template-columns: 1fr;
}

.sections-row-auto {
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
}

.sections-row > .section {
  border-top: none;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1.25rem;
  background: var(--surface-card);
  margin: 0;
}

.sections-row > .section .section-heading {
  margin-bottom: 1rem;
}

/* ── Stacked Sections ── */
.section {
  border-top: 1px solid var(--surface-border);
  padding: 1rem 0;
}

.section-heading {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--text-color-secondary);
  margin-bottom: 0.75rem;
}

.section-heading i {
  color: var(--primary-color);
  font-size: 1rem;
}

.section-inline {
  display: flex;
  gap: 1.5rem;
  margin-top: 0.5rem;
  font-size: 0.8rem;
}

.zfs-pools-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 1.25rem;
}

.zfs-pool {
  padding: 0.75rem 1rem;
  background: var(--surface-overlay);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
}

.zfs-pool-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.5rem;
}

.zfs-pool-name {
  font-weight: 600;
  font-size: 0.85rem;
}

.zfs-pool-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  margin-top: 0.5rem;
  font-size: 0.8rem;
}

.error-count-warn {
  color: var(--red-400) !important;
  font-weight: 700;
}

.zfs-warn-yellow {
  color: #eab308 !important;
  font-weight: 700;
}

.zfs-warn-red {
  color: var(--red-400) !important;
  font-weight: 700;
}

.zfs-arc-stats {
  margin-top: 0.75rem;
}

/* ── Health Badge ── */
.health-badge {
  font-size: 0.7rem;
  font-weight: 700;
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
  text-transform: uppercase;
}

.health-badge-green {
  background: var(--green-subtle);
  color: var(--green-400);
}

.health-badge-yellow {
  background: rgba(234, 179, 8, 0.15);
  color: #eab308;
}

.health-badge-red {
  background: var(--red-subtle);
  color: var(--red-400);
}

/* ── ARC Hit Rate Colors ── */
.arc-rate-green {
  color: var(--green-400);
}

.arc-rate-yellow {
  color: #eab308;
}

.arc-rate-red {
  color: var(--red-400);
}

/* ── Network Health ── */
.health-stats {
  gap: 1.25rem;
}

.health-stat {
  font-size: 0.8rem;
  font-weight: 500;
  color: var(--text-color-muted);
  font-family: 'JetBrains Mono', monospace;
}

.health-stat-warn {
  color: var(--red-400);
  font-weight: 700;
}

.conntrack-section {
  margin-top: 0.75rem;
}

.stat-muted {
  font-size: 0.7rem;
  color: var(--text-color-muted);
}

.title-badge {
  background: var(--primary-50);
  color: var(--primary-color);
  font-size: 0.7rem;
  font-weight: 700;
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
}

/* ── Components ── */
.component-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
}

.component-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
}

.component-name {
  font-weight: 600;
  font-size: 0.85rem;
}

.component-version {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  font-family: 'JetBrains Mono', monospace;
}

.component-status {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 0.2rem 0.6rem;
  border-radius: 4px;
  text-transform: capitalize;
}

.status-active {
  background: var(--green-subtle);
  color: var(--green-400);
}

.status-inactive {
  background: var(--red-subtle);
  color: var(--red-400);
}

.status-unknown {
  background: var(--surface-overlay);
  color: var(--text-color-muted);
}

/* ── Failed Services ── */
.badge-danger {
  background: var(--red-subtle);
  color: var(--red-400);
}

.failed-units-list {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
}

.failed-units-scroll {
  max-height: 240px;
  overflow-y: auto;
}

.failed-unit-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.75rem;
  background: var(--red-subtle);
  border: 1px solid rgba(239, 68, 68, 0.2);
  border-radius: 4px;
  font-size: 0.8rem;
  font-family: 'JetBrains Mono', monospace;
  color: var(--red-400);
}

.failed-unit-item i {
  font-size: 0.75rem;
}

.section-ok {
  font-size: 0.8rem;
  color: var(--text-color-muted);
}

/* ── Updates ── */
.updates-toggle {
  cursor: pointer;
  user-select: none;
}

.updates-toggle:hover {
  color: var(--text-color);
}

.toggle-icon {
  margin-left: auto;
  font-size: 0.75rem;
  color: var(--text-color-muted);
}

.updates-summary {
  font-size: 0.8rem;
  color: var(--text-color-muted);
}

.updates-list {
  list-style: none;
  font-size: 0.78rem;
  font-family: 'JetBrains Mono', monospace;
  color: var(--text-color-secondary);
}

.updates-list li {
  padding: 0.4rem 0;
  border-bottom: 1px solid var(--surface-border);
}

.updates-list li:last-child {
  border-bottom: none;
}

/* ── Loading / Error ── */
.loading-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  padding: 4rem 0;
  color: var(--text-color-muted);
  font-size: 0.9rem;
}

.message-banner {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.85rem 1rem;
  border-radius: 4px;
  font-size: 0.875rem;
}

.message-error {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid rgba(239, 68, 68, 0.2);
}

@media (max-width: 768px) {
  .charts-grid {
    grid-template-columns: 1fr;
  }
  .sections-row {
    grid-template-columns: 1fr;
  }
  .stat-tiles {
    flex-direction: column;
  }
  .stat-tile:first-child {
    border-radius: 4px 4px 0 0;
  }
  .stat-tile:last-child {
    border-radius: 0 0 4px 4px;
  }
  .component-row {
    flex-direction: column;
  }
}

@media (max-width: 991px) {
  .charts-grid {
    grid-template-columns: 1fr;
  }
  .exelet-capacity-grid {
    grid-template-columns: 1fr;
  }
  .sections-row {
    grid-template-columns: 1fr;
  }
  .zfs-pools-grid {
    grid-template-columns: 1fr;
  }
}
</style>

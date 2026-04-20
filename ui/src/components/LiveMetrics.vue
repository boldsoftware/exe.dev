<template>
  <div class="live-metrics">
    <div class="section-heading">Live Metrics<span class="updated-ago"> · refreshes every 5s</span></div>
    <div v-if="error" class="metrics-error">
      <span>{{ error }}</span>
    </div>
    <div v-else class="metrics-grid">
      <div class="metric-card">
        <div class="mt">CPU</div>
        <div class="mv green">{{ cpuDisplay }}</div>
        <div class="mb"><div class="mb-fill" :style="{ width: cpuPct + '%', background: 'var(--success-color, #22c55e)' }"></div></div>
        <div class="ms">{{ cpuSub }}</div>
      </div>
      <div class="metric-card">
        <div class="mt">Memory</div>
        <div class="mv blue">{{ memDisplay }}</div>
        <div class="ms">{{ memSub }}</div>
      </div>
      <div class="metric-card">
        <div class="mt">Disk</div>
        <div class="mv orange">{{ diskDisplay }}</div>
        <div class="ms">{{ diskSub }}</div>
      </div>
      <div class="metric-card">
        <div class="mt">Net ↓</div>
        <div class="mv blue">{{ netRxDisplay }}</div>
        <div class="ms">{{ netRxSub }}</div>
      </div>
      <div class="metric-card">
        <div class="mt">Net ↑</div>
        <div class="mv blue">{{ netTxDisplay }}</div>
        <div class="ms">{{ netTxSub }}</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { fetchVMLiveMetrics, type VMLiveMetrics } from '../api/client'

const props = defineProps<{
  vmName: string
  vmStatus: string
}>()

const POLL_INTERVAL = 5000

const metrics = ref<VMLiveMetrics | null>(null)
const prevMetrics = ref<VMLiveMetrics | null>(null)
const prevTime = ref<number>(0)
const error = ref('')
let timer: ReturnType<typeof setInterval> | null = null

// CPU
const cpuPct = computed(() => {
  if (!metrics.value) return 0
  // cpu_percent: 100 = 1 core. Cap bar at 100% of available.
  return Math.min(metrics.value.cpu_percent, 100)
})
const cpuDisplay = computed(() => {
  if (!metrics.value) return '—'
  return `${metrics.value.cpu_percent.toFixed(1)}%`
})
const cpuSub = computed(() => {
  if (!metrics.value) return ''
  if (metrics.value.cpus) {
    return `of ${metrics.value.cpus} vCPU${metrics.value.cpus > 1 ? 's' : ''}`
  }
  return 'of CPU capacity'
})

// Memory — show provisioned capacity (cgroup memory.current is not meaningful for VMs)
const memDisplay = computed(() => {
  if (!metrics.value) return '—'
  if (metrics.value.mem_capacity_bytes) {
    return formatBytesShort(metrics.value.mem_capacity_bytes)
  }
  return '—'
})
const memSub = computed(() => {
  if (!metrics.value) return ''
  return 'provisioned'
})

// Disk — show provisioned capacity (same approach as memory)
const diskDisplay = computed(() => {
  if (!metrics.value) return '—'
  if (metrics.value.disk_capacity_bytes) {
    return formatBytesShort(metrics.value.disk_capacity_bytes)
  }
  return '—'
})
const diskSub = computed(() => {
  if (!metrics.value) return ''
  return 'provisioned'
})

// Network rates (bytes/sec computed from cumulative counters)
const netRxRate = computed(() => {
  if (!metrics.value || !prevMetrics.value || !prevTime.value) return 0
  const elapsed = (Date.now() - prevTime.value) / 1000
  if (elapsed <= 0) return 0
  const delta = metrics.value.net_rx_bytes - prevMetrics.value.net_rx_bytes
  return delta / elapsed
})
const netTxRate = computed(() => {
  if (!metrics.value || !prevMetrics.value || !prevTime.value) return 0
  const elapsed = (Date.now() - prevTime.value) / 1000
  if (elapsed <= 0) return 0
  const delta = metrics.value.net_tx_bytes - prevMetrics.value.net_tx_bytes
  return delta / elapsed
})

const netRxDisplay = computed(() => {
  if (!metrics.value) return '—'
  if (!prevMetrics.value) return '—'
  return formatRate(netRxRate.value)
})
const netTxDisplay = computed(() => {
  if (!metrics.value) return '—'
  if (!prevMetrics.value) return '—'
  return formatRate(netTxRate.value)
})
const netRxSub = computed(() => {
  if (!metrics.value || !prevMetrics.value) return ''
  return 'receive rate'
})
const netTxSub = computed(() => {
  if (!metrics.value || !prevMetrics.value) return ''
  return 'send rate'
})

// Format bytes as short human string (1024-based, e.g. "2.3 GB")
function formatBytesShort(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function formatRate(bytesPerSec: number): string {
  if (bytesPerSec < 0) return '0 B/s'
  // Convert to bits for Mbps display
  const bitsPerSec = bytesPerSec * 8
  if (bitsPerSec >= 1_000_000) {
    return `${(bitsPerSec / 1_000_000).toFixed(1)} Mbps`
  }
  if (bitsPerSec >= 1_000) {
    return `${(bitsPerSec / 1_000).toFixed(1)} Kbps`
  }
  return `${Math.round(bitsPerSec)} bps`
}

async function poll() {
  try {
    const data = await fetchVMLiveMetrics(props.vmName)
    if (metrics.value) {
      prevMetrics.value = metrics.value
      prevTime.value = Date.now()
    }
    metrics.value = data
    error.value = ''
  } catch (e: any) {
    if (!metrics.value) {
      error.value = 'Unable to load metrics'
    }
  }
}

function startPolling() {
  stopPolling()
  if (props.vmStatus === 'running') {
    poll()
    timer = setInterval(poll, POLL_INTERVAL)
  }
}

function stopPolling() {
  if (timer) { clearInterval(timer); timer = null }
}

watch(() => props.vmStatus, (newStatus) => {
  if (newStatus === 'running') {
    startPolling()
  } else {
    stopPolling()
    metrics.value = null
    prevMetrics.value = null
    error.value = ''
  }
})

watch(() => props.vmName, () => {
  metrics.value = null
  prevMetrics.value = null
  prevTime.value = 0
  error.value = ''
  startPolling()
})

onMounted(() => {
  startPolling()
})

onBeforeUnmount(() => {
  stopPolling()
})
</script>

<style scoped>
.live-metrics {
  display: flex;
  flex-direction: column;
}

.section-heading {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: var(--text-color-muted);
  text-transform: uppercase;
  margin-bottom: 10px;
}

.updated-ago {
  font-weight: 400;
}

.metrics-error {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 4px 0;
}

.metrics-grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 12px;
}

.metric-card {
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 14px 16px;
  background: var(--surface-card);
}

.mt {
  font-size: 10px;
  font-weight: 600;
  color: var(--text-color-muted);
  letter-spacing: 0.05em;
  text-transform: uppercase;
  margin-bottom: 6px;
}

.mv {
  font-size: 20px;
  font-weight: 700;
  margin-bottom: 6px;
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
}

.mv.green { color: var(--success-color, #22c55e); }
.mv.blue { color: #2563eb; }
.mv.orange { color: #ea580c; }

.mb {
  height: 3px;
  background: var(--surface-border);
  border-radius: 2px;
  margin-bottom: 6px;
}

.mb-fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.5s ease;
}

.ms {
  font-size: 10px;
  color: var(--text-color-muted);
}

@media (max-width: 768px) {
  .metrics-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}

@media (max-width: 480px) {
  .metrics-grid {
    grid-template-columns: repeat(2, 1fr);
  }
  /* Let the 5th card span full width so it doesn't orphan */
  .metric-card:last-child {
    grid-column: 1 / -1;
  }
}
</style>

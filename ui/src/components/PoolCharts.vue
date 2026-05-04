<template>
  <div class="pool-charts-section">
    <div v-if="!hideHeading" class="section-heading">Resource Pool</div>

    <div v-if="loading" class="pool-charts-loading">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="points.length === 0" class="pool-charts-empty">No pool data available.</div>

    <div v-else class="chart-grid">
      <div class="chart-card">
        <div class="chart-label">CPU Usage</div>
        <div class="chart-wrap">
          <canvas ref="cpuCanvas"></canvas>
        </div>
        <div v-if="legendItems.length > 0" class="chart-legend">
          <span v-for="item in legendItems" :key="item.label" class="legend-item">
            <span class="legend-swatch" :style="{ background: item.color }"></span>
            {{ item.label }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, computed, nextTick } from 'vue'
import { Chart, registerables } from 'chart.js'
import {
  fetchPoolHistory,
  fetchVMsPool,
  type PoolPoint,
  type VMPoolPoint,
  type VMsPoolResponse,
} from '../api/client'

Chart.register(...registerables)

const props = defineProps<{
  hours: number
  highlightVM?: string
  hideHeading?: boolean
}>()

const loading = ref(true)
const points = ref<PoolPoint[]>([])
const vmBreakdown = ref<Record<string, VMPoolPoint[]>>({})
const pool = ref<VMsPoolResponse | null>(null)

const cpuCanvas = ref<HTMLCanvasElement | null>(null)
let cpuChart: Chart | null = null

const cpuLimit = computed(() => pool.value?.cpu_max ?? 0)

interface LegendItem {
  label: string
  color: string
}
const legendItems = ref<LegendItem[]>([])

// Match CoolS.vue hash so chart colors are consistent with VM list logos.
function hashString(s: string): number {
  let hash = 0
  for (const c of s) {
    hash = ((hash << 5) - hash) + c.charCodeAt(0)
    hash = hash >>> 0
  }
  return hash
}

function vmColor(name: string): string {
  const key = `${name}.exe.xyz:9999`
  const h = hashString(key) % 360
  return `hsl(${h}, 70%, 55%)`
}

function vmColorAlpha(name: string, alpha: number): string {
  const key = `${name}.exe.xyz:9999`
  const h = hashString(key) % 360
  return `hsla(${h}, 70%, 55%, ${alpha})`
}

const OTHER_COLOR = '#999'

function fmtTime(ts: string): string {
  const d = new Date(ts)
  if (props.hours <= 24) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function getChartColors() {
  const style = getComputedStyle(document.documentElement)
  return {
    muted: style.getPropertyValue('--text-color-muted').trim() || '#717171',
    border: style.getPropertyValue('--surface-border').trim() || '#e0e0e0',
  }
}

function renderCharts() {
  cpuChart?.destroy()
  cpuChart = null
  legendItems.value = []

  if (points.value.length === 0) return
  if (!cpuCanvas.value || cpuLimit.value <= 0) return

  const colors = getChartColors()
  const labels = points.value.map((p) => fmtTime(p.timestamp))
  const vms = vmBreakdown.value

  // Build per-VM CPU arrays.
  const vmCPU: Record<string, number[]> = {}
  for (const [vm, pts] of Object.entries(vms)) {
    vmCPU[vm] = pts.map((p) => p.cpu_cores)
  }

  // Rank VMs by total CPU usage, pick top 10.
  const vmTotals = Object.entries(vmCPU).map(([name, vals]) => ({
    name,
    total: vals.reduce((a, b) => a + b, 0),
  }))
  vmTotals.sort((a, b) => b.total - a.total)

  const MAX_VMS = 10
  const topVMs = vmTotals.slice(0, MAX_VMS).map((v) => v.name)
  const otherVMs = vmTotals.slice(MAX_VMS).map((v) => v.name)

  // Compute "Other" series by summing remaining VMs.
  let otherData: number[] | null = null
  if (otherVMs.length > 0) {
    const len = labels.length
    otherData = new Array(len).fill(0)
    for (const vm of otherVMs) {
      const vals = vmCPU[vm] ?? []
      for (let i = 0; i < len; i++) {
        otherData[i] += vals[i] ?? 0
      }
    }
  }

  // Build stacked datasets (bottom to top: Other first, then top VMs in reverse rank).
  const datasets: any[] = []
  const legend: LegendItem[] = []

  if (otherData) {
    datasets.push({
      label: `Other (${otherVMs.length})`,
      data: otherData,
      borderColor: OTHER_COLOR,
      backgroundColor: OTHER_COLOR + '40',
      borderWidth: 1,
      fill: true,
      pointRadius: 0,
      pointHitRadius: 8,
      tension: 0.3,
      order: topVMs.length + 1,
    })
    legend.push({ label: `Other (${otherVMs.length})`, color: OTHER_COLOR })
  }

  // Add top VMs in reverse order so highest-usage VM is on top of the stack.
  for (let i = topVMs.length - 1; i >= 0; i--) {
    const vm = topVMs[i]
    const color = vmColor(vm)
    const bgColor = vmColorAlpha(vm, 0.5)
    datasets.push({
      label: vm,
      data: vmCPU[vm] ?? [],
      borderColor: color,
      backgroundColor: bgColor,
      borderWidth: 1,
      fill: true,
      pointRadius: 0,
      pointHitRadius: 8,
      tension: 0.3,
      order: i,
    })
  }
  // Legend in rank order (top usage first).
  for (const vm of topVMs) {
    legend.push({ label: vm, color: vmColor(vm) })
  }

  // Pool limit line (always on top).
  datasets.push({
    label: 'Pool size',
    data: new Array(labels.length).fill(cpuLimit.value),
    borderColor: '#cf222e',
    borderWidth: 1.5,
    borderDash: [6, 4],
    fill: false,
    pointRadius: 0,
    pointHitRadius: 0,
    order: -1,
  })

  const yMax = Math.max(cpuLimit.value * 1.15, ...points.value.map((p) => p.cpu_cores.sum)) || 1

  cpuChart = new Chart(cpuCanvas.value, {
    type: 'line',
    data: { labels, datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: '#fff',
          borderColor: colors.border,
          borderWidth: 1,
          titleColor: colors.muted,
          titleFont: {
            family: "'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace",
            size: 11,
          },
          bodyFont: {
            family: "'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace",
            size: 11,
          },
          padding: 8,
          boxWidth: 8,
          boxHeight: 8,
          usePointStyle: true,
          filter: (item) => {
            // Hide zero-value items in tooltip.
            return (item.parsed?.y ?? 0) > 0.01
          },
          callbacks: {
            label: (ctx) => {
              const v = ctx.parsed.y ?? 0
              return `${ctx.dataset.label}: ${v.toFixed(1)} vCPUs`
            },
            labelColor: (ctx) => {
              const c = (ctx.dataset.borderColor as string) || colors.muted
              return { borderColor: c, backgroundColor: c }
            },
            labelTextColor: (ctx) => {
              return (ctx.dataset.borderColor as string) || colors.muted
            },
          },
        },
      },
      scales: {
        x: {
          grid: { color: colors.border },
          ticks: { color: colors.muted, font: { size: 10 }, maxTicksLimit: 6 },
        },
        y: {
          stacked: true,
          min: 0,
          max: yMax,
          grid: { color: colors.border },
          ticks: {
            color: colors.muted,
            font: { size: 10 },
            callback: (v) => (v as number).toFixed(1),
          },
          title: { display: false },
        },
      },
    },
  })

  legendItems.value = legend
}

async function loadData() {
  loading.value = true
  try {
    const [poolRes, historyRes] = await Promise.all([
      pool.value ? Promise.resolve(pool.value) : fetchVMsPool(),
      fetchPoolHistory(props.hours),
    ])
    pool.value = poolRes
    points.value = historyRes.points ?? []
    vmBreakdown.value = historyRes.vms ?? {}
  } catch {
    points.value = []
    vmBreakdown.value = {}
  } finally {
    loading.value = false
  }
  await nextTick()
  renderCharts()
}

watch(() => props.hours, loadData)

onMounted(loadData)

onBeforeUnmount(() => {
  cpuChart?.destroy()
})
</script>

<style scoped>
.pool-charts-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.section-heading {
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-color-secondary);
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.chart-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
}

.chart-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 14px 16px;
}

.chart-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-color-muted);
}

.chart-wrap {
  position: relative;
  height: 160px;
}

.chart-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 14px;
  font-size: 11px;
  color: var(--text-color-secondary);
}

.legend-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.legend-swatch {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 2px;
  flex-shrink: 0;
}

.pool-charts-loading {
  text-align: center;
  padding: 24px;
  color: var(--text-color-secondary);
  font-size: 13px;
}

.pool-charts-empty {
  text-align: center;
  padding: 24px;
  color: var(--text-color-muted);
  font-size: 13px;
}
</style>

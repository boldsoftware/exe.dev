<template>
  <div class="pool-charts-section">
    <div v-if="!hideHeading" class="section-heading">Resource Pool</div>

    <div v-if="loading" class="pool-charts-loading">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="points.length === 0" class="pool-charts-empty">No pool data available.</div>

    <div v-else class="chart-grid">
      <div class="chart-card">
        <div class="chart-label">CPU Usage <span class="chart-current">{{ cpuCurrent }}</span></div>
        <div class="chart-wrap">
          <canvas ref="cpuCanvas"></canvas>
        </div>
      </div>
      <div v-if="memLimit > 0" class="chart-card">
        <div class="chart-label">Memory Allocated <span class="chart-current">{{ memCurrent }}</span></div>
        <div class="chart-wrap">
          <canvas ref="memCanvas"></canvas>
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
const memCanvas = ref<HTMLCanvasElement | null>(null)
let cpuChart: Chart | null = null
let memChart: Chart | null = null

const cpuLimit = computed(() => pool.value?.cpu_max ?? 0)
const memLimit = computed(() => pool.value?.mem_max_bytes ?? 0)

const cpuCurrent = computed(() => {
  if (points.value.length === 0) return ''
  const last = points.value[points.value.length - 1]
  return `${last.cpu_cores.sum.toFixed(1)} / ${cpuLimit.value} vCPUs`
})

const memCurrent = computed(() => {
  if (points.value.length === 0 || memLimit.value === 0) return ''
  const last = points.value[points.value.length - 1]
  return `${fmtGiB(last.mem_bytes.sum)} / ${fmtGiB(memLimit.value)}`
})

function fmtGiB(bytes: number): string {
  const gib = bytes / (1024 * 1024 * 1024)
  if (gib >= 1) return gib.toFixed(1) + ' GiB'
  const mib = bytes / (1024 * 1024)
  return mib.toFixed(0) + ' MiB'
}

function fmtTime(ts: string): string {
  const d = new Date(ts)
  if (props.hours <= 24) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function getChartColors() {
  const style = getComputedStyle(document.documentElement)
  return {
    text: style.getPropertyValue('--text-color').trim() || '#1a1a1a',
    muted: style.getPropertyValue('--text-color-muted').trim() || '#717171',
    border: style.getPropertyValue('--surface-border').trim() || '#e0e0e0',
    primary: '#0969da',
  }
}

function buildChart(
  canvas: HTMLCanvasElement,
  labels: string[],
  totalData: number[],
  limit: number,
  formatY: (v: number) => string,
  tooltipSuffix: string,
  vmData: Record<string, number[]>,
  highlightVM?: string,
): Chart {
  const colors = getChartColors()
  const yMax = Math.max(limit * 1.15, ...totalData) || 1
  const hasVMs = Object.keys(vmData).length > 0
  const showPerVM = hasVMs && highlightVM

  const datasets: any[] = []

  if (showPerVM) {
    // Per-VM lines: faded for all, bold for highlighted.
    const vmNames = Object.keys(vmData).sort()
    vmNames.forEach((vm) => {
      const isHighlighted = vm === highlightVM
      datasets.push({
        label: vm,
        data: vmData[vm],
        borderColor: isHighlighted ? colors.primary : colors.muted,
        backgroundColor: isHighlighted ? colors.primary + '30' : 'transparent',
        borderWidth: isHighlighted ? 2 : 1,
        fill: isHighlighted,
        pointRadius: 0,
        pointHitRadius: 8,
        tension: 0.3,
        borderDash: isHighlighted ? [] : [3, 3],
        order: isHighlighted ? 0 : 1,
      })
    })
  } else {
    // Aggregate total line.
    datasets.push({
      label: 'Total',
      data: totalData,
      borderColor: colors.primary,
      backgroundColor: colors.primary + '30',
      borderWidth: 1.5,
      fill: true,
      pointRadius: 0,
      pointHitRadius: 8,
      tension: 0.3,
    })
  }

  // Pool size line.
  datasets.push({
    label: 'Pool size',
    data: new Array(labels.length).fill(limit),
    borderColor: '#cf222e',
    borderWidth: 1.5,
    borderDash: [6, 4],
    fill: false,
    pointRadius: 0,
    pointHitRadius: 0,
  })

  return new Chart(canvas, {
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
            family:
              "'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace",
            size: 11,
          },
          bodyFont: {
            family:
              "'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace",
            size: 11,
          },
          padding: 8,
          boxWidth: 8,
          boxHeight: 8,
          usePointStyle: true,
          callbacks: {
            label: (ctx) => {
              const base = formatY(ctx.parsed.y ?? 0)
              return `${ctx.dataset.label}: ${base}${tooltipSuffix}`
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
          min: 0,
          max: yMax,
          grid: { color: colors.border },
          ticks: {
            color: colors.muted,
            font: { size: 10 },
            callback: (v) => formatY(v as number),
          },
          title: { display: false },
        },
      },
    },
  })
}

function renderCharts() {
  cpuChart?.destroy()
  memChart?.destroy()
  cpuChart = null
  memChart = null

  if (points.value.length === 0) return

  const labels = points.value.map((p) => fmtTime(p.timestamp))
  const vms = vmBreakdown.value

  // Build per-VM data arrays keyed by metric.
  const vmCPU: Record<string, number[]> = {}
  const vmMem: Record<string, number[]> = {}
  for (const [vm, pts] of Object.entries(vms)) {
    vmCPU[vm] = pts.map((p) => p.cpu_cores)
    vmMem[vm] = pts.map((p) => p.mem_bytes)
  }

  if (cpuCanvas.value && cpuLimit.value > 0) {
    cpuChart = buildChart(
      cpuCanvas.value,
      labels,
      points.value.map((p) => p.cpu_cores.sum),
      cpuLimit.value,
      (v) => v.toFixed(1),
      ' vCPUs',
      vmCPU,
      props.highlightVM,
    )
  }

  if (memCanvas.value && memLimit.value > 0) {
    memChart = buildChart(
      memCanvas.value,
      labels,
      points.value.map((p) => p.mem_bytes.sum),
      memLimit.value,
      (v) => fmtGiB(v),
      ' allocated',
      vmMem,
      props.highlightVM,
    )
  }
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
  memChart?.destroy()
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
  grid-template-columns: 1fr 1fr;
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
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.chart-current {
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color);
  text-transform: none;
  letter-spacing: 0;
}

.chart-wrap {
  position: relative;
  height: 120px;
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

@media (max-width: 640px) {
  .chart-grid {
    grid-template-columns: 1fr;
  }
}
</style>

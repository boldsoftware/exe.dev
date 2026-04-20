<template>
  <div class="usage-chart">
    <div class="section-heading">
      COMPUTE USAGE HISTORY
    </div>

    <!-- Tab and Time Range Selector -->
    <div class="chart-controls">
      <div class="metric-tabs">
        <button
          v-for="metric in metrics"
          :key="metric"
          class="metric-tab"
          :class="{ active: selectedMetric === metric }"
          @click="selectedMetric = metric"
        >
          {{ metric }}
        </button>
      </div>
      <div class="time-range-tabs">
        <button
          v-for="range in timeRanges"
          :key="range.hours"
          class="time-tab"
          :class="{ active: selectedHours === range.hours }"
          @click="selectTimeRange(range.hours)"
        >
          {{ range.label }}
        </button>
      </div>
    </div>

    <!-- Error State -->
    <div v-if="error" class="chart-error">
      <span>{{ error }}</span>
    </div>

    <!-- Loading State -->
    <div v-else-if="loading" class="chart-loading">
      <span class="spinner"></span>
    </div>

    <!-- Empty State -->
    <div v-else-if="!rawData || rawData.length === 0" class="chart-empty">
      No data available for this time range.
    </div>

    <!-- Chart -->
    <div v-else class="chart-container">
      <Chart :key="selectedMetric + '-' + selectedHours" type="line" :data="chartJsData" :options="chartJsOptions" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import Chart from 'primevue/chart'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Filler,
  Tooltip,
  type ChartData,
  type ChartOptions,
  type TooltipItem,
} from 'chart.js'
import { fetchVMComputeUsage, type VMComputeUsagePoint } from '../api/client'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Filler, Tooltip)

const props = defineProps<{
  vmName: string
  vmStatus: string
}>()

const metrics = ['CPU', 'Memory', 'Disk', 'Network'] as const
type Metric = (typeof metrics)[number]

const timeRanges = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 168 },
  { label: '30d', hours: 720 },
] as const

const selectedMetric = ref<Metric>('CPU')
const selectedHours = ref(24)
const loading = ref(false)
const error = ref('')
const rawData = ref<VMComputeUsagePoint[]>([])


const selectTimeRange = (hours: number) => {
  selectedHours.value = hours
  loadData()
}

const loadData = async () => {
  loading.value = true
  error.value = ''
  try {
    const data = await fetchVMComputeUsage(props.vmName, selectedHours.value)
    rawData.value = data
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load compute usage'
  } finally {
    loading.value = false
  }
}

const formatBytes = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1)
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`
}

const formatAxisTime = (date: Date): string => {
  const opts: Intl.DateTimeFormatOptions = { timeZone: 'UTC' }
  if (selectedHours.value <= 24) {
    Object.assign(opts, { hour: 'numeric', minute: '2-digit' })
  } else if (selectedHours.value <= 168) {
    Object.assign(opts, { month: 'short', day: 'numeric', hour: 'numeric' })
  } else {
    Object.assign(opts, { month: 'short', day: 'numeric' })
  }
  return date.toLocaleString('en-US', opts)
}

// Labels for x-axis
const labels = computed(() => {
  if (!rawData.value.length) return []
  return rawData.value.map((p) => formatAxisTime(new Date(p.timestamp)))
})

// Build Chart.js dataset config per metric
const chartJsData = computed<ChartData<'line'>>(() => {
  const data = rawData.value
  if (!data.length) return { labels: [], datasets: [] }

  switch (selectedMetric.value) {
    case 'CPU':
      return {
        labels: labels.value,
        datasets: [
          {
            label: 'CPU',
            data: data.map((p) => p.cpu_percent),
            borderColor: '#22c55e',
            borderWidth: 2,
            fill: false,
            tension: 0.3,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
        ],
      }
    case 'Memory':
      return {
        labels: labels.value,
        datasets: [
          {
            label: 'Memory',
            data: data.map((p) => p.memory_bytes),
            borderColor: '#2563eb',
            backgroundColor: 'rgba(37, 99, 235, 0.15)',
            borderWidth: 2,
            fill: true,
            tension: 0.3,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
        ],
      }
    case 'Disk':
      return {
        labels: labels.value,
        datasets: [
          {
            label: 'Used',
            data: data.map((p) => p.disk_used_bytes),
            borderColor: '#ea580c',
            backgroundColor: 'rgba(234, 88, 12, 0.15)',
            borderWidth: 2,
            fill: true,
            tension: 0.3,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
          {
            label: 'Capacity',
            data: data.map((p) => p.disk_capacity_bytes),
            borderColor: '#888',
            borderWidth: 1,
            borderDash: [5, 5],
            fill: false,
            tension: 0,
            pointRadius: 0,
            pointHoverRadius: 0,
          },
        ],
      }
    case 'Network':
      return {
        labels: labels.value,
        datasets: [
          {
            label: 'rx',
            data: data.map((p) => p.net_rx_bytes_per_sec),
            borderColor: '#22c55e',
            borderWidth: 2,
            fill: false,
            tension: 0.3,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
          {
            label: 'tx',
            data: data.map((p) => p.net_tx_bytes_per_sec),
            borderColor: '#2563eb',
            borderWidth: 2,
            fill: false,
            tension: 0.3,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
        ],
      }
  }
})

// Chart.js options — adapts y-axis formatting per metric
const chartJsOptions = computed<ChartOptions<'line'>>(() => {
  const metric = selectedMetric.value
  const isDark = window.matchMedia('(prefers-color-scheme: dark)').matches

  const yTickCallback = (value: number | string) => {
    const v = typeof value === 'string' ? parseFloat(value) : value
    switch (metric) {
      case 'CPU':
        return `${v.toFixed(0)}%`
      case 'Memory':
      case 'Disk':
        return formatBytes(v)
      case 'Network':
        return `${formatBytes(v)}/s`
    }
  }

  const tooltipLabel = (ctx: TooltipItem<'line'>) => {
    const v = ctx.parsed.y ?? 0
    switch (metric) {
      case 'CPU':
        return `${ctx.dataset.label}: ${v.toFixed(1)}%`
      case 'Memory':
        return `${ctx.dataset.label}: ${formatBytes(v)}`
      case 'Disk':
        return `${ctx.dataset.label}: ${formatBytes(v)}`
      case 'Network':
        return `${ctx.dataset.label}: ${formatBytes(v)}/s`
    }
  }

  const tooltipTitle = (items: TooltipItem<'line'>[]) => {
    if (!items.length) return ''
    const idx = items[0].dataIndex
    const point = rawData.value[idx]
    if (!point) return items[0].label
    const d = new Date(point.timestamp)
    return (
      d.toLocaleString('en-US', {
        timeZone: 'UTC',
        month: 'short',
        day: 'numeric',
        hour: 'numeric',
        minute: '2-digit',
      }) + ' UTC'
    )
  }

  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: {
      mode: 'index',
      intersect: false,
    },
    plugins: {
      legend: {
        display: metric === 'Network' || metric === 'Disk',
        position: 'bottom',
        labels: {
          usePointStyle: true,
          boxWidth: 8,
          padding: 12,
          font: { size: 11 },
        },
      },
      tooltip: {
        backgroundColor: isDark ? '#1e1e1e' : '#ffffff',
        titleColor: isDark ? '#e0e0e0' : '#1e1e1e',
        bodyColor: isDark ? '#a0a0a0' : '#555555',
        borderColor: isDark ? '#333333' : '#e0e0e0',
        borderWidth: 1,
        cornerRadius: 6,
        padding: 10,
        titleFont: { size: 12, weight: 'bold' as const },
        bodyFont: { size: 11 },
        displayColors: false,
        callbacks: {
          title: tooltipTitle,
          label: tooltipLabel,
        },
      },
    },
    scales: {
      x: {
        ticks: {
          maxTicksLimit: 6,
          maxRotation: 0,
          font: { size: 11 },
        },
        grid: {
          display: false,
        },
      },
      y: {
        beginAtZero: true,
        max: metric === 'CPU' ? 100 : undefined,
        ticks: {
          callback: yTickCallback,
          maxTicksLimit: 6,
          font: { size: 11 },
        },
        grid: {
          color: 'rgba(128, 128, 128, 0.15)',
        },
      },
    },
  }
})

// Lifecycle
onMounted(() => {
  loadData()
})
</script>

<style scoped>
.usage-chart {
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


.chart-controls {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  flex-wrap: wrap;
  gap: 8px;
}

.metric-tabs,
.time-range-tabs {
  display: flex;
  gap: 0;
}

.metric-tab,
.time-tab {
  padding: 5px 12px;
  border: 1px solid var(--surface-border);
  background: var(--surface-card);
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 11px;
  font-weight: 500;
  font-family: inherit;
  transition: all 0.15s;
}

.metric-tab:first-child,
.time-tab:first-child {
  border-radius: 4px 0 0 4px;
}

.metric-tab:last-child,
.time-tab:last-child {
  border-radius: 0 4px 4px 0;
}

.metric-tab:not(:first-child),
.time-tab:not(:first-child) {
  border-left: none;
}

.metric-tab:hover,
.time-tab:hover {
  background: var(--surface-inset, var(--surface-ground));
  color: var(--text-color);
}

.metric-tab.active,
.time-tab.active {
  background: var(--text-color);
  color: var(--surface-ground);
  border-color: var(--text-color);
}

.metric-tab.active + .metric-tab,
.time-tab.active + .time-tab {
  border-left: none;
}

.chart-error {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 24px 0;
  text-align: center;
}

.chart-loading {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: 48px 0;
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid var(--surface-border);
  border-top-color: var(--text-color-muted);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.chart-empty {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 48px 0;
  text-align: center;
}

.chart-container {
  position: relative;
  height: 260px;
}

.chart-container :deep(> div) {
  height: 100%;
}

@media (max-width: 640px) {
  .chart-controls {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>

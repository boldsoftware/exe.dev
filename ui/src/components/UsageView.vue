<template>
  <div class="usage-view">
    <div class="beta-banner">
      <span class="beta-banner-tag">Beta</span>
      Metrics update periodically and may have discrepancies. For real-time data, use <code>free</code>, <code>df -h</code>, or <code>top</code> on the VM.
    </div>

    <!-- Pool Charts -->
    <PoolCharts v-if="pool && pool.cpu_max > 0" :hours="props.hours" />

    <!-- Loading -->
    <div v-if="historyLoading" class="usage-loading">
      <i class="pi pi-spin pi-spinner"></i> Loading usage data...
    </div>

    <!-- Usage Table -->
    <template v-else-if="filteredRows.length > 0">
    <div class="table-heading">VMs <span class="table-heading-range">{{ rangeLabel }}</span></div>
    <div class="boxes-list">
      <div class="usage-header">
        <button class="col-btn" @click="toggleSort('name')">VM <i :class="sortIcon('name')" class="sort-icon"></i></button>
        <button class="col-btn col-right" @click="toggleSort('cpu')">CPU <i :class="sortIcon('cpu')" class="sort-icon"></i></button>
        <button class="col-btn col-right" @click="toggleSort('mem')">Used <i :class="sortIcon('mem')" class="sort-icon"></i></button>
        <button class="col-btn col-right" @click="toggleSort('disk')">Disk <i :class="sortIcon('disk')" class="sort-icon"></i></button>
        <button class="col-btn col-right" @click="toggleSort('io')">IO <i :class="sortIcon('io')" class="sort-icon"></i></button>
      </div>
      <div v-for="row in filteredRows" :key="row.name" class="box-row" @click="$router.push(`/vm/${row.name}`)">
        <div class="vm-cell">
          <StatusDot :status="row.status" />
          <router-link :to="`/vm/${row.name}`" class="vm-name" @click.stop>{{ row.name }}</router-link>
        </div>
        <div class="metric-cell">
          <svg class="spark-svg" width="64" height="18" viewBox="0 0 64 18">
            <polyline :points="row.cpuSpark" style="stroke: #ff7f0e" />
          </svg>
          <span class="metric-value">{{ row.cpuLabel }}</span>
        </div>
        <div class="metric-cell">
          <svg class="spark-svg" width="64" height="18" viewBox="0 0 64 18">
            <polyline :points="row.memSpark" style="stroke: #1f77b4" />
          </svg>
          <span class="metric-value">{{ row.memLabel }}</span>
        </div>
        <div class="metric-cell">
          <svg class="spark-svg" width="64" height="18" viewBox="0 0 64 18">
            <polyline :points="row.diskSpark" style="stroke: #9467bd" />
          </svg>
          <span class="metric-value">{{ row.diskLabel }}</span>
        </div>
        <div class="metric-cell">
          <svg class="spark-svg" width="64" height="18" viewBox="0 0 64 18">
            <polyline :points="row.ioSpark" style="stroke: #17becf" />
          </svg>
          <span class="metric-value">{{ row.ioLabel }}</span>
        </div>
      </div>
    </div>
    </template>

    <!-- No data -->
    <div v-else-if="!historyLoading" class="usage-empty">
      No usage data available.
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import {
  fetchUsageHistory,
  fetchVMsPool,
  type UsageDataPoint,
  type UsageHistoryResponse,
  type VMsPoolResponse,
  type BoxInfo,
} from '../api/client'
import StatusDot from './StatusDot.vue'
import PoolCharts from './PoolCharts.vue'

const props = defineProps<{
  boxes: BoxInfo[]
  filter: string
  hours: number
}>()

const pool = ref<VMsPoolResponse | null>(null)
const history = ref<UsageHistoryResponse>({})
const historyLoading = ref(false)
const ranges = [
  { hours: 24, label: '24h' },
  { hours: 168, label: '7d' },
  { hours: 720, label: '30d' },
]

const rangeLabel = computed(() => ranges.find((r) => r.hours === props.hours)?.label ?? '')

// Sorting.
type SortCol = 'name' | 'cpu' | 'mem' | 'disk' | 'io'
const sortCol = ref<SortCol>('name')
const sortAsc = ref(true)

function toggleSort(col: SortCol) {
  if (sortCol.value === col) {
    sortAsc.value = !sortAsc.value
  } else {
    sortCol.value = col
    // Default descending for metrics, ascending for name.
    sortAsc.value = col === 'name'
  }
}

function sortIcon(col: SortCol): string {
  if (sortCol.value !== col) return 'pi pi-sort-alt'
  return sortAsc.value ? 'pi pi-sort-amount-up-alt' : 'pi pi-sort-amount-down'
}

async function loadData() {
  historyLoading.value = true
  try {
    const [poolRes, historyRes] = await Promise.all([
      pool.value ? Promise.resolve(pool.value) : fetchVMsPool(),
      fetchUsageHistory(props.hours),
    ])
    pool.value = poolRes
    history.value = historyRes
  } catch {
    // Best-effort.
  } finally {
    historyLoading.value = false
  }
}

onMounted(loadData)
watch(() => props.hours, async () => {
  historyLoading.value = true
  try {
    history.value = await fetchUsageHistory(props.hours)
  } catch {
    /* ignore */
  } finally {
    historyLoading.value = false
  }
})

// Build a status lookup from boxes prop.
const boxStatusMap = computed(() => {
  const m = new Map<string, string>()
  for (const b of props.boxes) m.set(b.name, b.status)
  return m
})

// Per-VM row data with raw sort values.
interface UsageRow {
  name: string
  status: string
  cpuSpark: string
  cpuLabel: string
  cpuSort: number
  memSpark: string
  memLabel: string
  memSort: number
  diskSpark: string
  diskLabel: string
  diskSort: number
  ioSpark: string
  ioLabel: string
  ioSort: number
}

function makeSpark(values: number[], scaleMax?: number): string {
  const w = 64,
    h = 18
  if (values.length === 0) return ''
  const max = scaleMax ?? Math.max(...values, 0.001)
  return values
    .map((v, i) => {
      const x = values.length === 1 ? w / 2 : (i / (values.length - 1)) * w
      const y = h - (Math.max(v, 0) / max) * (h - 2) - 1
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

function fmtPct(cores: number, nominal: number): string {
  if (nominal <= 0) return '0%'
  return Math.round((cores / nominal) * 100) + '%'
}

function fmtMem(gb: number): string {
  if (gb >= 1) return gb.toFixed(1) + ' GB'
  const mb = gb * 1024
  return mb < 1 ? '0 MB' : mb.toFixed(0) + ' MB'
}

function fmtGB(gb: number): string {
  return gb.toFixed(1) + ' GB'
}

function fmtIO(mbps: number): string {
  if (mbps < 0.1) return '0'
  return mbps.toFixed(1) + ' MB/s'
}

function lastValue(points: UsageDataPoint[], field: keyof UsageDataPoint): number {
  if (points.length === 0) return 0
  return points[points.length - 1][field] as number
}

const rows = computed<UsageRow[]>(() => {
  const result: UsageRow[] = []

  const allNames = new Set<string>()
  for (const b of props.boxes) allNames.add(b.name)
  for (const name of Object.keys(history.value)) allNames.add(name)

  for (const name of allNames) {
    const points = history.value[name] ?? []
    const status = boxStatusMap.value.get(name) ?? 'unknown'
    const cpuNominal = points.length > 0 ? lastValue(points, 'cpu_nominal') : 1

    const cpuValues = points.map((p) => (cpuNominal > 0 ? p.cpu_cores / cpuNominal : 0))
    const memValues = points.map((p) => p.memory_used_gb)
    const diskValues = points.map((p) => p.disk_used_gb)
    const ioValues = points.map((p) => p.io_read_mbps + p.io_write_mbps)

    const cpuLast = points.length > 0 ? lastValue(points, 'cpu_cores') : 0
    const memLast = points.length > 0 ? lastValue(points, 'memory_used_gb') : 0
    const diskLast = points.length > 0 ? lastValue(points, 'disk_used_gb') : 0
    const ioLast = points.length > 0 ? lastValue(points, 'io_read_mbps') + lastValue(points, 'io_write_mbps') : 0

    result.push({
      name,
      status,
      cpuSpark: makeSpark(cpuValues, 1),
      cpuLabel: points.length > 0 ? fmtPct(cpuLast, cpuNominal) : '\u2014',
      cpuSort: cpuNominal > 0 ? cpuLast / cpuNominal : 0,
      memSpark: makeSpark(memValues),
      memLabel: points.length > 0 ? fmtMem(memLast) : '\u2014',
      memSort: memLast,
      diskSpark: makeSpark(diskValues),
      diskLabel: points.length > 0 ? fmtGB(diskLast) : '\u2014',
      diskSort: diskLast,
      ioSpark: makeSpark(ioValues),
      ioLabel: points.length > 0 ? fmtIO(ioLast) : '\u2014',
      ioSort: ioLast,
    })
  }

  // Sort by selected column.
  const col = sortCol.value
  const asc = sortAsc.value
  result.sort((a, b) => {
    let cmp: number
    switch (col) {
      case 'cpu':
        cmp = a.cpuSort - b.cpuSort
        break
      case 'mem':
        cmp = a.memSort - b.memSort
        break
      case 'disk':
        cmp = a.diskSort - b.diskSort
        break
      case 'io':
        cmp = a.ioSort - b.ioSort
        break
      default:
        cmp = a.name.localeCompare(b.name)
    }
    return asc ? cmp : -cmp
  })

  return result
})

// Filter rows by name.
const filteredRows = computed(() => {
  const q = props.filter.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter((r) => r.name.toLowerCase().includes(q))
})
</script>

<style scoped>
.usage-view {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* Beta banner */
.beta-banner {
  font-size: 12px;
  color: var(--badge-public-text);
  background: var(--badge-public-bg);
  border-radius: 6px;
  padding: 8px 12px;
  line-height: 1.5;
}
.beta-banner code {
  font-family: var(--font-mono);
  font-size: 11px;
  background: rgba(0, 0, 0, 0.08);
  padding: 1px 4px;
  border-radius: 3px;
}
.beta-banner-tag {
  font-weight: 800;
  text-transform: uppercase;
  font-size: 12px;
  letter-spacing: 0.5px;
  margin-right: 4px;
}

/* Table heading */
.table-heading {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-color-muted);
  display: flex;
  align-items: center;
  gap: 6px;
}
.table-heading-range {
  font-weight: 400;
  text-transform: none;
  letter-spacing: 0;
  font-size: 10px;
}

/* Table — matches .boxes-list from VMList.vue */
.boxes-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  background: var(--surface-border);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  overflow: hidden;
}

.usage-header {
  display: grid;
  grid-template-columns: minmax(160px, 1.5fr) 1fr 1fr 1fr 1fr;
  background: var(--surface-inset);
  padding: 0 16px;
}

.col-btn {
  background: none;
  border: none;
  padding: 6px 0;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  color: var(--text-color-muted);
  cursor: pointer;
  font-family: inherit;
  display: flex;
  align-items: center;
  gap: 4px;
}
.col-btn:hover {
  color: var(--text-color);
}
.col-btn.col-right {
  justify-content: flex-end;
}
.sort-icon {
  font-size: 10px;
  opacity: 0.6;
}

.box-row {
  display: grid;
  grid-template-columns: minmax(160px, 1.5fr) 1fr 1fr 1fr 1fr;
  align-items: center;
  background: var(--surface-card);
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.1s;
}
.box-row:hover {
  background: var(--surface-hover);
}

.vm-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 500;
  font-size: 13px;
  min-width: 0;
}
.vm-name {
  color: var(--text-color);
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.vm-name:hover {
  text-decoration: underline;
  color: var(--primary-color, var(--text-color));
}

.metric-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  justify-content: flex-end;
  font-family: var(--font-mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: var(--text-color);
}
.metric-value {
  min-width: 55px;
  text-align: right;
}

.spark-svg polyline {
  fill: none;
  stroke-width: 1.5;
}

.usage-loading {
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.usage-empty {
  text-align: center;
  padding: 48px;
  color: var(--text-color-muted);
}

@media (max-width: 768px) {
  .usage-header,
  .box-row {
    grid-template-columns: minmax(100px, 1.2fr) 1fr 1fr;
  }
  /* Hide disk and IO on mobile */
  .usage-header .col-btn:nth-child(4),
  .usage-header .col-btn:nth-child(5),
  .box-row > :nth-child(4),
  .box-row > :nth-child(5) {
    display: none;
  }
}
</style>

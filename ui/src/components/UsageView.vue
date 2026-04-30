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
    <DataTable
      :value="filteredRows"
      sortField="name"
      :sortOrder="1"
      stripedRows
      size="small"
      class="usage-table"
      tableStyle="width: 100%"
      @row-click="onRowClick"
    >
      <Column field="name" header="VM" sortable>
        <template #body="{ data }">
          <div class="vm-cell">
            <StatusDot :status="data.status" />
            <router-link :to="`/vm/${data.name}`" class="vm-name" @click.stop>{{ data.name }}</router-link>
          </div>
        </template>
      </Column>
      <Column field="cpuSort" header="vCPUs" sortable headerStyle="text-align: center; width: 15%" bodyStyle="text-align: center">
        <template #body="{ data }">
          <div class="metric-cell">
            <!-- <TufteSpark :values="data.cpuValues" :scale-max="data.cpuNominal" color="#ff7f0e" /> -->
            <span class="metric-value">{{ data.cpuLabel }}</span>
          </div>
        </template>
      </Column>
      <Column field="memSort" header="Memory" sortable headerStyle="text-align: center; width: 15%" bodyStyle="text-align: center">
        <template #body="{ data }">
          <div class="metric-cell">
            <!-- <TufteSpark :values="data.memValues" color="#1f77b4" /> -->
            <span class="metric-value">{{ data.memLabel }}</span>
          </div>
        </template>
      </Column>
      <Column field="diskSort" header="Disk" sortable headerStyle="text-align: center; width: 15%" bodyStyle="text-align: center">
        <template #body="{ data }">
          <div class="metric-cell">
            <!-- <TufteSpark :values="data.diskValues" color="#9467bd" /> -->
            <span class="metric-value">{{ data.diskLabel }}</span>
          </div>
        </template>
      </Column>
    </DataTable>
    </template>

    <!-- No data -->
    <div v-else-if="!historyLoading" class="usage-empty">
      No usage data available.
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
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
// import TufteSpark from './TufteSpark.vue'

const props = defineProps<{
  boxes: BoxInfo[]
  filter: string
  hours: number
}>()

const router = useRouter()
const pool = ref<VMsPoolResponse | null>(null)
const history = ref<UsageHistoryResponse>({})
const historyLoading = ref(false)
const ranges = [
  { hours: 24, label: '24h' },
  { hours: 168, label: '7d' },
  { hours: 720, label: '30d' },
]

const rangeLabel = computed(() => ranges.find((r) => r.hours === props.hours)?.label ?? '')

function onRowClick(e: { data: UsageRow }) {
  router.push(`/vm/${e.data.name}`)
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
watch(
  () => props.hours,
  async () => {
    historyLoading.value = true
    try {
      history.value = await fetchUsageHistory(props.hours)
    } catch {
      /* ignore */
    } finally {
      historyLoading.value = false
    }
  },
)

const boxStatusMap = computed(() => {
  const m = new Map<string, string>()
  for (const b of props.boxes) m.set(b.name, b.status)
  return m
})

interface UsageRow {
  name: string
  status: string
  cpuValues: number[]
  cpuNominal: number
  cpuLabel: string
  cpuSort: number
  memValues: number[]
  memLabel: string
  memSort: number
  diskValues: number[]
  diskLabel: string
  diskSort: number
}

function fmtCores(cores: number): string {
  if (cores < 0.01) return '0'
  if (cores < 0.1) return cores.toFixed(2)
  return cores.toFixed(1)
}

// Backend returns decimal GB (bytes / 1e9). Convert to GiB for display.
const GB_TO_GIB = 1e9 / (1024 * 1024 * 1024) // ~0.9313

function fmtMem(gb: number): string {
  const gib = gb * GB_TO_GIB
  if (gib >= 1) return gib.toFixed(1) + ' GiB'
  const mib = gib * 1024
  return mib < 1 ? '0 MiB' : mib.toFixed(0) + ' MiB'
}

function fmtDisk(gb: number): string {
  return (gb * GB_TO_GIB).toFixed(1) + ' GiB'
}

function lastVal(points: UsageDataPoint[], field: keyof UsageDataPoint): number {
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
    const cpuNominal = points.length > 0 ? lastVal(points, 'cpu_nominal') : 1

    const cpuLast = points.length > 0 ? lastVal(points, 'cpu_cores') : 0
    const memLast = points.length > 0 ? lastVal(points, 'memory_used_gb') : 0
    const diskLast = points.length > 0 ? lastVal(points, 'disk_used_gb') : 0

    result.push({
      name,
      status,
      cpuValues: points.map((p) => p.cpu_cores),
      cpuNominal: cpuNominal > 0 ? cpuNominal : 1,
      cpuLabel: points.length > 0 ? fmtCores(cpuLast) : '\u2014',
      cpuSort: cpuLast,
      memValues: points.map((p) => p.memory_used_gb),
      memLabel: points.length > 0 ? fmtMem(memLast) : '\u2014',
      memSort: memLast,
      diskValues: points.map((p) => p.disk_used_gb),
      diskLabel: points.length > 0 ? fmtDisk(diskLast) : '\u2014',
      diskSort: diskLast,
    })
  }

  return result
})

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

.usage-table :deep(.p-datatable-tbody > tr) {
  cursor: pointer;
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
  justify-content: center;
  font-family: var(--font-mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  color: var(--text-color);
}
.metric-value {
  text-align: center;
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
  .usage-table :deep(th:nth-child(4)),
  .usage-table :deep(td:nth-child(4)) {
    display: none;
  }
}
</style>

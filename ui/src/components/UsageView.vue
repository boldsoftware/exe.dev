<template>
  <div class="usage-view">
    <!-- Pool Charts -->
    <PoolCharts v-if="pool && pool.cpu_max > 0" :hours="props.hours" />

    <!-- Pool capacity warnings -->
    <Message v-if="poolAlert === 'danger'" severity="error" :closable="false" class="pool-alert">
      Your resource pool is at capacity.
      <router-link to="/user" class="pool-alert-link">Upgrade your plan</router-link> to add more resources.
    </Message>
    <Message v-else-if="poolAlert === 'warn'" severity="warn" :closable="false" class="pool-alert">
      Your resource pool is almost at capacity.
      <router-link to="/user" class="pool-alert-link">Upgrade your plan</router-link> to add more resources.
    </Message>

    <!-- Loading -->
    <div v-if="historyLoading" class="usage-loading">
      <i class="pi pi-spin pi-spinner"></i> Loading usage data...
    </div>

    <!-- Usage Table -->
    <template v-else-if="filteredRows.length > 0">
    <div class="table-heading">VMs</div>
    <div class="totals-row">
      <div class="totals-name">Total ({{ filteredRows.length }} VMs)</div>
      <div class="totals-metric">{{ totalCpuLabel }} <span v-if="cpuMax > 0" class="metric-denom">/ {{ cpuMax }}</span></div>
      <div class="totals-metric">{{ totalMemLabel }} <span v-if="memMaxLabel" class="metric-denom">/ {{ memMaxLabel }}</span></div>
      <div class="totals-metric">{{ totalDiskLabel }}</div>
    </div>
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
      <Column field="cpuSort" header="CPU" sortable headerStyle="text-align: center; width: 15%" bodyStyle="text-align: center">
        <template #body="{ data }">
          <div class="metric-cell">
            <span class="metric-value">{{ data.cpuLabel }}</span>
          </div>
        </template>
      </Column>
      <Column field="memSort" header="Memory" sortable headerStyle="text-align: center; width: 15%" bodyStyle="text-align: center">
        <template #body="{ data }">
          <div class="metric-cell">
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
import Message from 'primevue/message'
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

const cpuMax = computed(() => pool.value?.cpu_max ?? 0)

// Pool capacity alert: CPU uses actual usage, memory uses allocation.
const poolPct = computed(() => {
  const p = pool.value
  if (!p) return 0
  let maxPct = 0
  if (p.cpu_max > 0) {
    const cpuUsed = filteredRows.value.reduce((acc, r) => acc + r.cpuSort, 0)
    maxPct = Math.max(maxPct, (cpuUsed / p.cpu_max) * 100)
  }
  if (p.mem_max_bytes > 0) {
    maxPct = Math.max(maxPct, (p.mem_allocated_bytes / p.mem_max_bytes) * 100)
  }
  return maxPct
})
const poolAlert = computed<'danger' | 'warn' | null>(() => {
  if (poolPct.value >= 95) return 'danger'
  if (poolPct.value >= 75) return 'warn'
  return null
})

const memMaxLabel = computed(() => {
  const bytes = pool.value?.mem_max_bytes ?? 0
  if (bytes === 0) return ''
  const gib = bytes / (1024 * 1024 * 1024)
  if (gib >= 1) return gib.toFixed(1) + ' GiB'
  const mib = bytes / (1024 * 1024)
  return mib.toFixed(0) + ' MiB'
})

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

const totalCpuLabel = computed(() => {
  const sum = filteredRows.value.reduce((acc, r) => acc + r.cpuSort, 0)
  return fmtCores(sum)
})

const totalMemLabel = computed(() => {
  const sum = filteredRows.value.reduce((acc, r) => acc + r.memSort, 0)
  return fmtMem(sum)
})

const totalDiskLabel = computed(() => {
  const sum = filteredRows.value.reduce((acc, r) => acc + r.diskSort, 0)
  return fmtDisk(sum)
})
</script>

<style scoped>
.usage-view {
  display: flex;
  flex-direction: column;
  gap: 12px;
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

.totals-row {
  display: grid;
  grid-template-columns: 1fr 15% 15% 15%;
  align-items: center;
  position: sticky;
  top: 0;
  z-index: 2;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 8px 12px;
  font-family: var(--font-mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}
.totals-name {
  font-family: inherit;
  font-weight: 600;
  font-size: 13px;
  color: var(--text-color);
}
.totals-metric {
  text-align: center;
  color: var(--text-color);
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
.metric-denom {
  color: var(--text-color-muted);
  font-size: 11px;
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

.pool-alert :deep(.p-message-text) {
  font-size: 13px;
}

.pool-alert-link {
  font-weight: 600;
  text-decoration: underline;
}

@media (max-width: 768px) {
  .usage-table :deep(th:nth-child(4)),
  .usage-table :deep(td:nth-child(4)) {
    display: none;
  }
}
</style>

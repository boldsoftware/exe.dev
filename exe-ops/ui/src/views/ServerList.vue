<template>
  <div class="server-list">
    <div class="page-header">
      <div>
        <h1>Servers</h1>
        <p class="page-subtitle">All registered servers</p>
      </div>
      <div class="header-right">
        <span class="server-count-badge" v-if="servers.length > 0">
          <i class="pi pi-server"></i>
          {{ filteredServers.length }} of {{ servers.length }} server{{ servers.length !== 1 ? 's' : '' }}
        </span>
        <button
          v-if="uniqueRoles.length > 0 || uniqueRegions.length > 1 || uniqueEnvs.length > 1"
          class="mobile-filter-toggle"
          :class="{ 'has-active': activeRoles.size > 0 || activeRegions.size > 0 || activeEnvs.size > 0 }"
          @click="showFilters = !showFilters"
        >
          <i class="pi pi-filter"></i>
        </button>
        <div class="filter-groups" v-if="uniqueRoles.length > 0 || uniqueRegions.length > 1 || uniqueEnvs.length > 1">
          <div class="filter-group" v-if="uniqueRoles.length > 0">
            <span class="filter-label">Role</span>
            <div class="filter-buttons">
              <button
                v-for="role in uniqueRoles"
                :key="'role-' + role"
                class="filter-btn"
                :class="{ active: activeRoles.has(role) }"
                @click="toggleFilter(activeRoles, role)"
              >{{ role }}</button>
            </div>
          </div>
          <div class="filter-group" v-if="uniqueRegions.length > 1">
            <span class="filter-label">Region</span>
            <div class="filter-buttons">
              <button
                v-for="r in uniqueRegions"
                :key="'region-' + r"
                class="filter-btn"
                :class="{ active: activeRegions.has(r) }"
                @click="toggleFilter(activeRegions, r)"
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
                @click="toggleFilter(activeEnvs, e)"
              >{{ e }}</button>
            </div>
          </div>
        </div>
      </div>
    </div>
    <!-- Mobile filter panel -->
    <div v-if="showFilters" class="mobile-filter-panel">
      <div class="filter-group" v-if="uniqueRoles.length > 0">
        <span class="filter-label">Role</span>
        <div class="filter-buttons">
          <button
            v-for="role in uniqueRoles"
            :key="'mrole-' + role"
            class="filter-btn"
            :class="{ active: activeRoles.has(role) }"
            @click="toggleFilter(activeRoles, role)"
          >{{ role }}</button>
        </div>
      </div>
      <div class="filter-group" v-if="uniqueRegions.length > 1">
        <span class="filter-label">Region</span>
        <div class="filter-buttons">
          <button
            v-for="r in uniqueRegions"
            :key="'mregion-' + r"
            class="filter-btn"
            :class="{ active: activeRegions.has(r) }"
            @click="toggleFilter(activeRegions, r)"
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
            @click="toggleFilter(activeEnvs, e)"
          >{{ e }}</button>
        </div>
      </div>
    </div>

    <div v-if="error" class="message-banner message-error">
      <i class="pi pi-exclamation-triangle"></i>
      <span>{{ error }}</span>
    </div>

    <!-- Search toolbar -->
    <div v-if="servers.length > 0" class="toolbar">
      <div class="search-box">
        <i class="pi pi-search"></i>
        <input
          v-model="search"
          type="text"
          placeholder="Search servers..."
          class="search-input"
        />
        <button v-if="search" class="search-clear" @click="search = ''">
          <i class="pi pi-times"></i>
        </button>
      </div>
    </div>

    <!-- Bulk action toolbar -->
    <div v-if="selected.size > 0" class="bulk-toolbar">
      <span class="bulk-count">{{ selected.size }} selected</span>
      <button class="bulk-btn bulk-upgrade" @click="showBulkUpgrade = true" :disabled="upgradableSelected.length === 0">
        <i class="pi pi-arrow-up"></i> Upgrade Agents
        <span v-if="upgradableSelected.length > 0" class="bulk-btn-count">({{ upgradableSelected.length }})</span>
      </button>
      <button class="bulk-btn bulk-delete" @click="showBulkDelete = true">
        <i class="pi pi-trash"></i> Delete
        <span class="bulk-btn-count">({{ selected.size }})</span>
      </button>
      <button class="bulk-btn bulk-cancel" @click="selected.clear()">
        <i class="pi pi-times"></i> Clear
      </button>
    </div>

    <div v-if="loading && servers.length === 0" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i>
      <span>Loading servers...</span>
    </div>

    <div v-else-if="servers.length === 0" class="empty-state">
      No servers found.
    </div>

    <div v-else-if="filteredServers.length === 0" class="empty-state">
      No servers match the current filters.
    </div>

    <template v-else>
    <div class="table-wrapper desktop-only">
      <table class="server-table">
        <thead>
          <tr>
            <th class="col-checkbox" @click.stop>
              <input type="checkbox" :checked="allFilteredSelected" :indeterminate.prop="someFilteredSelected && !allFilteredSelected" @change="toggleSelectAll" />
            </th>
            <th class="sortable" @click="toggleSort('status')">
              Status
              <span class="sort-icon" v-if="sortKey === 'status'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('name')">
              Name
              <span class="sort-icon" v-if="sortKey === 'name'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('cpu')">
              CPU
              <span class="sort-icon" v-if="sortKey === 'cpu'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('mem')">
              Memory
              <span class="sort-icon" v-if="sortKey === 'mem'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('swap')">
              Swap
              <span class="sort-icon" v-if="sortKey === 'swap'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('disk')">
              Disk
              <span class="sort-icon" v-if="sortKey === 'disk'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th class="sortable" @click="toggleSort('capacity')">
              Capacity
              <span class="sort-icon" v-if="sortKey === 'capacity'">{{ sortAsc ? '\u25B2' : '\u25BC' }}</span>
            </th>
            <th>Components</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="s in filteredServers"
            :key="s.name"
            class="server-row"
            :class="{ 'row-selected': selected.has(s.name) }"
            @click="$router.push(`/servers/${s.name}`)"
          >
            <td class="col-checkbox" @click.stop>
              <input type="checkbox" :checked="selected.has(s.name)" @change="toggleSelect(s.name)" />
            </td>
            <td>
              <span class="status-dot" :class="isOnline(s) ? 'online' : 'offline'"></span>
            </td>
            <td class="col-name">{{ s.name }}</td>
            <td class="col-metric">
              <template v-if="isOnline(s)">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill" :class="barClass(s.cpu_percent)" :style="{ width: Math.min(100, s.cpu_percent) + '%' }"></div>
                  </div>
                  <span class="inline-bar-value" :class="barClass(s.cpu_percent)">{{ s.cpu_percent.toFixed(1) }}%</span>
                </div>
              </template>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-metric">
              <template v-if="isOnline(s)">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill" :class="memBarClass(memPercent(s))" :style="{ width: memPercent(s) + '%' }"></div>
                  </div>
                  <span class="inline-bar-value" :class="memBarClass(memPercent(s))">{{ memPercent(s).toFixed(0) }}%</span>
                </div>
              </template>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-metric">
              <template v-if="isOnline(s) && s.mem_swap_total > 0">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill" :class="barClass(swapPercent(s))" :style="{ width: swapPercent(s) + '%' }"></div>
                  </div>
                  <span class="inline-bar-value" :class="barClass(swapPercent(s))">{{ swapPercent(s).toFixed(0) }}%</span>
                </div>
              </template>
              <span v-else-if="!isOnline(s)" class="metric-blank">&mdash;</span>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-metric">
              <template v-if="isOnline(s)">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill" :class="barClass(diskPercent(s))" :style="{ width: diskPercent(s) + '%' }"></div>
                  </div>
                  <span class="inline-bar-value" :class="barClass(diskPercent(s))">{{ diskPercent(s).toFixed(0) }}%</span>
                </div>
              </template>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-metric">
              <template v-if="s.capacity && s.capacity > 0">
                <div class="inline-bar">
                  <div class="inline-bar-track">
                    <div class="inline-bar-fill bar-normal" :style="{ width: capacityPercent(s) + '%' }"></div>
                  </div>
                  <span class="inline-bar-value bar-normal">{{ s.instances ?? 0 }}/{{ s.capacity }}</span>
                </div>
              </template>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-component">
              <template v-if="s.components && s.components.length > 0">
                <span v-for="c in s.components" :key="c.name" class="component-tag">
                  <span class="component-label">{{ c.name }}</span>
                  <span class="component-ver">{{ c.version || '-' }}</span>
                </span>
              </template>
              <span v-else class="metric-blank">&mdash;</span>
            </td>
            <td class="col-actions" @click.stop>
              <button
                class="delete-btn"
                :disabled="deleting.has(s.name)"
                @click="handleDelete(s)"
                title="Delete server"
              >
                <i class="pi" :class="deleting.has(s.name) ? 'pi-spin pi-spinner' : 'pi-trash'"></i>
              </button>
              <button
                v-if="needsUpgrade(s)"
                class="upgrade-btn"
                :class="{ pending: s.upgrade_available }"
                :disabled="upgrading.has(s.name) || s.upgrade_available"
                @click="handleUpgrade(s)"
                :title="s.upgrade_available ? 'Upgrade pending' : `Upgrade to ${serverVersion}`"
              >
                <i class="pi" :class="upgrading.has(s.name) ? 'pi-spin pi-spinner' : s.upgrade_available ? 'pi-clock' : 'pi-arrow-up'"></i>
              </button>
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
          <span class="status-dot" :class="isOnline(s) ? 'online' : 'offline'"></span>
          <span class="col-name">{{ s.name }}</span>
          <div class="mobile-card-actions" @click.stop>
            <button
              class="delete-btn"
              :disabled="deleting.has(s.name)"
              @click="handleDelete(s)"
              title="Delete server"
            >
              <i class="pi" :class="deleting.has(s.name) ? 'pi-spin pi-spinner' : 'pi-trash'"></i>
            </button>
            <button
              v-if="needsUpgrade(s)"
              class="upgrade-btn"
              :class="{ pending: s.upgrade_available }"
              :disabled="upgrading.has(s.name) || s.upgrade_available"
              @click="handleUpgrade(s)"
              :title="s.upgrade_available ? 'Upgrade pending' : `Upgrade to ${serverVersion}`"
            >
              <i class="pi" :class="upgrading.has(s.name) ? 'pi-spin pi-spinner' : s.upgrade_available ? 'pi-clock' : 'pi-arrow-up'"></i>
            </button>
          </div>
        </div>
        <template v-if="isOnline(s)">
          <div class="mobile-card-metrics">
            <div class="mobile-metric">
              <span class="mobile-metric-label">CPU</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill" :class="barClass(s.cpu_percent)" :style="{ width: Math.min(100, s.cpu_percent) + '%' }"></div>
                </div>
                <span class="inline-bar-value" :class="barClass(s.cpu_percent)">{{ s.cpu_percent.toFixed(1) }}%</span>
              </div>
            </div>
            <div class="mobile-metric">
              <span class="mobile-metric-label">Mem</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill" :class="memBarClass(memPercent(s))" :style="{ width: memPercent(s) + '%' }"></div>
                </div>
                <span class="inline-bar-value" :class="memBarClass(memPercent(s))">{{ memPercent(s).toFixed(0) }}%</span>
              </div>
            </div>
            <div class="mobile-metric" v-if="s.mem_swap_total > 0">
              <span class="mobile-metric-label">Swap</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill" :class="barClass(swapPercent(s))" :style="{ width: swapPercent(s) + '%' }"></div>
                </div>
                <span class="inline-bar-value" :class="barClass(swapPercent(s))">{{ swapPercent(s).toFixed(0) }}%</span>
              </div>
            </div>
            <div class="mobile-metric">
              <span class="mobile-metric-label">Disk</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill" :class="barClass(diskPercent(s))" :style="{ width: diskPercent(s) + '%' }"></div>
                </div>
                <span class="inline-bar-value" :class="barClass(diskPercent(s))">{{ diskPercent(s).toFixed(0) }}%</span>
              </div>
            </div>
            <div class="mobile-metric" v-if="s.capacity && s.capacity > 0">
              <span class="mobile-metric-label">Cap</span>
              <div class="inline-bar">
                <div class="inline-bar-track">
                  <div class="inline-bar-fill bar-normal" :style="{ width: capacityPercent(s) + '%' }"></div>
                </div>
                <span class="inline-bar-value bar-normal">{{ s.instances ?? 0 }}/{{ s.capacity }}</span>
              </div>
            </div>
          </div>
        </template>
        <div v-if="s.components && s.components.length > 0" class="mobile-card-components">
          <span v-for="c in s.components" :key="c.name" class="component-tag">
            <span class="component-label">{{ c.name }}</span>
            <span class="component-ver">{{ c.version || '-' }}</span>
          </span>
        </div>
      </div>
    </div>
    </template>
    <ConfirmDialog
      :visible="!!upgradeTarget"
      title="Upgrade Agent"
      :message="`Upgrade ${upgradeTarget?.name} from ${upgradeTarget?.agent_version} to ${serverVersion}? The agent will download the new binary and restart.`"
      confirmLabel="Upgrade"
      variant="primary"
      :loading="upgradeLoading"
      @confirm="confirmUpgrade"
      @cancel="cancelUpgrade"
    />
    <ConfirmDialog
      :visible="!!deleteTarget"
      title="Delete Server"
      :message="`This will permanently remove ${deleteTarget?.name} and all its historical data. This action cannot be undone.`"
      confirmLabel="Delete"
      variant="danger"
      :loading="deleteLoading"
      @confirm="confirmDelete"
      @cancel="cancelDelete"
    />
    <ConfirmDialog
      :visible="showBulkUpgrade"
      title="Upgrade Agents"
      :message="`Upgrade ${upgradableSelected.length} agent${upgradableSelected.length !== 1 ? 's' : ''} to ${serverVersion}? Each agent will download the new binary and restart.`"
      confirmLabel="Upgrade All"
      variant="primary"
      :loading="bulkUpgradeLoading"
      @confirm="confirmBulkUpgrade"
      @cancel="showBulkUpgrade = false"
    />
    <ConfirmDialog
      :visible="showBulkDelete"
      title="Delete Servers"
      :message="`Permanently delete ${selected.size} server${selected.size !== 1 ? 's' : ''} and all their historical data? This action cannot be undone.`"
      confirmLabel="Delete All"
      variant="danger"
      :loading="bulkDeleteLoading"
      @confirm="confirmBulkDelete"
      @cancel="showBulkDelete = false"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch, onMounted, onUnmounted } from 'vue'
import { fetchServers, fetchServerVersion, triggerUpgrade, deleteServer, type ServerSummary } from '../api/client'
import ConfirmDialog from '../components/ConfirmDialog.vue'

const servers = ref<ServerSummary[]>([])
const loading = ref(true)
const error = ref('')
const serverVersion = ref('')
const upgrading = reactive(new Set<string>())
const deleting = reactive(new Set<string>())
const upgradeTarget = ref<ServerSummary | null>(null)
const upgradeLoading = ref(false)
const deleteTarget = ref<ServerSummary | null>(null)
const deleteLoading = ref(false)
const selected = reactive(new Set<string>())
const showBulkUpgrade = ref(false)
const showBulkDelete = ref(false)
const bulkUpgradeLoading = ref(false)
const bulkDeleteLoading = ref(false)
let pollTimer: ReturnType<typeof setInterval> | null = null

// Search
const search = ref('')

// Filters
const activeRegions = reactive(new Set<string>())
const activeEnvs = reactive(new Set<string>())
const activeRoles = reactive(new Set<string>())
const showFilters = ref(false)

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

// Sorting
const sortKey = ref<string>('')
const sortAsc = ref(true)

function isOnline(s: ServerSummary): boolean {
  return Date.now() - new Date(s.last_seen).getTime() < 120_000
}

function memPercent(s: ServerSummary): number {
  if (s.mem_total === 0) return 0
  return Math.min(100, (s.mem_used / s.mem_total) * 100)
}

function swapPercent(s: ServerSummary): number {
  if (s.mem_swap_total === 0) return 0
  return Math.min(100, (s.mem_swap / s.mem_swap_total) * 100)
}

function diskPercent(s: ServerSummary): number {
  if (s.disk_total === 0) return 0
  return Math.min(100, (s.disk_used / s.disk_total) * 100)
}

function capacityPercent(s: ServerSummary): number {
  if (!s.capacity || s.capacity === 0) return 0
  return Math.min(100, ((s.instances ?? 0) / s.capacity) * 100)
}

function barClass(percent: number): string {
  if (percent >= 90) return 'bar-danger'
  if (percent >= 70) return 'bar-warning'
  return 'bar-normal'
}

function memBarClass(percent: number): string {
  if (percent >= 96) return 'bar-danger'
  if (percent >= 90) return 'bar-warning'
  return 'bar-normal'
}

// Unique values for filter buttons
const uniqueRegions = computed(() =>
  [...new Set(servers.value.map(s => s.region).filter(Boolean))].sort()
)
const uniqueEnvs = computed(() =>
  [...new Set(servers.value.map(s => s.env).filter(Boolean))].sort()
)
const uniqueRoles = computed(() =>
  [...new Set(servers.value.map(s => s.role).filter(Boolean))].sort()
)

const hasActiveFilters = computed(() =>
  activeRegions.size > 0 || activeEnvs.size > 0 || activeRoles.size > 0 || search.value !== ''
)

function toggleFilter(set: Set<string>, value: string) {
  if (set.has(value)) {
    set.delete(value)
  } else {
    set.add(value)
  }
}

function clearFilters() {
  activeRegions.clear()
  activeEnvs.clear()
  activeRoles.clear()
  search.value = ''
}

function toggleSort(key: string) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value
  } else {
    sortKey.value = key
    sortAsc.value = true
  }
}

function getSortValue(s: ServerSummary, key: string): string | number {
  switch (key) {
    case 'status': return isOnline(s) ? 0 : 1
    case 'name': return s.name.toLowerCase()
    case 'cpu': return s.cpu_percent
    case 'mem': return s.mem_total > 0 ? s.mem_used / s.mem_total : 0
    case 'swap': return s.mem_swap_total > 0 ? s.mem_swap / s.mem_swap_total : 0
    case 'disk': return s.disk_total > 0 ? s.disk_used / s.disk_total : 0
    case 'capacity': return s.capacity && s.capacity > 0 ? (s.instances ?? 0) / s.capacity : -1
    default: return ''
  }
}

const filteredServers = computed(() => {
  const q = search.value.toLowerCase().trim()

  let result = servers.value.filter(s => {
    // Text search
    if (q && !s.name.toLowerCase().includes(q) &&
        !s.region.toLowerCase().includes(q) &&
        !s.env.toLowerCase().includes(q) &&
        !s.role.toLowerCase().includes(q)) {
      return false
    }
    // Button filters
    if (activeRegions.size > 0 && !activeRegions.has(s.region)) return false
    if (activeEnvs.size > 0 && !activeEnvs.has(s.env)) return false
    if (activeRoles.size > 0 && !activeRoles.has(s.role)) return false
    return true
  })

  if (sortKey.value) {
    result = [...result].sort((a, b) => {
      const va = getSortValue(a, sortKey.value)
      const vb = getSortValue(b, sortKey.value)
      let cmp: number
      if (typeof va === 'number' && typeof vb === 'number') {
        cmp = va - vb
      } else {
        cmp = String(va).localeCompare(String(vb))
      }
      return sortAsc.value ? cmp : -cmp
    })
  }

  return result
})

function needsUpgrade(s: ServerSummary): boolean {
  if (!s.agent_version || !serverVersion.value) return false
  return s.agent_version !== serverVersion.value
}

function handleUpgrade(s: ServerSummary) {
  upgradeTarget.value = s
}

async function confirmUpgrade() {
  const s = upgradeTarget.value
  if (!s) return

  upgradeLoading.value = true
  upgrading.add(s.name)
  try {
    await triggerUpgrade(s.name)
    upgradeTarget.value = null
    await load()
  } catch (e: any) {
    error.value = 'Upgrade failed: ' + (e.message || 'unknown error')
    upgradeTarget.value = null
  } finally {
    upgradeLoading.value = false
    upgrading.delete(s.name)
  }
}

function cancelUpgrade() {
  upgradeTarget.value = null
}

// Selection
const allFilteredSelected = computed(() =>
  filteredServers.value.length > 0 && filteredServers.value.every(s => selected.has(s.name))
)
const someFilteredSelected = computed(() =>
  filteredServers.value.some(s => selected.has(s.name))
)
const upgradableSelected = computed(() =>
  [...selected].filter(name => {
    const s = servers.value.find(sv => sv.name === name)
    return s && needsUpgrade(s) && !s.upgrade_available
  })
)

function toggleSelect(name: string) {
  if (selected.has(name)) {
    selected.delete(name)
  } else {
    selected.add(name)
  }
}

function toggleSelectAll() {
  if (allFilteredSelected.value) {
    for (const s of filteredServers.value) selected.delete(s.name)
  } else {
    for (const s of filteredServers.value) selected.add(s.name)
  }
}

async function confirmBulkUpgrade() {
  bulkUpgradeLoading.value = true
  const names = [...upgradableSelected.value]
  try {
    await Promise.all(names.map(name => triggerUpgrade(name)))
    showBulkUpgrade.value = false
    selected.clear()
    await load()
  } catch (e: any) {
    error.value = 'Bulk upgrade failed: ' + (e.message || 'unknown error')
    showBulkUpgrade.value = false
  } finally {
    bulkUpgradeLoading.value = false
  }
}

async function confirmBulkDelete() {
  bulkDeleteLoading.value = true
  const names = [...selected]
  try {
    await Promise.all(names.map(name => deleteServer(name)))
    showBulkDelete.value = false
    selected.clear()
    await load()
  } catch (e: any) {
    error.value = 'Bulk delete failed: ' + (e.message || 'unknown error')
    showBulkDelete.value = false
  } finally {
    bulkDeleteLoading.value = false
  }
}

function handleDelete(s: ServerSummary) {
  deleteTarget.value = s
}

async function confirmDelete() {
  const s = deleteTarget.value
  if (!s) return

  deleteLoading.value = true
  deleting.add(s.name)
  try {
    await deleteServer(s.name)
    deleteTarget.value = null
    await load()
  } catch (e: any) {
    error.value = 'Delete failed: ' + (e.message || 'unknown error')
    deleteTarget.value = null
  } finally {
    deleteLoading.value = false
    deleting.delete(s.name)
  }
}

function cancelDelete() {
  deleteTarget.value = null
}

async function load() {
  try {
    servers.value = await fetchServers()
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load servers'
  } finally {
    loading.value = false
  }
}

async function loadVersion() {
  try {
    const v = await fetchServerVersion()
    serverVersion.value = v.version
  } catch {
    // Non-critical, ignore.
  }
}

onMounted(() => {
  load()
  loadVersion()
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
  color: var(--text-color);
}

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.server-count-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--text-color-secondary);
  background: var(--surface-card);
  padding: 0.375rem 0.75rem;
  border-radius: 4px;
  border: 1px solid var(--surface-border);
}

/* Toolbar: search + filters */
.toolbar {
  margin-bottom: 1rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
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

/* Filter groups */
.filter-groups {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 1rem;
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

/* Bulk toolbar */
.bulk-toolbar {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  margin-bottom: 0.5rem;
  background: var(--surface-card);
  border: 1px solid var(--primary-color);
  border-radius: 4px;
}

.bulk-count {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--text-color);
  margin-right: 0.5rem;
}

.bulk-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.375rem 0.75rem;
  font-size: 0.75rem;
  font-family: inherit;
  font-weight: 500;
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s;
  background: var(--surface-card);
  color: var(--text-color-secondary);
}

.bulk-btn:hover:not(:disabled) {
  border-color: var(--surface-border-bright);
  color: var(--text-color);
}

.bulk-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.bulk-btn-count {
  opacity: 0.7;
}

.bulk-upgrade:hover:not(:disabled) {
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.bulk-delete:hover:not(:disabled) {
  border-color: var(--red-400, #f85149);
  color: var(--red-400, #f85149);
}

.bulk-cancel {
  margin-left: auto;
}

/* Checkbox column */
.col-checkbox {
  width: 36px;
  text-align: center;
  padding: 0.5rem 0.5rem 0.5rem 1rem !important;
}

.col-checkbox input[type="checkbox"] {
  cursor: pointer;
  accent-color: var(--primary-color);
  color-scheme: dark;
}

:global(.light-mode) .col-checkbox input[type="checkbox"] {
  color-scheme: light;
}

.row-selected {
  background: var(--primary-50, rgba(56, 139, 253, 0.06));
}

.row-selected:hover {
  background: var(--primary-50, rgba(56, 139, 253, 0.1)) !important;
}

/* Table */
.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
}

.server-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.server-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
  white-space: nowrap;
  user-select: none;
}

.server-table th.sortable {
  cursor: pointer;
  transition: color 0.15s;
}

.server-table th.sortable:hover {
  color: var(--text-color);
}

.sort-icon {
  font-size: 0.55rem;
  margin-left: 0.25rem;
  color: var(--primary-color);
}

.server-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  white-space: nowrap;
  color: var(--text-color-secondary);
}

.server-row {
  cursor: pointer;
  transition: background 0.15s;
}

.server-row:hover {
  background: var(--surface-hover);
}

.server-row:last-child td {
  border-bottom: none;
}

.col-name {
  font-weight: 600;
  color: var(--text-color);
}

.col-component {
  font-size: 0.75rem;
  color: var(--text-color-secondary);
}

.component-tag {
  display: inline-flex;
  align-items: center;
  background: var(--surface-overlay);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  overflow: hidden;
  margin-right: 0.375rem;
  margin-bottom: 0.125rem;
}

.component-label {
  font-size: 0.6rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-muted);
  padding: 0.2rem 0.4rem;
  background: var(--surface-border);
}

.component-ver {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  color: var(--text-color-secondary);
  padding: 0.2rem 0.4rem;
}

.status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.status-dot.online {
  background: var(--green-500);
  box-shadow: 0 0 6px rgba(63, 185, 80, 0.4);
}

.status-dot.offline {
  background: var(--text-color-muted);
}

.col-actions {
  text-align: left;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 0.35rem;
}

.upgrade-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  font-size: 0.75rem;
  background: none;
  border: 1px solid transparent;
  border-radius: 3px;
  color: var(--primary-color);
  cursor: pointer;
  transition: all 0.15s;
}

.upgrade-btn:hover:not(:disabled) {
  background: var(--primary-50);
  border-color: var(--primary-color);
}

.upgrade-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.upgrade-btn.pending {
  color: var(--yellow-400, #e3b341);
}

.upgrade-btn.pending:hover:not(:disabled) {
  background: var(--yellow-subtle, rgba(227, 179, 65, 0.15));
  border-color: var(--yellow-400, #e3b341);
}

.delete-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  font-size: 0.75rem;
  background: none;
  border: 1px solid transparent;
  border-radius: 3px;
  color: var(--text-color-muted);
  cursor: pointer;
  transition: all 0.15s;
}

.delete-btn:hover:not(:disabled) {
  background: var(--red-subtle, rgba(248, 81, 73, 0.1));
  border-color: var(--red-400, #f85149);
  color: var(--red-400, #f85149);
}

.delete-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Inline metric bars */
.col-metric {
  min-width: 110px;
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

.inline-bar-fill.bar-normal {
  background: var(--green-500, #3fb950);
}

.inline-bar-fill.bar-warning {
  background: var(--yellow-400, #e3b341);
}

.inline-bar-fill.bar-danger {
  background: var(--red-400, #f85149);
}

.inline-bar-value {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  min-width: 3ch;
  text-align: right;
  white-space: nowrap;
}

.inline-bar-value.bar-normal {
  color: var(--green-500, #3fb950);
}

.inline-bar-value.bar-warning {
  color: var(--yellow-400, #e3b341);
}

.inline-bar-value.bar-danger {
  color: var(--red-400, #f85149);
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

  .toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .search-box {
    max-width: 100%;
  }

  .filter-groups {
    gap: 0.5rem;
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
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .mobile-card-header .col-name {
    flex: 1;
  }

  .mobile-card-actions {
    display: flex;
    align-items: center;
    gap: 0.35rem;
  }

  .mobile-card-metrics {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
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
    width: 36px;
    flex-shrink: 0;
  }

  .mobile-metric .inline-bar {
    flex: 1;
  }

  .mobile-card-components {
    margin-top: 0.5rem;
    padding-top: 0.5rem;
    border-top: 1px solid var(--surface-border);
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
}
</style>

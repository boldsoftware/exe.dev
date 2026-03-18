<template>
  <div class="alerts-view">
    <div class="page-header">
      <div>
        <h1>Alerts</h1>
        <p class="page-subtitle">Servers with active problems</p>
      </div>
      <div class="header-right">
        <span class="alert-count-badge" v-if="alerts.length > 0">
          <i class="pi pi-exclamation-triangle"></i>
          <span class="alert-count-full">{{ alerts.length }} alert{{ alerts.length !== 1 ? 's' : '' }} on {{ alertedServerCount }} server{{ alertedServerCount !== 1 ? 's' : '' }}</span>
          <span class="alert-count-short">{{ alerts.length }} alert{{ alerts.length !== 1 ? 's' : '' }}</span>
        </span>
        <button class="manage-rules-btn" @click="showRulesDialog = true" :class="{ 'has-rules': customRules.length > 0 }">
          <i class="pi pi-cog"></i>
          <span class="manage-rules-label">Rules</span>
          <span v-if="customRules.length > 0" class="rules-count-badge">{{ customRules.length }}</span>
        </button>
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
      <div class="summary-row">
        <div class="summary-card">
          <div class="summary-value">{{ alerts.length }}</div>
          <div class="summary-label">Total Alerts</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-red">{{ alerts.filter(a => a.severity === 'critical').length }}</div>
          <div class="summary-label">Critical</div>
        </div>
        <div class="summary-card">
          <div class="summary-value value-yellow">{{ alerts.filter(a => a.severity === 'warning').length }}</div>
          <div class="summary-label">Warnings</div>
        </div>
        <div class="summary-card">
          <div class="summary-value">{{ alertedServerCount }}</div>
          <div class="summary-label">Affected Servers</div>
        </div>
        <div class="summary-card">
          <div class="summary-value" :class="fleetHealthClass">{{ fleetHealthPct.toFixed(0) }}%</div>
          <div class="summary-label">Fleet Health</div>
        </div>
        <div class="summary-card" v-if="failedUnitCount > 0">
          <div class="summary-value value-red">{{ failedUnitCount }}</div>
          <div class="summary-label">Failed Units</div>
        </div>
        <div class="summary-card" v-if="unhealthyPoolCount > 0">
          <div class="summary-value value-red">{{ unhealthyPoolCount }}</div>
          <div class="summary-label">Unhealthy Pools</div>
        </div>
        <div class="summary-card" v-if="netIssueServerCount > 0">
          <div class="summary-value value-yellow">{{ netIssueServerCount }}</div>
          <div class="summary-label">Network Issues</div>
        </div>
        <div class="summary-card" v-if="resourcePressureCount > 0">
          <div class="summary-value value-yellow">{{ resourcePressureCount }}</div>
          <div class="summary-label">Resource Pressure</div>
        </div>
        <div class="summary-card" v-if="staleAgentCount > 0">
          <div class="summary-value value-yellow">{{ staleAgentCount }}</div>
          <div class="summary-label">Stale Agents</div>
        </div>
        <div class="summary-card" v-if="customAlertCount > 0">
          <div class="summary-value" :class="customAlertCriticalCount > 0 ? 'value-red' : 'value-yellow'">{{ customAlertCount }}</div>
          <div class="summary-label">Custom Alerts</div>
        </div>
      </div>

      <div v-if="alerts.length === 0" class="empty-state">
        No active alerts. All systems healthy.
      </div>

      <!-- Category tabs -->
      <div v-if="alerts.length > 0" class="tab-bar">
        <button
          v-for="tab in (['all', 'network', 'agent', 'fleet', 'custom'] as const)"
          :key="tab"
          class="tab-btn"
          :class="{ active: activeTab === tab }"
          @click="activeTab = tab"
        >
          {{ categoryLabels[tab] }}
          <span class="tab-count" v-if="tabCounts[tab] > 0">{{ tabCounts[tab] }}</span>
        </button>
      </div>

      <div v-if="alerts.length > 0 && tabAlerts.length === 0" class="empty-state">
        No {{ categoryLabels[activeTab].toLowerCase() }} alerts.
      </div>

      <!-- Critical alerts -->
      <div v-if="criticalAlerts.length > 0" class="alert-section">
        <div class="section-header section-critical">
          <span class="severity-badge badge-critical">Critical</span>
          <span class="section-count">{{ criticalAlerts.length }}</span>
        </div>
        <div class="table-wrapper desktop-only">
          <table class="alert-table">
            <thead>
              <tr>
                <th>Server</th>
                <th>Alert</th>
                <th>Value</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in criticalAlerts" :key="a.key" class="alert-row" @click="$router.push(`/servers/${a.server}`)">
                <td class="col-server">{{ a.server }}</td>
                <td>{{ a.type }}</td>
                <td class="col-value">{{ a.value }}</td>
                <td class="col-actions"></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="mobile-cards mobile-only">
          <div v-for="a in criticalAlerts" :key="a.key" class="mobile-card mobile-card-critical" @click="$router.push(`/servers/${a.server}`)">
            <div class="mobile-card-header">
              <span class="col-server">{{ a.server }}</span>
            </div>
            <div class="mobile-card-body">
              <span class="mobile-card-type">{{ a.type }}</span>
              <span class="col-value">{{ a.value }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Warning alerts -->
      <div v-if="warningAlerts.length > 0" class="alert-section">
        <div class="section-header section-warning">
          <span class="severity-badge badge-warning">Warning</span>
          <span class="section-count">{{ warningAlerts.length }}</span>
        </div>
        <div class="table-wrapper desktop-only">
          <table class="alert-table">
            <thead>
              <tr>
                <th>Server</th>
                <th>Alert</th>
                <th>Value</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in warningAlerts" :key="a.key" class="alert-row" @click="$router.push(`/servers/${a.server}`)">
                <td class="col-server">{{ a.server }}</td>
                <td>{{ a.type }}</td>
                <td class="col-value">{{ a.value }}</td>
                <td class="col-actions">
                  <button v-if="a.dismissable" class="dismiss-btn" title="Clear alert" @click.stop="dismissNetAlert(a.server)">
                    <i class="pi pi-times"></i>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="mobile-cards mobile-only">
          <div v-for="a in warningAlerts" :key="a.key" class="mobile-card mobile-card-warning" @click="$router.push(`/servers/${a.server}`)">
            <div class="mobile-card-header">
              <span class="col-server">{{ a.server }}</span>
              <button v-if="a.dismissable" class="dismiss-btn" title="Clear alert" @click.stop="dismissNetAlert(a.server)">
                <i class="pi pi-times"></i>
              </button>
            </div>
            <div class="mobile-card-body">
              <span class="mobile-card-type">{{ a.type }}</span>
              <span class="col-value">{{ a.value }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- Rules management dialog -->
    <Teleport to="body">
      <div v-if="showRulesDialog" class="dialog-overlay" @click.self="showRulesDialog = false">
        <div class="dialog">
          <div class="dialog-header">
            <h2>Custom Alert Rules</h2>
            <div class="dialog-header-actions">
              <button v-if="!showRuleForm" class="add-rule-btn" @click="resetRuleForm(); showRuleForm = true">
                <i class="pi pi-plus"></i> Add Rule
              </button>
              <button class="dialog-close" @click="showRulesDialog = false">
                <i class="pi pi-times"></i>
              </button>
            </div>
          </div>
          <div class="dialog-body">
            <!-- New rule form -->
            <div v-if="showRuleForm" class="rule-form">
              <div class="form-row">
                <label>Name</label>
                <input v-model="newRule.name" type="text" placeholder="e.g. High ZFS fragmentation" class="form-input" />
              </div>
              <div class="form-row form-row-inline">
                <div class="form-field">
                  <label>Metric</label>
                  <select v-model="newRule.metric" class="form-input">
                    <option v-for="m in availableMetrics" :key="m.value" :value="m.value">{{ m.label }}</option>
                  </select>
                </div>
                <div class="form-field form-field-sm">
                  <label>Operator</label>
                  <select v-model="newRule.operator" class="form-input">
                    <option value=">">&gt;</option>
                    <option value="<">&lt;</option>
                    <option value=">=">&gt;=</option>
                    <option value="<=">&lt;=</option>
                    <option value="==">==</option>
                    <option value="!=">!=</option>
                  </select>
                </div>
                <div class="form-field form-field-sm">
                  <label>Threshold</label>
                  <input v-model.number="newRule.threshold" type="number" step="any" class="form-input" />
                </div>
                <div class="form-field form-field-sm">
                  <label>Severity</label>
                  <select v-model="newRule.severity" class="form-input">
                    <option value="warning">Warning</option>
                    <option value="critical">Critical</option>
                  </select>
                </div>
              </div>
              <div class="form-actions">
                <button class="save-rule-btn" @click="saveRule" :disabled="!newRule.name">{{ editingRuleId != null ? 'Update Rule' : 'Save Rule' }}</button>
                <button class="cancel-rule-btn" @click="showRuleForm = false; resetRuleForm()">Cancel</button>
              </div>
            </div>

            <!-- Existing rules list -->
            <div v-if="customRules.length > 0" class="rules-list">
              <div v-for="rule in customRules" :key="rule.id" class="rule-item" :class="{ 'rule-disabled': !rule.enabled }">
                <div class="rule-info">
                  <span class="rule-name">{{ rule.name }}</span>
                  <span class="rule-expr">{{ availableMetrics.find(m => m.value === rule.metric)?.label || rule.metric }} {{ rule.operator }} {{ rule.threshold }}</span>
                  <span class="severity-badge" :class="rule.severity === 'critical' ? 'badge-critical' : 'badge-warning'">{{ rule.severity }}</span>
                </div>
                <div class="rule-actions">
                  <button class="dismiss-btn" title="Edit rule" @click="editRule(rule)">
                    <i class="pi pi-pencil"></i>
                  </button>
                  <button class="dismiss-btn" title="Delete rule" @click="removeRule(rule.id)">
                    <i class="pi pi-trash"></i>
                  </button>
                </div>
              </div>
            </div>
            <div v-else-if="!showRuleForm" class="empty-state-sm">
              No custom alert rules defined yet.
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted } from 'vue'
import { fetchFleet, resetNetCounters, fetchCustomAlerts, createCustomAlert, updateCustomAlert, deleteCustomAlert, type FleetServer, type CustomAlertRule } from '../api/client'

type AlertCategory = 'network' | 'agent' | 'fleet' | 'custom'

interface Alert {
  key: string
  server: string
  type: string
  value: string
  severity: 'critical' | 'warning'
  category: AlertCategory
  dismissable?: boolean
}

const servers = ref<FleetServer[]>([])
const customRules = ref<CustomAlertRule[]>([])
const loading = ref(true)
const error = ref('')
const activeRegions = reactive(new Set<string>())
const activeEnvs = reactive(new Set<string>())
const activeTab = ref<'all' | AlertCategory>('all')
const showFilters = ref(false)
const showRuleForm = ref(false)
const showRulesDialog = ref(false)
const editingRuleId = ref<number | null>(null)
const newRule = reactive({
  name: '',
  metric: 'cpu_pct',
  operator: '>',
  threshold: 90,
  severity: 'warning' as 'warning' | 'critical',
  enabled: true,
})
let pollTimer: ReturnType<typeof setInterval> | null = null

const availableMetrics: { value: string; label: string; unit: string }[] = [
  { value: 'cpu_pct', label: 'CPU %', unit: '%' },
  { value: 'mem_pct', label: 'Memory %', unit: '%' },
  { value: 'disk_pct', label: 'Disk %', unit: '%' },
  { value: 'conntrack_pct', label: 'Conntrack %', unit: '%' },
  { value: 'fd_pct', label: 'File descriptors %', unit: '%' },
  { value: 'zfs_frag_avg', label: 'ZFS avg fragmentation', unit: '%' },
  { value: 'zfs_frag_max', label: 'ZFS max fragmentation', unit: '%' },
  { value: 'zfs_cap_max', label: 'ZFS max capacity', unit: '%' },
  { value: 'pending_updates', label: 'Pending updates', unit: '' },
  { value: 'failed_units_count', label: 'Failed units count', unit: '' },
  { value: 'net_rx_errors', label: 'Net RX errors', unit: '' },
  { value: 'net_tx_errors', label: 'Net TX errors', unit: '' },
  { value: 'net_rx_dropped', label: 'Net RX dropped', unit: '' },
  { value: 'net_tx_dropped', label: 'Net TX dropped', unit: '' },
]

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

function getMetricValue(s: FleetServer, metric: string): number | null {
  switch (metric) {
    case 'cpu_pct': return s.cpu_percent
    case 'mem_pct': return s.mem_total > 0 ? (s.mem_used / s.mem_total) * 100 : null
    case 'disk_pct': return s.disk_total > 0 ? (s.disk_used / s.disk_total) * 100 : null
    case 'conntrack_pct':
      return s.conntrack_count != null && s.conntrack_max != null && s.conntrack_max > 0
        ? (s.conntrack_count / s.conntrack_max) * 100 : null
    case 'fd_pct': return s.fd_max > 0 ? (s.fd_allocated / s.fd_max) * 100 : null
    case 'zfs_frag_avg': {
      if (!s.zfs_pools || s.zfs_pools.length === 0) return null
      const valid = s.zfs_pools.filter(p => p.frag_pct >= 0)
      if (valid.length === 0) return null
      return valid.reduce((sum, p) => sum + p.frag_pct, 0) / valid.length
    }
    case 'zfs_frag_max': {
      if (!s.zfs_pools || s.zfs_pools.length === 0) return null
      const valid = s.zfs_pools.filter(p => p.frag_pct >= 0)
      if (valid.length === 0) return null
      return Math.max(...valid.map(p => p.frag_pct))
    }
    case 'zfs_cap_max': {
      if (!s.zfs_pools || s.zfs_pools.length === 0) return null
      return Math.max(...s.zfs_pools.map(p => p.cap_pct))
    }
    case 'pending_updates': return s.updates ? s.updates.length : 0
    case 'failed_units_count': return s.failed_units ? s.failed_units.length : 0
    case 'net_rx_errors': return s.net_rx_errors
    case 'net_tx_errors': return s.net_tx_errors
    case 'net_rx_dropped': return s.net_rx_dropped
    case 'net_tx_dropped': return s.net_tx_dropped
    default: return null
  }
}

function evalOperator(value: number, operator: string, threshold: number): boolean {
  switch (operator) {
    case '>': return value > threshold
    case '<': return value < threshold
    case '>=': return value >= threshold
    case '<=': return value <= threshold
    case '==': return value === threshold
    case '!=': return value !== threshold
    default: return false
  }
}

function formatMetricValue(value: number, metric: string): string {
  const meta = availableMetrics.find(m => m.value === metric)
  const unit = meta?.unit || ''
  return unit ? `${value.toFixed(1)}${unit}` : `${value}`
}

function generateAlerts(fleet: FleetServer[], rules: CustomAlertRule[]): Alert[] {
  const alerts: Alert[] = []
  for (const s of fleet) {
    // Critical: ZFS pool health != ONLINE
    if (s.zfs_pools) {
      for (const p of s.zfs_pools) {
        if (p.health !== 'ONLINE') {
          alerts.push({ key: `${s.name}-zfs-${p.name}`, server: s.name, type: `ZFS pool "${p.name}" ${p.health}`, value: p.health, severity: 'critical', category: 'fleet' })
        }
      }
    }
    // Critical: failed systemd units
    if (s.failed_units && s.failed_units.length > 0) {
      for (const u of s.failed_units) {
        alerts.push({ key: `${s.name}-unit-${u}`, server: s.name, type: `Failed unit: ${u}`, value: 'failed', severity: 'critical', category: 'fleet' })
      }
    }
    // Warning: CPU > 90%
    if (s.cpu_percent > 90) {
      alerts.push({ key: `${s.name}-cpu`, server: s.name, type: 'High CPU', value: `${s.cpu_percent.toFixed(1)}%`, severity: 'warning', category: 'fleet' })
    }
    // Memory: warning > 90%, critical > 96%
    if (s.mem_total > 0) {
      const memPct = (s.mem_used / s.mem_total) * 100
      if (memPct > 96) {
        alerts.push({ key: `${s.name}-mem`, server: s.name, type: 'High memory', value: `${memPct.toFixed(0)}%`, severity: 'critical', category: 'fleet' })
      } else if (memPct > 90) {
        alerts.push({ key: `${s.name}-mem`, server: s.name, type: 'High memory', value: `${memPct.toFixed(0)}%`, severity: 'warning', category: 'fleet' })
      }
    }
    // Warning: disk > 90%
    if (s.disk_total > 0 && (s.disk_used / s.disk_total) * 100 > 90) {
      alerts.push({ key: `${s.name}-disk`, server: s.name, type: 'High disk', value: `${((s.disk_used / s.disk_total) * 100).toFixed(0)}%`, severity: 'warning', category: 'fleet' })
    }
    // Warning: conntrack > 80%
    if (s.conntrack_count != null && s.conntrack_max != null && s.conntrack_max > 0 && (s.conntrack_count / s.conntrack_max) * 100 > 80) {
      alerts.push({ key: `${s.name}-conntrack`, server: s.name, type: 'High conntrack', value: `${((s.conntrack_count / s.conntrack_max) * 100).toFixed(0)}%`, severity: 'warning', category: 'network' })
    }
    // Warning: FD > 80%
    if (s.fd_max > 0 && (s.fd_allocated / s.fd_max) * 100 > 80) {
      alerts.push({ key: `${s.name}-fd`, server: s.name, type: 'High file descriptors', value: `${((s.fd_allocated / s.fd_max) * 100).toFixed(0)}%`, severity: 'warning', category: 'fleet' })
    }
    // Warning: network errors > 0
    if (s.net_rx_errors > 0 || s.net_tx_errors > 0) {
      alerts.push({ key: `${s.name}-neterr`, server: s.name, type: 'Network errors', value: `rx:${s.net_rx_errors} tx:${s.net_tx_errors}`, severity: 'warning', category: 'network', dismissable: true })
    }
    // Warning: network drops > 0
    if (s.net_rx_dropped > 0 || s.net_tx_dropped > 0) {
      alerts.push({ key: `${s.name}-netdrop`, server: s.name, type: 'Network drops', value: `rx:${s.net_rx_dropped} tx:${s.net_tx_dropped}`, severity: 'warning', category: 'network', dismissable: true })
    }
    // Warning: stale agent (last_seen > 2 min ago)
    if (Date.now() - new Date(s.last_seen).getTime() > 120_000) {
      alerts.push({ key: `${s.name}-stale`, server: s.name, type: 'Agent stale', value: 'last seen >2m ago', severity: 'warning', category: 'agent' })
    }

    // Custom alert rules
    for (const rule of rules) {
      if (!rule.enabled) continue
      const val = getMetricValue(s, rule.metric)
      if (val === null) continue
      if (evalOperator(val, rule.operator, rule.threshold)) {
        alerts.push({
          key: `${s.name}-custom-${rule.id}`,
          server: s.name,
          type: rule.name,
          value: formatMetricValue(val, rule.metric),
          severity: rule.severity,
          category: 'custom',
        })
      }
    }
  }
  return alerts
}

const alerts = computed(() => {
  // Access customRules.value here so Vue tracks it as a dependency.
  const rules = customRules.value
  return generateAlerts(filteredServers.value, rules)
})

const tabAlerts = computed(() => {
  if (activeTab.value === 'all') return alerts.value
  return alerts.value.filter(a => a.category === activeTab.value)
})

const criticalAlerts = computed(() => tabAlerts.value.filter(a => a.severity === 'critical'))
const warningAlerts = computed(() => tabAlerts.value.filter(a => a.severity === 'warning'))
const alertedServerCount = computed(() => new Set(alerts.value.map(a => a.server)).size)

const categoryLabels: Record<'all' | AlertCategory, string> = {
  all: 'All',
  network: 'Network',
  agent: 'Agent',
  fleet: 'Fleet',
  custom: 'Custom',
}

const tabCounts = computed(() => {
  const counts: Record<string, number> = { all: alerts.value.length, network: 0, agent: 0, fleet: 0, custom: 0 }
  for (const a of alerts.value) counts[a.category]++
  return counts
})

const fleetHealthPct = computed(() => {
  const total = filteredServers.value.length
  if (total === 0) return 100
  return ((total - alertedServerCount.value) / total) * 100
})

const fleetHealthClass = computed(() => {
  const pct = fleetHealthPct.value
  if (pct >= 90) return 'value-green'
  if (pct >= 70) return 'value-yellow'
  return 'value-red'
})

const failedUnitCount = computed(() => {
  let count = 0
  for (const s of filteredServers.value) {
    if (s.failed_units) count += s.failed_units.length
  }
  return count
})

const unhealthyPoolCount = computed(() => {
  let count = 0
  for (const s of filteredServers.value) {
    if (s.zfs_pools) {
      for (const p of s.zfs_pools) {
        if (p.health !== 'ONLINE') count++
      }
    }
  }
  return count
})

const netIssueServerCount = computed(() => {
  return filteredServers.value.filter(s => s.net_rx_errors > 0 || s.net_tx_errors > 0 || s.net_rx_dropped > 0 || s.net_tx_dropped > 0).length
})

const resourcePressureCount = computed(() => {
  return filteredServers.value.filter(s => {
    if (s.cpu_percent > 90) return true
    if (s.mem_total > 0 && (s.mem_used / s.mem_total) * 100 > 90) return true
    if (s.disk_total > 0 && (s.disk_used / s.disk_total) * 100 > 90) return true
    return false
  }).length
})

const staleAgentCount = computed(() => {
  return filteredServers.value.filter(s => Date.now() - new Date(s.last_seen).getTime() > 120_000).length
})

const customAlertCount = computed(() => alerts.value.filter(a => a.category === 'custom').length)
const customAlertCriticalCount = computed(() => alerts.value.filter(a => a.category === 'custom' && a.severity === 'critical').length)

async function dismissNetAlert(serverName: string) {
  try {
    await resetNetCounters(serverName)
    await load()
  } catch (e: any) {
    error.value = e.message || 'Failed to dismiss alert'
  }
}

function resetRuleForm() {
  editingRuleId.value = null
  newRule.name = ''
  newRule.metric = 'cpu_pct'
  newRule.operator = '>'
  newRule.threshold = 90
  newRule.severity = 'warning'
  newRule.enabled = true
}

function editRule(rule: CustomAlertRule) {
  editingRuleId.value = rule.id
  newRule.name = rule.name
  newRule.metric = rule.metric
  newRule.operator = rule.operator
  newRule.threshold = rule.threshold
  newRule.severity = rule.severity
  newRule.enabled = rule.enabled
  showRuleForm.value = true
}

async function saveRule() {
  try {
    if (editingRuleId.value != null) {
      await updateCustomAlert({ id: editingRuleId.value, ...newRule })
    } else {
      await createCustomAlert({ ...newRule })
    }
    customRules.value = await fetchCustomAlerts()
    showRuleForm.value = false
    resetRuleForm()
  } catch (e: any) {
    error.value = e.message || 'Failed to save alert rule'
  }
}

async function removeRule(id: number) {
  try {
    await deleteCustomAlert(id)
    customRules.value = await fetchCustomAlerts()
  } catch (e: any) {
    error.value = e.message || 'Failed to delete alert rule'
  }
}

async function load() {
  try {
    const [fleet, rules] = await Promise.all([fetchFleet(), fetchCustomAlerts()])
    servers.value = fleet
    customRules.value = rules
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

.header-right {
  display: flex;
  align-items: center;
  gap: 0.75rem;
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

.alert-count-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.75rem;
  font-weight: 500;
  color: var(--red-400);
  background: var(--red-subtle);
  padding: 0.375rem 0.75rem;
  border-radius: 4px;
  border: 1px solid rgba(248, 81, 73, 0.2);
}

.alert-count-short {
  display: none;
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

.alert-section {
  margin-bottom: 1.5rem;
}

.section-header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.section-count {
  font-size: 0.75rem;
  color: var(--text-color-muted);
}

.severity-badge {
  display: inline-flex;
  align-items: center;
  padding: 0.2rem 0.5rem;
  border-radius: 3px;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.badge-critical {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid rgba(248, 81, 73, 0.2);
}

.badge-warning {
  background: var(--yellow-subtle);
  color: var(--yellow-400);
  border: 1px solid rgba(210, 153, 34, 0.2);
}

.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
}

.alert-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.alert-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.alert-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
}

.alert-row {
  cursor: pointer;
  transition: background 0.15s;
}

.alert-row:hover {
  background: var(--surface-hover);
}

.alert-row:last-child td {
  border-bottom: none;
}

.col-server {
  font-weight: 600;
  color: var(--text-color);
}

.col-value {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.col-actions {
  width: 2.5rem;
  text-align: center;
}

.dismiss-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5rem;
  height: 1.5rem;
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  background: var(--surface-card);
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 0.6rem;
  transition: all 0.15s;
}

.dismiss-btn:hover {
  background: var(--surface-hover);
  color: var(--text-color);
  border-color: var(--text-color-muted);
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
  padding: 1.5rem 1.5rem;
  min-width: 0;
  text-align: center;
}

.summary-value {
  font-size: 1.75rem;
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

/* ── Manage rules button ── */
.manage-rules-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.375rem 0.625rem;
  font-size: 0.75rem;
  font-family: inherit;
  font-weight: 500;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color-muted);
  cursor: pointer;
  transition: all 0.15s;
}

.manage-rules-btn:hover {
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.manage-rules-btn.has-rules {
  color: var(--text-color-secondary);
}

.manage-rules-label {
  display: inline;
}

.rules-count-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 1.125rem;
  height: 1.125rem;
  padding: 0 0.3rem;
  font-size: 0.6rem;
  font-weight: 600;
  border-radius: 9rem;
  background: var(--primary-50);
  color: var(--primary-color);
}

/* ── Dialog ── */
.dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.dialog {
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 90%;
  max-width: 600px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}

.dialog-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1rem 1.25rem;
  border-bottom: 1px solid var(--surface-border);
}

.dialog-header-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.dialog-header h2 {
  font-size: 0.95rem;
  font-weight: 600;
  margin: 0;
}

.dialog-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.75rem;
  height: 1.75rem;
  background: none;
  border: 1px solid transparent;
  border-radius: 4px;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 0.8rem;
  transition: all 0.15s;
}

.dialog-close:hover {
  background: var(--surface-hover);
  color: var(--text-color);
}

.dialog-body {
  padding: 1.25rem;
  overflow-y: auto;
  max-height: calc(80vh - 4rem);
}

.form-actions {
  display: flex;
  gap: 0.5rem;
}

.cancel-rule-btn {
  padding: 0.5rem 1rem;
  font-size: 0.8rem;
  font-family: inherit;
  font-weight: 500;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.cancel-rule-btn:hover {
  border-color: var(--text-color-muted);
  color: var(--text-color);
}

.add-rule-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.375rem 0.75rem;
  font-size: 0.75rem;
  font-family: inherit;
  font-weight: 500;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.add-rule-btn:hover {
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.rule-form {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 1rem;
  margin-bottom: 1.25rem;
}

.form-row {
  margin-bottom: 0.75rem;
}

.form-row label {
  display: block;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-muted);
  margin-bottom: 0.25rem;
}

.form-row-inline {
  display: flex;
  gap: 0.75rem;
}

.form-field {
  flex: 1;
}

.form-field-sm {
  flex: 0 0 auto;
  min-width: 7rem;
}

.form-input {
  width: 100%;
  padding: 0.5rem 0.625rem;
  font-size: 0.8rem;
  font-family: inherit;
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  color: var(--text-color);
  outline: none;
  transition: border-color 0.15s;
  box-sizing: border-box;
}

.form-input:focus {
  border-color: var(--primary-color);
}

.save-rule-btn {
  padding: 0.5rem 1rem;
  font-size: 0.8rem;
  font-family: inherit;
  font-weight: 500;
  background: var(--primary-color);
  border: none;
  border-radius: 4px;
  color: #fff;
  cursor: pointer;
  transition: opacity 0.15s;
}

.save-rule-btn:hover {
  opacity: 0.9;
}

.save-rule-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.rules-list {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  margin-bottom: 1rem;
}

.rule-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.625rem 0.75rem;
}

.rule-item.rule-disabled {
  opacity: 0.5;
}

.rule-info {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.rule-name {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--text-color);
}

.rule-expr {
  font-size: 0.75rem;
  font-family: 'JetBrains Mono', monospace;
  color: var(--text-color-muted);
}

.rule-actions {
  display: flex;
  gap: 0.25rem;
}

.empty-state-sm {
  text-align: center;
  padding: 2rem 0;
  color: var(--text-color-muted);
  font-size: 0.8rem;
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

  .alert-count-full {
    display: none;
  }

  .alert-count-short {
    display: inline;
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

  .mobile-card-critical {
    border-left: 3px solid var(--red-400);
  }

  .mobile-card-warning {
    border-left: 3px solid var(--yellow-400);
  }

  .mobile-card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.375rem;
  }

  .mobile-card-body {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .mobile-card-type {
    font-size: 0.8rem;
    color: var(--text-color-secondary);
  }

  .form-row-inline {
    flex-direction: column;
  }

  .form-field-sm {
    min-width: 0;
  }

  .rule-info {
    flex-wrap: wrap;
    gap: 0.375rem;
  }
}
</style>

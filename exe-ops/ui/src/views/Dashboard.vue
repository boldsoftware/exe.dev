<template>
  <div class="dashboard">
    <div class="page-header">
      <div>
        <h1>Dashboard</h1>
        <p class="page-subtitle">Deploy overview</p>
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
      <!-- Summary Stats -->
      <div class="stats-row">
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--primary-50); color: var(--primary-color);">
            <i class="pi pi-server"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ totalProcesses }}</span>
            <span class="stat-label">Processes</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--green-subtle); color: var(--green-400);">
            <i class="pi pi-check-circle"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ upToDateCount }}</span>
            <span class="stat-label">Up to Date</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--yellow-subtle); color: var(--yellow-400);">
            <i class="pi pi-arrow-down"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ behindCount }}</span>
            <span class="stat-label">Behind HEAD</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon" style="background: var(--primary-50); color: var(--primary-color);">
            <i class="pi pi-clock"></i>
          </div>
          <div class="stat-body">
            <span class="stat-value">{{ recentDeployCount }}</span>
            <span class="stat-label">Deploys (24h)</span>
          </div>
        </div>
      </div>

      <!-- HEAD info -->
      <div v-if="inventory" class="head-info">
        <span class="head-label">HEAD</span>
        <code class="head-sha">{{ inventory.head_sha.slice(0, 7) }}</code>
        <span class="head-subject">{{ inventory.head_subject }}</span>
        <span class="head-date">{{ formatRelative(inventory.head_date) }}</span>
      </div>

      <!-- Recent Deploys -->
      <div class="section">
        <div class="section-header">
          <h2 class="section-title">Recent Deploys</h2>
          <router-link to="/deploy" class="view-all-link">View all <i class="pi pi-arrow-right"></i></router-link>
        </div>
        <div v-if="deploys.length === 0" class="empty-state">
          <i class="pi pi-upload"></i>
          <p>No deploys in the last 24 hours</p>
        </div>
        <div v-else class="deploys-list">
          <div
            v-for="d in deploys.slice(0, 10)"
            :key="d.id"
            class="deploy-row"
            :class="'deploy-state-' + d.state"
          >
            <span class="deploy-indicator"></span>
            <span class="deploy-process">{{ d.process }}</span>
            <i class="pi pi-arrow-right deploy-arrow"></i>
            <span class="deploy-host">{{ d.host }}</span>
            <code class="deploy-sha">{{ d.sha.slice(0, 7) }}</code>
            <span class="deploy-state">{{ d.state }}</span>
            <span class="deploy-time">{{ formatRelative(d.started_at) }}</span>
            <span v-if="d.initiated_by" class="deploy-user">{{ d.initiated_by }}</span>
          </div>
        </div>
      </div>

      <!-- Process Summary by Stage -->
      <div v-if="inventory" class="section">
        <div class="section-header">
          <h2 class="section-title">Fleet Inventory</h2>
          <router-link to="/deploy" class="view-all-link">Details <i class="pi pi-arrow-right"></i></router-link>
        </div>
        <div class="inventory-grid">
          <div v-for="[stage, roles] in stageRoleSummary" :key="stage" class="inventory-card">
            <h3 class="inventory-stage">{{ stage }}</h3>
            <div v-for="[role, count] in roles" :key="role" class="inventory-role">
              <span class="role-name">{{ role }}</span>
              <span class="role-count">{{ count }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { fetchDeployInventory, fetchDeploys, type DeployInventory, type DeployStatus } from '../api/client'

const loading = ref(true)
const error = ref('')
const inventory = ref<DeployInventory | null>(null)
const deploys = ref<DeployStatus[]>([])

onMounted(async () => {
  try {
    const [inv, dep] = await Promise.all([
      fetchDeployInventory(),
      fetchDeploys('24h'),
    ])
    inventory.value = inv
    deploys.value = dep
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
})

const totalProcesses = computed(() => inventory.value?.processes.length ?? 0)
const upToDateCount = computed(() =>
  inventory.value?.processes.filter(p => p.commits_behind === 0).length ?? 0
)
const behindCount = computed(() =>
  inventory.value?.processes.filter(p => p.commits_behind > 0).length ?? 0
)
const recentDeployCount = computed(() => deploys.value.length)

const stageRoleSummary = computed(() => {
  if (!inventory.value) return []
  const map = new Map<string, Map<string, number>>()
  for (const p of inventory.value.processes) {
    if (!map.has(p.stage)) map.set(p.stage, new Map())
    const roles = map.get(p.stage)!
    roles.set(p.role, (roles.get(p.role) ?? 0) + 1)
  }
  return Array.from(map.entries()).map(([stage, roles]) => [stage, Array.from(roles.entries())] as const)
})

function formatRelative(dateStr: string): string {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - d.getTime()
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  return `${diffDay}d ago`
}
</script>

<style scoped>
.dashboard {
  max-width: 1200px;
}

.page-header {
  margin-bottom: 1.5rem;
}

.page-header h1 {
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--text-color);
}

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-secondary);
  margin-top: 0.25rem;
}

.message-banner {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  border-radius: 6px;
  font-size: 0.8rem;
  margin-bottom: 1rem;
}

.message-error {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid rgba(248, 81, 73, 0.2);
}

.loading-state {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 2rem;
  color: var(--text-color-secondary);
  font-size: 0.85rem;
}

/* Stats */
.stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.stat-card {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 1rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
}

.stat-icon {
  width: 40px;
  height: 40px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1rem;
  flex-shrink: 0;
}

.stat-body {
  display: flex;
  flex-direction: column;
}

.stat-value {
  font-size: 1.25rem;
  font-weight: 600;
  line-height: 1.2;
}

.stat-label {
  font-size: 0.7rem;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

/* HEAD info */
.head-info {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  font-size: 0.8rem;
  margin-bottom: 1.5rem;
}

.head-label {
  font-weight: 600;
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--primary-color);
  background: var(--primary-50);
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
}

.head-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  color: var(--primary-color);
}

.head-subject {
  color: var(--text-color);
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.head-date {
  color: var(--text-color-muted);
  font-size: 0.75rem;
  flex-shrink: 0;
}

/* Sections */
.section {
  margin-bottom: 1.5rem;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.75rem;
}

.section-title {
  font-size: 0.9rem;
  font-weight: 600;
}

.view-all-link {
  font-size: 0.75rem;
  color: var(--primary-color);
  display: flex;
  align-items: center;
  gap: 0.25rem;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
  padding: 2rem;
  color: var(--text-color-muted);
  font-size: 0.85rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
}

.empty-state i {
  font-size: 1.5rem;
}

/* Deploy list */
.deploys-list {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  overflow: hidden;
}

.deploy-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.6rem 1rem;
  font-size: 0.8rem;
  border-bottom: 1px solid var(--surface-border);
}

.deploy-row:last-child {
  border-bottom: none;
}

.deploy-indicator {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.deploy-state-done .deploy-indicator { background: var(--green-400); }
.deploy-state-running .deploy-indicator { background: var(--yellow-400); }
.deploy-state-failed .deploy-indicator { background: var(--red-400); }
.deploy-state-pending .deploy-indicator { background: var(--text-color-muted); }

.deploy-process {
  font-weight: 500;
  min-width: 80px;
}

.deploy-arrow {
  font-size: 0.6rem;
  color: var(--text-color-muted);
}

.deploy-host {
  color: var(--text-color-secondary);
  min-width: 140px;
}

.deploy-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  color: var(--primary-color);
}

.deploy-state {
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  min-width: 60px;
}

.deploy-state-done .deploy-state { color: var(--green-400); }
.deploy-state-running .deploy-state { color: var(--yellow-400); }
.deploy-state-failed .deploy-state { color: var(--red-400); }
.deploy-state-pending .deploy-state { color: var(--text-color-muted); }

.deploy-time {
  color: var(--text-color-muted);
  font-size: 0.75rem;
  margin-left: auto;
}

.deploy-user {
  color: var(--text-color-muted);
  font-size: 0.7rem;
}

/* Inventory grid */
.inventory-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 0.75rem;
}

.inventory-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 1rem;
}

.inventory-stage {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--primary-color);
  margin-bottom: 0.75rem;
}

.inventory-role {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.3rem 0;
  font-size: 0.8rem;
}

.role-name {
  color: var(--text-color-secondary);
}

.role-count {
  font-weight: 600;
  color: var(--text-color);
}
</style>

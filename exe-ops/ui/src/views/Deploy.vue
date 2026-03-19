<template>
  <div class="deploy-view">
    <div class="page-header">
      <div>
        <h1>Deploy</h1>
        <p class="page-subtitle">Fleet deployment inventory and version status</p>
      </div>
      <div v-if="headSHA" class="head-sha-badge">
        <span class="head-sha-label">origin/main</span>
        <a
          :href="'https://github.com/boldsoftware/exe/commit/' + headSHA"
          target="_blank"
          rel="noopener"
          class="head-sha-value"
        >{{ headSHA.slice(0, 7) }}</a>
        <span v-if="headSubject" class="head-sha-subject">{{ headSubject }}</span>
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
      <!-- Active / recent deploys -->
      <div v-if="deploys.length > 0" class="deploys-section">
        <h2 class="section-title">Deploys</h2>
        <div class="deploys-list">
          <div
            v-for="d in deploys"
            :key="d.id"
            class="deploy-card"
            :class="'deploy-state-' + d.state"
          >
            <div class="deploy-card-header">
              <span class="deploy-card-target">
                <span class="deploy-card-process">{{ d.process }}</span>
                <i class="pi pi-arrow-right deploy-card-arrow"></i>
                <span class="deploy-card-host">{{ d.host }}</span>
              </span>
              <span class="deploy-card-sha">{{ d.sha.slice(0, 7) }}</span>
              <span class="deploy-card-state" :class="'state-' + d.state">{{ d.state }}</span>
            </div>
            <div class="deploy-steps">
              <span
                v-for="step in d.steps"
                :key="step.name"
                class="deploy-step"
                :class="'step-' + step.status"
                :title="step.name + ': ' + step.status + (step.output ? ' — ' + step.output : '')"
              >
                <i v-if="step.status === 'running'" class="pi pi-spin pi-spinner step-icon"></i>
                <i v-else-if="step.status === 'done'" class="pi pi-check step-icon"></i>
                <i v-else-if="step.status === 'failed'" class="pi pi-times step-icon"></i>
                <span v-else class="step-dot"></span>
                <span class="step-label">{{ step.name }}</span>
              </span>
            </div>
            <div v-if="stepsWithOutput(d).length > 0" class="deploy-step-outputs">
              <div v-for="step in stepsWithOutput(d)" :key="step.name" class="deploy-step-output">
                <span class="step-output-name">{{ step.name }}</span>
                <span class="step-output-text">{{ step.output }}</span>
              </div>
            </div>
            <div v-if="d.error" class="deploy-card-error">{{ d.error }}</div>
          </div>
        </div>
      </div>

      <!-- Summary: COUNT(*) GROUP BY stage, process, version -->
      <div v-if="summaryRows.length > 0" class="table-wrapper summary-table-wrapper">
        <table class="summary-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Process</th>
              <th>Count</th>
              <th>Version</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in summaryRows" :key="i">
              <td>{{ row.stage }}</td>
              <td>{{ row.process }}</td>
              <td class="col-count">{{ row.count }}</td>
              <td class="col-summary-version">
                <span v-if="row.version" class="version-cell">
                  <a
                    v-if="row.versionURL"
                    :href="row.versionURL"
                    target="_blank"
                    rel="noopener"
                    class="version-sha"
                    @click.stop
                  >{{ row.version.slice(0, 7) }}</a>
                  <span v-else class="version-sha">{{ row.version.slice(0, 7) }}</span>
                  <span v-if="row.commitsBehind > 0" class="behind-badge">{{ row.commitsBehind }}<i class="pi pi-arrow-down"></i></span>
                  <span v-if="row.subject" class="version-subject">{{ row.subject }}</span>
                  <span v-if="row.date" class="version-date">{{ formatDate(row.date) }}</span>
                </span>
                <span v-else class="metric-blank">&mdash;</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Toolbar: filters + search on one line -->
      <div class="toolbar-row">
        <div class="filter-group" v-if="uniqueStages.length > 0">
          <span class="filter-label">Stage</span>
          <div class="filter-buttons">
            <button
              v-for="s in uniqueStages"
              :key="'stage-' + s"
              class="filter-btn"
              :class="{ active: activeStages.has(s) }"
              @click="toggleStageFilter(s)"
            >{{ s }}</button>
          </div>
        </div>
        <div class="filter-group" v-if="uniqueRoles.length > 0">
          <span class="filter-label">Role</span>
          <div class="filter-buttons">
            <button
              v-for="r in uniqueRoles"
              :key="'role-' + r"
              class="filter-btn"
              :class="{ active: activeRoles.has(r) }"
              @click="toggleRoleFilter(r)"
            >{{ r }}</button>
          </div>
        </div>
        <div class="filter-group" v-if="uniqueProcesses.length > 0">
          <span class="filter-label">Process</span>
          <div class="filter-buttons">
            <button
              v-for="p in uniqueProcesses"
              :key="'proc-' + p"
              class="filter-btn"
              :class="{ active: activeProcesses.has(p) }"
              @click="toggleProcessFilter(p)"
            >{{ p }}</button>
          </div>
        </div>
        <div class="search-box">
          <i class="pi pi-search"></i>
          <input
            v-model="search"
            type="text"
            placeholder="Search hostnames..."
            class="search-input"
          />
          <button v-if="search" class="search-clear" @click="search = ''">
            <i class="pi pi-times"></i>
          </button>
        </div>
      </div>

      <div v-if="filteredProcs.length === 0" class="empty-state">
        {{ search ? 'No processes match your search.' : 'No processes found.' }}
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table class="deploy-table">
            <thead>
              <tr>
                <th class="sortable" @click="toggleSort('hostname')">
                  Hostname
                  <i v-if="sortCol === 'hostname'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
                </th>
                <th>Role</th>
                <th>Stage</th>
                <th>Region</th>
                <th>Process</th>
                <th class="sortable" @click="toggleSort('version')">
                  Version
                  <i v-if="sortCol === 'version'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
                </th>
                <th class="sortable" @click="toggleSort('uptime')">
                  Uptime
                  <i v-if="sortCol === 'uptime'" class="pi" :class="sortDir === 'asc' ? 'pi-sort-amount-up-alt' : 'pi-sort-amount-down'"></i>
                </th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="p in filteredProcs"
                :key="p.hostname + ':' + p.process"
                class="deploy-row"
              >
                <td class="col-hostname">{{ p.hostname }}</td>
                <td class="col-role">
                  <span class="role-tag" :class="'role-' + p.role">{{ p.role }}</span>
                </td>
                <td class="col-stage">{{ p.stage }}</td>
                <td class="col-region">{{ p.region || '\u2014' }}</td>
                <td class="col-process">
                  <a :href="p.debug_url" target="_blank" rel="noopener" class="process-link" @click.stop>{{ p.process }}</a>
                </td>
                <td class="col-version">
                  <span v-if="p.version" class="version-cell">
                    <span class="version-indicator" :class="{ 'version-mismatch': isMismatch(p) }"></span>
                    <a
                      v-if="p.version_url"
                      :href="p.version_url"
                      target="_blank"
                      rel="noopener"
                      class="version-sha"
                      @click.stop
                    >{{ p.version.slice(0, 7) }}</a>
                    <span v-else class="version-sha">{{ p.version.slice(0, 7) }}</span>
                    <span v-if="p.commits_behind > 0" class="behind-badge">{{ p.commits_behind }}<i class="pi pi-arrow-down"></i></span>
                    <span v-if="p.version_subject" class="version-subject">{{ p.version_subject }}</span>
                    <span v-if="p.version_date" class="version-date">{{ formatDate(p.version_date) }}</span>
                  </span>
                  <span v-else class="metric-blank">&mdash;</span>
                </td>
                <td class="col-uptime">
                  <span v-if="p.uptime_secs" class="uptime-text">{{ humanizeUptime(p.uptime_secs) }}</span>
                  <span v-else class="metric-blank">&mdash;</span>
                </td>
                <td class="col-actions">
                  <button
                    v-if="isDeployable(p)"
                    class="deploy-btn"
                    :class="{ deploying: isDeploying(p) }"
                    :disabled="!canDeploy(p)"
                    :title="deployTitle(p)"
                    @click="doDeploy(p)"
                  >
                    <i v-if="isDeploying(p)" class="pi pi-spin pi-spinner"></i>
                    <i v-else class="pi pi-upload"></i>
                    Deploy
                    <span v-if="canDeploy(p) && headSHA" class="deploy-btn-sha">{{ headSHA.slice(0, 7) }}</span>
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch, onMounted, onUnmounted } from 'vue'
import {
  fetchDeployInventory,
  fetchDeploys,
  startDeploy,
  type DeployProcess,
  type DeployStatus,
} from '../api/client'

const deployableProcesses = new Set(['exeletd', 'exeprox', 'exed', 'cgtop'])

const procs = ref<DeployProcess[]>([])
const headSHA = ref('')
const headSubject = ref('')
const deploys = ref<DeployStatus[]>([])
const loading = ref(true)
const error = ref('')
const sortCol = ref<'hostname' | 'version' | 'uptime'>('hostname')
const sortDir = ref<'asc' | 'desc'>('asc')
const search = ref('')
const activeStages = reactive(new Set<string>())
const activeRoles = reactive(new Set<string>())
const activeProcesses = reactive(new Set<string>())
let pollTimer: ReturnType<typeof setInterval> | null = null
let deployPollTimer: ReturnType<typeof setInterval> | null = null

try {
  const savedStages = sessionStorage.getItem('exe-ops-deploy-stage-filter')
  if (savedStages) for (const s of JSON.parse(savedStages)) activeStages.add(s)
  const savedRoles = sessionStorage.getItem('exe-ops-deploy-role-filter')
  if (savedRoles) for (const r of JSON.parse(savedRoles)) activeRoles.add(r)
  const savedProcs = sessionStorage.getItem('exe-ops-deploy-process-filter')
  if (savedProcs) for (const p of JSON.parse(savedProcs)) activeProcesses.add(p)
} catch {}

watch(activeStages, () => {
  if (activeStages.size > 0) {
    sessionStorage.setItem('exe-ops-deploy-stage-filter', JSON.stringify([...activeStages]))
  } else {
    sessionStorage.removeItem('exe-ops-deploy-stage-filter')
  }
})

watch(activeRoles, () => {
  if (activeRoles.size > 0) {
    sessionStorage.setItem('exe-ops-deploy-role-filter', JSON.stringify([...activeRoles]))
  } else {
    sessionStorage.removeItem('exe-ops-deploy-role-filter')
  }
})

watch(activeProcesses, () => {
  if (activeProcesses.size > 0) {
    sessionStorage.setItem('exe-ops-deploy-process-filter', JSON.stringify([...activeProcesses]))
  } else {
    sessionStorage.removeItem('exe-ops-deploy-process-filter')
  }
})

function toggleStageFilter(value: string) {
  if (activeStages.has(value)) activeStages.delete(value)
  else activeStages.add(value)
}

function toggleRoleFilter(value: string) {
  if (activeRoles.has(value)) activeRoles.delete(value)
  else activeRoles.add(value)
}

function toggleProcessFilter(value: string) {
  if (activeProcesses.has(value)) activeProcesses.delete(value)
  else activeProcesses.add(value)
}

const uniqueStages = computed(() =>
  [...new Set(procs.value.map(p => p.stage).filter(Boolean))].sort()
)

const uniqueRoles = computed(() =>
  [...new Set(procs.value.map(p => p.role).filter(Boolean))].sort()
)

const uniqueProcesses = computed(() =>
  [...new Set(procs.value.map(p => p.process).filter(Boolean))].sort()
)

const baseFilteredProcs = computed(() => {
  return procs.value.filter(p => {
    if (activeStages.size > 0 && !activeStages.has(p.stage)) return false
    if (activeRoles.size > 0 && !activeRoles.has(p.role)) return false
    if (activeProcesses.size > 0 && !activeProcesses.has(p.process)) return false
    return true
  })
})

const filteredProcs = computed(() => {
  let list = [...baseFilteredProcs.value]
  if (search.value) {
    const q = search.value.toLowerCase()
    list = list.filter(p => p.hostname.toLowerCase().includes(q))
  }
  const dir = sortDir.value === 'asc' ? 1 : -1
  if (sortCol.value === 'hostname') {
    list.sort((a, b) => {
      const c = dir * a.hostname.localeCompare(b.hostname)
      return c !== 0 ? c : a.process.localeCompare(b.process)
    })
  } else if (sortCol.value === 'uptime') {
    list.sort((a, b) => dir * ((a.uptime_secs || 0) - (b.uptime_secs || 0)))
  } else {
    list.sort((a, b) => dir * a.version.localeCompare(b.version))
  }
  return list
})

function formatDate(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  const mon = d.toLocaleString('en', { month: 'short' })
  return `${mon} ${d.getDate()}`
}

function humanizeUptime(secs: number): string {
  if (secs <= 0) return ''
  const days = Math.floor(secs / 86400)
  const hours = Math.floor((secs % 86400) / 3600)
  const mins = Math.floor((secs % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${mins}m`
  return `${mins}m`
}

function toggleSort(col: 'hostname' | 'version' | 'uptime') {
  if (sortCol.value === col) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortCol.value = col
    sortDir.value = 'asc'
  }
}

// Summary: GROUP BY stage, process, version
interface SummaryRow {
  stage: string
  process: string
  version: string
  subject: string
  date: string
  versionURL: string
  commitsBehind: number
  count: number
}

const summaryRows = computed(() => {
  const key = (s: string, p: string, v: string) => `${s}\0${p}\0${v}`
  const groups = new Map<string, SummaryRow>()
  for (const p of baseFilteredProcs.value) {
    const k = key(p.stage, p.process, p.version)
    const existing = groups.get(k)
    if (existing) {
      existing.count++
    } else {
      groups.set(k, {
        stage: p.stage,
        process: p.process,
        version: p.version,
        subject: p.version_subject || '',
        date: p.version_date || '',
        versionURL: p.version_url || '',
        commitsBehind: p.commits_behind ?? -1,
        count: 1,
      })
    }
  }
  const rows = [...groups.values()]
  rows.sort((a, b) => {
    let c = a.stage.localeCompare(b.stage)
    if (c !== 0) return c
    c = a.process.localeCompare(b.process)
    if (c !== 0) return c
    return b.count - a.count // most common version first
  })
  return rows
})

// Compute the most common version per process name for mismatch detection
const modeVersionByProcess = computed(() => {
  const procVersions = new Map<string, Map<string, number>>()
  for (const p of baseFilteredProcs.value) {
    if (!p.version) continue
    if (!procVersions.has(p.process)) procVersions.set(p.process, new Map())
    const vc = procVersions.get(p.process)!
    vc.set(p.version, (vc.get(p.version) || 0) + 1)
  }
  const result = new Map<string, string>()
  for (const [proc, vc] of procVersions) {
    let maxCount = 0
    let modeVersion = ''
    for (const [version, count] of vc) {
      if (count > maxCount) {
        maxCount = count
        modeVersion = version
      }
    }
    result.set(proc, modeVersion)
  }
  return result
})

function stepsWithOutput(d: DeployStatus): { name: string; output: string }[] {
  return d.steps.filter(s => s.output && s.status !== 'failed')
}

function isMismatch(p: DeployProcess): boolean {
  if (!p.version) return false
  const mode = modeVersionByProcess.value.get(p.process)
  return !!mode && mode !== p.version
}

// Deploy helpers
const activeDeployKeys = computed(() => {
  const keys = new Set<string>()
  for (const d of deploys.value) {
    if (d.state === 'running' || d.state === 'pending') {
      keys.add(deployKey(d.stage, d.role, d.process, d.host))
    }
  }
  return keys
})

const hasActiveDeploys = computed(() => activeDeployKeys.value.size > 0)

function deployKey(stage: string, role: string, process: string, host: string): string {
  return `${stage}/${role}/${process}/${host}`
}

function isDeployable(p: DeployProcess): boolean {
  return deployableProcesses.has(p.process)
}

function isDeploying(p: DeployProcess): boolean {
  return activeDeployKeys.value.has(deployKey(p.stage, p.role, p.process, p.hostname))
}

function canDeploy(p: DeployProcess): boolean {
  if (p.stage !== 'staging') return false
  if (isDeploying(p)) return false
  if (!headSHA.value) return false
  return true
}

function deployTitle(p: DeployProcess): string {
  if (p.stage !== 'staging') return 'Only staging deploys are allowed'
  if (isDeploying(p)) return 'Deploy in progress'
  if (!headSHA.value) return 'HEAD SHA unknown'
  if (p.version === headSHA.value) return `Already at HEAD (${headSHA.value.slice(0, 7)})`
  return `Deploy ${headSHA.value.slice(0, 7)} to ${p.hostname}`
}

async function doDeploy(p: DeployProcess) {
  if (!canDeploy(p)) return
  try {
    await startDeploy({
      stage: p.stage,
      role: p.role,
      process: p.process,
      host: p.hostname,
      dns_name: p.dns_name,
      sha: headSHA.value,
    })
    await loadDeploys()
  } catch (e: any) {
    error.value = e.message || 'Deploy failed'
  }
}

async function load() {
  try {
    const inv = await fetchDeployInventory()
    procs.value = inv.processes
    headSHA.value = inv.head_sha
    headSubject.value = inv.head_subject
    error.value = ''
  } catch (e: any) {
    error.value = e.message || 'Failed to load deploy inventory'
  } finally {
    loading.value = false
  }
}

async function loadDeploys() {
  try {
    deploys.value = await fetchDeploys()
  } catch {
    // ignore — deploys section just won't show
  }
}

// Poll deploys faster when there are active ones
function startDeployPolling() {
  stopDeployPolling()
  const interval = hasActiveDeploys.value ? 2000 : 10000
  deployPollTimer = setInterval(async () => {
    await loadDeploys()
    // Adjust poll rate if active deploys state changed
    const newInterval = hasActiveDeploys.value ? 2000 : 10000
    if (newInterval !== interval) {
      startDeployPolling()
    }
  }, interval)
}

function stopDeployPolling() {
  if (deployPollTimer) {
    clearInterval(deployPollTimer)
    deployPollTimer = null
  }
}

watch(hasActiveDeploys, () => {
  startDeployPolling()
})

onMounted(async () => {
  await Promise.all([load(), loadDeploys()])
  pollTimer = setInterval(load, 30000)
  startDeployPolling()
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  stopDeployPolling()
})
</script>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 1rem;
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

.head-sha-badge {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.375rem 0.75rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-size: 0.75rem;
}

.head-sha-label {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  font-weight: 600;
  color: var(--text-color-muted);
}

.head-sha-value {
  font-family: 'JetBrains Mono', monospace;
  font-weight: 600;
  color: var(--primary-color);
}

.head-sha-value:hover {
  text-decoration: underline;
}

.head-sha-subject {
  color: var(--text-color-secondary);
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* -- Deploys section -- */
.deploys-section {
  margin-bottom: 1.5rem;
}

.section-title {
  font-size: 0.7rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  margin-bottom: 0.5rem;
}

.deploys-list {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.deploy-card {
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  background: var(--surface-card);
  font-size: 0.8rem;
}

.deploy-card.deploy-state-running {
  border-color: var(--primary-color);
  border-left: 3px solid var(--primary-color);
}

.deploy-card.deploy-state-done {
  border-color: var(--green-400);
  border-left: 3px solid var(--green-400);
}

.deploy-card.deploy-state-failed {
  border-color: var(--red-400);
  border-left: 3px solid var(--red-400);
}

.deploy-card-header {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.375rem;
}

.deploy-card-target {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  font-weight: 600;
  color: var(--text-color);
}

.deploy-card-arrow {
  font-size: 0.55rem;
  color: var(--text-color-muted);
}

.deploy-card-process {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.deploy-card-host {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
}

.deploy-card-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  color: var(--primary-color);
}

.deploy-card-state {
  margin-left: auto;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
}

.state-pending,
.state-running {
  background: var(--primary-50);
  color: var(--primary-color);
}

.state-done {
  background: var(--green-subtle);
  color: var(--green-400);
}

.state-failed {
  background: var(--red-subtle);
  color: var(--red-400);
}

.deploy-steps {
  display: flex;
  gap: 0.75rem;
}

.deploy-step {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  font-size: 0.7rem;
  color: var(--text-color-muted);
}

.deploy-step.step-running {
  color: var(--primary-color);
  font-weight: 600;
}

.deploy-step.step-done {
  color: var(--green-400);
}

.deploy-step.step-failed {
  color: var(--red-400);
}

.step-icon {
  font-size: 0.6rem;
}

.step-dot {
  display: inline-block;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--surface-border-bright);
}

.step-label {
  font-size: 0.65rem;
}

.deploy-step-outputs {
  margin-top: 0.375rem;
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
}

.deploy-step-output {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  font-size: 0.65rem;
  color: var(--text-color-muted);
}

.step-output-name {
  font-weight: 600;
  min-width: 3.5rem;
  color: var(--text-color-secondary);
}

.step-output-text {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.6rem;
}

.deploy-card-error {
  margin-top: 0.375rem;
  font-size: 0.7rem;
  color: var(--red-400);
  white-space: pre-wrap;
  word-break: break-all;
}

/* -- Summary table -- */
.summary-table-wrapper {
  margin-bottom: 1.5rem;
}

.summary-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.summary-table th {
  text-align: left;
  padding: 0.5rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.summary-table td {
  padding: 0.375rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
  white-space: nowrap;
}

.summary-table tbody tr:last-child td {
  border-bottom: none;
}

.summary-table .col-count {
  font-weight: 600;
  color: var(--text-color);
  text-align: right;
  width: 60px;
}

.summary-table .col-summary-version {
  white-space: nowrap;
}

/* -- Toolbar: filters + search on one line -- */
.toolbar-row {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin-bottom: 1rem;
  flex-wrap: wrap;
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

.search-box {
  position: relative;
  min-width: 180px;
  max-width: 280px;
  margin-left: auto;
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
  padding: 0.4rem 2rem 0.4rem 2.25rem;
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

/* -- Detail table -- */
.table-wrapper {
  overflow-x: auto;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  background: var(--surface-card);
}

.deploy-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8rem;
}

.deploy-table th {
  text-align: left;
  padding: 0.625rem 1rem;
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
  white-space: nowrap;
}

.deploy-table th.sortable {
  cursor: pointer;
  user-select: none;
  transition: color 0.15s;
}

.deploy-table th.sortable:hover {
  color: var(--text-color);
}

.deploy-table th.sortable .pi {
  font-size: 0.6rem;
  margin-left: 0.25rem;
  vertical-align: middle;
}

.deploy-table td {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--surface-border);
  color: var(--text-color-secondary);
  vertical-align: middle;
  white-space: nowrap;
}

.deploy-table .col-version {
  max-width: 500px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.deploy-row {
  transition: background 0.15s;
}

.deploy-row:hover {
  background: var(--surface-hover);
}

.deploy-row:last-child td {
  border-bottom: none;
}

.col-hostname {
  font-weight: 600;
  color: var(--text-color);
}

.role-tag {
  display: inline-flex;
  padding: 0.15rem 0.4rem;
  border-radius: 3px;
  font-size: 0.65rem;
  font-weight: 600;
  font-family: 'JetBrains Mono', monospace;
  white-space: nowrap;
}

.role-exelet {
  background: var(--green-subtle);
  color: var(--green-400);
}

.role-exeprox {
  background: var(--primary-50);
  color: var(--primary-color);
}

.role-exed {
  background: var(--yellow-subtle);
  color: var(--yellow-400);
}

.process-link {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  color: var(--primary-color);
}

.process-link:hover {
  color: var(--primary-hover);
  text-decoration: underline;
}

.version-cell {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
}

.version-indicator {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--green-400);
  flex-shrink: 0;
}

.version-indicator.version-mismatch {
  background: var(--yellow-400);
}

.version-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  color: var(--primary-color);
}

a.version-sha:hover {
  color: var(--primary-hover);
  text-decoration: underline;
}

.version-subject {
  font-size: 0.7rem;
  color: var(--text-color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
}

.version-date {
  font-size: 0.65rem;
  color: var(--text-color-muted);
  opacity: 0.7;
}

.behind-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.15rem;
  padding: 0.1rem 0.35rem;
  border-radius: 3px;
  font-size: 0.6rem;
  font-weight: 600;
  background: var(--red-subtle);
  color: var(--red-400);
  white-space: nowrap;
}

.behind-badge .pi {
  font-size: 0.55rem;
}

.uptime-text {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  color: var(--text-color-secondary);
}

.col-actions {
  width: 1px;
  white-space: nowrap;
}

.deploy-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.25rem 0.5rem;
  font-size: 0.7rem;
  font-family: inherit;
  font-weight: 500;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.deploy-btn .pi {
  font-size: 0.6rem;
}

.deploy-btn:hover:not(:disabled) {
  border-color: var(--primary-color);
  color: var(--primary-color);
  background: var(--primary-50);
}

.deploy-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.deploy-btn.deploying {
  border-color: var(--primary-color);
  color: var(--primary-color);
  opacity: 0.8;
}

.deploy-btn-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.6rem;
  opacity: 0.7;
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

@media (max-width: 768px) {
  .toolbar-row {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
  }

  .search-box {
    margin-left: 0;
    max-width: 100%;
    width: 100%;
  }

  .filter-buttons {
    flex-wrap: wrap;
  }

  .page-header {
    flex-wrap: wrap;
    gap: 0.5rem;
  }
}
</style>

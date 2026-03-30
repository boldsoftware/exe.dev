<template>
  <div class="deploy-view">
    <div class="page-header">
      <div>
        <h1>Deploy</h1>
        <p class="page-subtitle">Fleet deployment inventory and version status</p>
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
      <!-- Latest commit -->
      <div v-if="headSHA" class="head-commit">
        <a
          :href="'https://github.com/boldsoftware/exe/commit/' + headSHA"
          target="_blank"
          rel="noopener"
          class="head-commit-sha"
        >{{ headSHA.slice(0, 7) }}</a>
        <span v-if="headSubject" class="head-commit-subject">{{ headSubject }}</span>
        <span v-if="headDate" class="head-commit-date">{{ formatDate(headDate) }}</span>
      </div>

      <!-- Toolbar: filters + search -->
      <div class="toolbar-row">
        <div class="filter-dropdown" v-if="uniqueStages.length > 0">
          <button class="dropdown-trigger" @click="toggleDropdown('stage')" :class="{ 'has-selection': activeStages.size > 0 }">
            <span class="dropdown-label">Stage</span>
            <span class="dropdown-value">{{ activeStages.size === 0 ? 'All' : [...activeStages].join(', ') }}</span>
            <i class="pi pi-chevron-down dropdown-chevron"></i>
          </button>
          <div v-if="openDropdown === 'stage'" class="dropdown-menu">
            <label v-for="s in uniqueStages" :key="'stage-' + s" class="dropdown-option">
              <input type="checkbox" :checked="activeStages.has(s)" @change="toggleStageFilter(s)" />
              <span>{{ s }}</span>
            </label>
            <button v-if="activeStages.size > 0" class="dropdown-clear" @click="activeStages.clear()">Clear</button>
          </div>
        </div>
        <div class="filter-dropdown" v-if="uniqueRoles.length > 0">
          <button class="dropdown-trigger" @click="toggleDropdown('role')" :class="{ 'has-selection': activeRoles.size > 0 }">
            <span class="dropdown-label">Role</span>
            <span class="dropdown-value">{{ activeRoles.size === 0 ? 'All' : [...activeRoles].join(', ') }}</span>
            <i class="pi pi-chevron-down dropdown-chevron"></i>
          </button>
          <div v-if="openDropdown === 'role'" class="dropdown-menu">
            <label v-for="r in uniqueRoles" :key="'role-' + r" class="dropdown-option">
              <input type="checkbox" :checked="activeRoles.has(r)" @change="toggleRoleFilter(r)" />
              <span>{{ r }}</span>
            </label>
            <button v-if="activeRoles.size > 0" class="dropdown-clear" @click="activeRoles.clear()">Clear</button>
          </div>
        </div>
        <div class="filter-dropdown" v-if="uniqueProcesses.length > 0">
          <button class="dropdown-trigger" @click="toggleDropdown('process')" :class="{ 'has-selection': activeProcesses.size > 0 }">
            <span class="dropdown-label">Process</span>
            <span class="dropdown-value">{{ activeProcesses.size === 0 ? 'All' : [...activeProcesses].join(', ') }}</span>
            <i class="pi pi-chevron-down dropdown-chevron"></i>
          </button>
          <div v-if="openDropdown === 'process'" class="dropdown-menu">
            <label v-for="p in uniqueProcesses" :key="'proc-' + p" class="dropdown-option">
              <input type="checkbox" :checked="activeProcesses.has(p)" @change="toggleProcessFilter(p)" />
              <span>{{ p }}</span>
            </label>
            <button v-if="activeProcesses.size > 0" class="dropdown-clear" @click="activeProcesses.clear()">Clear</button>
          </div>
        </div>
        <div class="search-box">
          <i class="pi pi-search"></i>
          <input
            v-model="search"
            type="text"
            placeholder="Search..."
            class="search-input"
          />
          <button v-if="search" class="search-clear" @click="search = ''">
            <i class="pi pi-times"></i>
          </button>
        </div>
      </div>

      <!-- Tabs -->
      <div class="tab-bar">
        <button class="tab-btn" :class="{ active: activeTab === 'fleet' }" @click="activeTab = 'fleet'">
          <i class="pi pi-server"></i> Fleet
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'versions' }" @click="activeTab = 'versions'">
          <i class="pi pi-tags"></i> Versions
        </button>
        <button class="tab-btn" :class="{ active: activeTab === 'history' }" @click="activeTab = 'history'">
          <i class="pi pi-history"></i> History
          <span v-if="deploys.length > 0" class="tab-count">{{ deploys.length }}</span>
        </button>
      </div>

      <!-- ═══ Fleet Tab ═══ -->
      <div v-show="activeTab === 'fleet'">
        <div v-if="filteredProcs.length === 0" class="empty-state">
          {{ search ? 'No processes match your search.' : 'No processes found.' }}
        </div>

        <template v-else>
        <!-- Bulk action bar -->
        <div v-if="selectedProcs.size > 0" class="bulk-action-bar">
          <span class="bulk-count">{{ selectedProcs.size }} selected</span>
          <button
            class="bulk-deploy-btn"
            :disabled="deployableSelected.length === 0"
            @click="confirmBulkDeploy"
          >
            <i class="pi pi-upload"></i>
            Deploy {{ deployableSelected.length > 0 ? `(${deployableSelected.length})` : '' }}
          </button>
          <button class="bulk-clear-btn" @click="selectedProcs.clear()">Clear</button>
        </div>

        <div class="table-wrapper">
          <table class="deploy-table">
            <thead>
              <tr>
                <th class="col-select">
                  <input
                    type="checkbox"
                    class="row-checkbox"
                    :checked="allVisibleSelected"
                    :indeterminate="someVisibleSelected && !allVisibleSelected"
                    @change="toggleSelectAll"
                    title="Select all"
                  />
                </th>
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
                :class="{ 'row-selected': selectedProcs.has(procKey(p)) }"
                @click="toggleSelect(p, $event)"
              >
                <td class="col-select" @click.stop>
                  <input
                    type="checkbox"
                    class="row-checkbox"
                    :checked="selectedProcs.has(procKey(p))"
                    @change="toggleSelect(p, $event)"
                  />
                </td>
                <td class="col-hostname">
                  {{ p.hostname }}
                  <button class="copy-btn" title="Copy full domain name" @click.stop="copyDnsName(p.dns_name)">
                    <i class="pi" :class="justCopied === p.dns_name ? 'pi-check' : 'pi-copy'"></i>
                  </button>
                </td>
                <td class="col-role">{{ p.role }}</td>
                <td class="col-stage">{{ p.stage }}</td>
                <td class="col-region">{{ p.region || '\u2014' }}</td>
                <td class="col-process">
                  {{ p.process }}
                  <a :href="p.debug_url" target="_blank" rel="noopener" class="debug-link" @click.stop>
                    <i class="pi pi-external-link"></i>
                  </a>
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
                    @click.stop="confirmDeploy(p)"
                  >
                    <i v-if="isDeploying(p)" class="pi pi-spin pi-spinner"></i>
                    <i v-else class="pi pi-upload"></i>
                    Deploy
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      </div>

      <!-- ═══ History Tab ═══ -->
      <div v-show="activeTab === 'history'">
        <!-- Recent deploys -->
        <div v-if="deploys.length > 0" class="deploys-section">
          <div class="section-header" @click="deploysCollapsed = !deploysCollapsed">
            <i class="pi collapse-icon" :class="deploysCollapsed ? 'pi-chevron-right' : 'pi-chevron-down'"></i>
            <h2 class="section-title">Deploys</h2>
            <span class="section-count">{{ filteredDeploys.length }}</span>
            <div class="deploy-time-filters" @click.stop>
              <button
                v-for="f in deployTimeFilters"
                :key="f.value"
                class="filter-btn"
                :class="{ active: deployTimeFilter === f.value }"
                @click="deployTimeFilter = f.value"
              >{{ f.label }}</button>
            </div>
          </div>
          <div v-show="!deploysCollapsed" class="deploys-list">
            <div
              v-for="d in filteredDeploys"
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
                <span v-if="d.initiated_by" class="deploy-card-user">{{ d.initiated_by }}</span>
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
        <div v-else class="empty-state">No deploys recorded yet.</div>
      </div>

      <!-- ═══ Versions Tab ═══ -->
      <div v-show="activeTab === 'versions'">
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
        <div v-else class="empty-state">No version data available.</div>
      </div>
    </template>

    <!-- Deploy confirmation modal (single) -->
    <div v-if="confirmProc" class="modal-overlay" @click.self="closeConfirm">
      <div class="modal-dialog">
        <div class="modal-header">
          <span class="modal-title">
            Deploy <span class="modal-mono">{{ confirmProc.process }}</span>
            to <span class="modal-mono">{{ confirmProc.hostname }}</span>
          </span>
          <button class="modal-close" @click="closeConfirm"><i class="pi pi-times"></i></button>
        </div>
        <div class="modal-body">
          <div class="modal-sha-row">
            <span class="modal-sha-label">Deploying</span>
            <span class="modal-sha-value">{{ deploySHA.slice(0, 7) }}</span>
            <span v-if="!customSHA && headSubject" class="modal-sha-subject">{{ headSubject }}</span>
            <span v-if="customSHA" class="modal-sha-custom-badge">custom</span>
          </div>
          <div v-if="confirmProc.version" class="modal-sha-row">
            <span class="modal-sha-label">Currently</span>
            <span class="modal-sha-value">{{ confirmProc.version.slice(0, 7) }}</span>
            <span v-if="confirmProc.version_subject" class="modal-sha-subject">{{ confirmProc.version_subject }}</span>
          </div>
          <div class="modal-custom-sha">
            <label class="modal-custom-sha-label" for="custom-sha">Custom SHA</label>
            <input
              id="custom-sha"
              v-model="customSHA"
              type="text"
              class="modal-custom-sha-input"
              placeholder="paste full 40-char SHA to override"
              spellcheck="false"
              autocomplete="off"
            />
            <span v-if="customSHA && !isValidSHA(customSHA)" class="modal-custom-sha-error">must be 40 hex characters</span>
          </div>
          <div v-if="confirmLoading" class="modal-loading">
            <i class="pi pi-spin pi-spinner"></i> Loading commits...
          </div>
          <div v-else-if="confirmCommits.length > 0" class="modal-commits">
            <div class="modal-commits-header">{{ confirmCommits.length }} commit{{ confirmCommits.length !== 1 ? 's' : '' }}</div>
            <div class="modal-commit-list">
              <div v-for="c in confirmCommits" :key="c.sha" class="modal-commit">
                <a
                  :href="'https://github.com/boldsoftware/exe/commit/' + c.sha"
                  target="_blank"
                  rel="noopener"
                  class="modal-commit-sha"
                >{{ c.sha.slice(0, 7) }}</a>
                <span class="modal-commit-subject">{{ c.subject }}</span>
                <span v-if="c.date" class="modal-commit-date">{{ formatDate(c.date) }}</span>
              </div>
            </div>
          </div>
          <div v-else-if="!confirmLoading" class="modal-no-commits">
            No commit history available
          </div>
        </div>
        <div class="modal-footer">
          <button class="deploy-btn deploy-btn-cancel" @click="closeConfirm">Cancel</button>
          <button
            class="deploy-btn deploy-btn-confirm"
            :disabled="customSHA !== '' && !isValidSHA(customSHA)"
            @click="doDeploy(confirmProc!)"
          >
            <i class="pi pi-upload"></i>
            Deploy
          </button>
        </div>
      </div>
    </div>

    <!-- Bulk deploy confirmation modal -->
    <div v-if="bulkConfirmProcs" class="modal-overlay" @click.self="closeBulkConfirm">
      <div class="modal-dialog">
        <div class="modal-header">
          <span class="modal-title">
            Deploy <span class="modal-mono">{{ bulkConfirmProcs.length }}</span> targets
          </span>
          <button class="modal-close" @click="closeBulkConfirm"><i class="pi pi-times"></i></button>
        </div>
        <div class="modal-body">
          <div class="modal-sha-row">
            <span class="modal-sha-label">Deploying</span>
            <span class="modal-sha-value">{{ headSHA.slice(0, 7) }}</span>
            <span v-if="headSubject" class="modal-sha-subject">{{ headSubject }}</span>
          </div>
          <div class="bulk-target-list">
            <div class="bulk-target-header">Targets</div>
            <div v-for="p in bulkConfirmProcs" :key="procKey(p)" class="bulk-target-row">
              <span class="bulk-target-process">{{ p.process }}</span>
              <i class="pi pi-arrow-right bulk-target-arrow"></i>
              <span class="bulk-target-host">{{ p.hostname }}</span>
              <span v-if="p.version" class="bulk-target-version">{{ p.version.slice(0, 7) }}</span>
              <span v-if="p.commits_behind > 0" class="behind-badge">{{ p.commits_behind }}<i class="pi pi-arrow-down"></i></span>
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="deploy-btn deploy-btn-cancel" @click="closeBulkConfirm">Cancel</button>
          <button
            class="deploy-btn deploy-btn-confirm"
            @click="doBulkDeploy"
          >
            <i class="pi pi-upload"></i>
            Deploy All ({{ bulkConfirmProcs.length }})
          </button>
        </div>
      </div>
    </div>

    <!-- Live deploy progress dialog -->
    <div v-if="liveDeployVisible" class="modal-overlay" @click.self="liveDeployVisible = false">
      <div class="modal-dialog live-deploy-dialog">
        <div class="modal-header">
          <span class="modal-title">
            <i v-if="liveDeployAllDone" class="pi" :class="liveDeployAnyFailed ? 'pi-times-circle' : 'pi-check-circle'" :style="{ color: liveDeployAnyFailed ? 'var(--red-400)' : 'var(--green-400)' }"></i>
            <i v-else class="pi pi-spin pi-spinner"></i>
            Deploying {{ liveDeployIds.length }} target{{ liveDeployIds.length !== 1 ? 's' : '' }}
          </span>
          <button class="modal-close" @click="liveDeployVisible = false" title="Close (deploys continue in background)">
            <i class="pi pi-times"></i>
          </button>
        </div>
        <div class="live-deploy-content">
          <!-- Sidebar: node list -->
          <div v-if="liveDeployIds.length > 1" class="live-deploy-sidebar">
            <div
              v-for="id in liveDeployIds"
              :key="id"
              class="live-deploy-node"
              :class="{ active: liveDeploySelected === id, ['node-' + liveDeployStatusOf(id)?.state]: true }"
              @click="liveDeploySelected = id"
            >
              <i v-if="liveDeployStatusOf(id)?.state === 'running' || liveDeployStatusOf(id)?.state === 'pending'" class="pi pi-spin pi-spinner node-icon"></i>
              <i v-else-if="liveDeployStatusOf(id)?.state === 'done'" class="pi pi-check node-icon node-done"></i>
              <i v-else-if="liveDeployStatusOf(id)?.state === 'failed'" class="pi pi-times node-icon node-failed"></i>
              <span class="node-label">
                <span class="node-process">{{ liveDeployStatusOf(id)?.process }}</span>
                <span class="node-host">{{ liveDeployStatusOf(id)?.host }}</span>
              </span>
            </div>
          </div>
          <!-- Main: step detail for selected deploy -->
          <div class="live-deploy-detail">
            <template v-if="liveDeploySelectedStatus">
              <div class="live-detail-header">
                <span class="live-detail-target">
                  <span class="modal-mono">{{ liveDeploySelectedStatus.process }}</span>
                  <i class="pi pi-arrow-right" style="font-size: 0.5rem; color: var(--text-color-muted)"></i>
                  <span class="modal-mono">{{ liveDeploySelectedStatus.host }}</span>
                </span>
                <span class="deploy-card-state" :class="'state-' + liveDeploySelectedStatus.state">{{ liveDeploySelectedStatus.state }}</span>
              </div>
              <div class="live-detail-sha">
                <span class="modal-sha-value">{{ liveDeploySelectedStatus.sha.slice(0, 7) }}</span>
              </div>
              <div class="live-steps-list">
                <div
                  v-for="step in liveDeploySelectedStatus.steps"
                  :key="step.name"
                  class="live-step"
                  :class="'live-step-' + step.status"
                >
                  <div class="live-step-header">
                    <i v-if="step.status === 'running'" class="pi pi-spin pi-spinner live-step-icon"></i>
                    <i v-else-if="step.status === 'done'" class="pi pi-check live-step-icon"></i>
                    <i v-else-if="step.status === 'failed'" class="pi pi-times live-step-icon"></i>
                    <span v-else class="step-dot"></span>
                    <span class="live-step-name">{{ step.name }}</span>
                    <span v-if="step.started_at && step.done_at" class="live-step-duration">{{ stepDuration(step) }}</span>
                  </div>
                  <div v-if="step.output" class="live-step-output" :class="{ 'live-step-error': step.status === 'failed' }">{{ step.output }}</div>
                </div>
              </div>
              <div v-if="liveDeploySelectedStatus.error" class="deploy-card-error">{{ liveDeploySelectedStatus.error }}</div>
            </template>
          </div>
        </div>
      </div>
    </div>

    <!-- Minimized live deploy indicator (when dialog closed but deploys running) -->
    <button
      v-if="liveDeployIds.length > 0 && !liveDeployVisible && !liveDeployAllDone"
      class="live-deploy-fab"
      @click="liveDeployVisible = true"
      :title="`${liveDeployIds.length} deploy(s) in progress — click to view`"
    >
      <i class="pi pi-spin pi-spinner"></i>
      <span>{{ liveDeployIds.length }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import {
  fetchDeployInventory,
  fetchDeployCommits,
  fetchDeploys,
  startDeploy,
  type DeployProcess,
  type DeployStatus,
  type DeployCommit,
} from '../api/client'

const deployableProcesses = new Set(['exeletd', 'exeprox', 'exed', 'cgtop', 'metricsd', 'exe-ops'])

// Multi-select state
const selectedProcs = reactive(new Set<string>())
const bulkConfirmProcs = ref<DeployProcess[] | null>(null)

function procKey(p: DeployProcess): string {
  return p.hostname + ':' + p.process
}

const deployableSelected = computed(() => {
  return filteredProcs.value.filter(p =>
    selectedProcs.has(procKey(p)) && isDeployable(p) && canDeploy(p)
  )
})

const allVisibleSelected = computed(() => {
  if (filteredProcs.value.length === 0) return false
  return filteredProcs.value.every(p => selectedProcs.has(procKey(p)))
})

const someVisibleSelected = computed(() => {
  return filteredProcs.value.some(p => selectedProcs.has(procKey(p)))
})

function toggleSelect(p: DeployProcess, e: Event) {
  // Don't toggle when clicking links or buttons inside the row
  const target = e.target as HTMLElement
  if (target.closest('a') || target.closest('button.deploy-btn') || target.closest('button.copy-btn')) return
  const key = procKey(p)
  if (selectedProcs.has(key)) selectedProcs.delete(key)
  else selectedProcs.add(key)
}

function toggleSelectAll() {
  if (allVisibleSelected.value) {
    for (const p of filteredProcs.value) selectedProcs.delete(procKey(p))
  } else {
    for (const p of filteredProcs.value) selectedProcs.add(procKey(p))
  }
}

function confirmBulkDeploy() {
  const targets = deployableSelected.value
  if (targets.length === 0) return
  bulkConfirmProcs.value = targets
}

function closeBulkConfirm() {
  bulkConfirmProcs.value = null
}

// Live deploy progress tracking
const liveDeployIds = ref<string[]>([])
const liveDeployVisible = ref(false)
const liveDeploySelected = ref('')
let liveDeployPollTimer: ReturnType<typeof setInterval> | null = null

const liveDeployStatuses = computed(() => {
  const map = new Map<string, DeployStatus>()
  for (const d of deploys.value) {
    if (liveDeployIds.value.includes(d.id)) {
      map.set(d.id, d)
    }
  }
  return map
})

function liveDeployStatusOf(id: string): DeployStatus | undefined {
  return liveDeployStatuses.value.get(id)
}

const liveDeploySelectedStatus = computed(() => {
  if (!liveDeploySelected.value) return null
  return liveDeployStatuses.value.get(liveDeploySelected.value) || null
})

const liveDeployAllDone = computed(() => {
  if (liveDeployIds.value.length === 0) return false
  return liveDeployIds.value.every(id => {
    const s = liveDeployStatuses.value.get(id)
    return s && (s.state === 'done' || s.state === 'failed')
  })
})

const liveDeployAnyFailed = computed(() => {
  return liveDeployIds.value.some(id => {
    const s = liveDeployStatuses.value.get(id)
    return s?.state === 'failed'
  })
})

function stepDuration(step: { started_at?: string; done_at?: string }): string {
  if (!step.started_at || !step.done_at) return ''
  const ms = new Date(step.done_at).getTime() - new Date(step.started_at).getTime()
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function openLiveDeployDialog(ids: string[]) {
  liveDeployIds.value = ids
  liveDeploySelected.value = ids[0] || ''
  liveDeployVisible.value = true
}

// Auto-select first failed node when a failure occurs
watch(liveDeployAnyFailed, (failed) => {
  if (failed && liveDeployVisible.value) {
    const failedId = liveDeployIds.value.find(id => liveDeployStatuses.value.get(id)?.state === 'failed')
    if (failedId) liveDeploySelected.value = failedId
  }
})

// Clear live deploy tracking when all are done and dialog is closed
watch([liveDeployAllDone, liveDeployVisible], ([done, visible]) => {
  if (done && !visible) {
    liveDeployIds.value = []
    liveDeploySelected.value = ''
  }
})

async function doBulkDeploy() {
  const targets = bulkConfirmProcs.value
  if (!targets || targets.length === 0) return
  const sha = headSHA.value
  closeBulkConfirm()
  selectedProcs.clear()

  const results = await Promise.allSettled(
    targets.map(p =>
      startDeploy({
        stage: p.stage,
        role: p.role,
        process: p.process,
        host: p.hostname,
        dns_name: p.dns_name,
        sha,
      })
    )
  )

  const ids: string[] = []
  for (const r of results) {
    if (r.status === 'fulfilled') ids.push(r.value.id)
  }

  const failures = results.filter(r => r.status === 'rejected')
  if (failures.length > 0) {
    error.value = `${failures.length} of ${targets.length} deploys failed to start`
  }

  await loadDeploys()
  if (ids.length > 0) openLiveDeployDialog(ids)
}

const procs = ref<DeployProcess[]>([])
const headSHA = ref('')
const headSubject = ref('')
const headDate = ref('')
const confirmProc = ref<DeployProcess | null>(null)
const confirmCommits = ref<DeployCommit[]>([])
const confirmLoading = ref(false)
const customSHA = ref('')

const deploySHA = computed(() => {
  if (customSHA.value && isValidSHA(customSHA.value)) return customSHA.value
  return headSHA.value
})

function isValidSHA(s: string): boolean {
  return /^[0-9a-f]{40}$/i.test(s)
}
const deploys = ref<DeployStatus[]>([])
const loading = ref(true)
const error = ref('')
const sortCol = ref<'hostname' | 'version' | 'uptime'>('hostname')
const sortDir = ref<'asc' | 'desc'>('asc')
const search = ref('')
const activeStages = reactive(new Set<string>())
const activeRoles = reactive(new Set<string>())
const activeProcesses = reactive(new Set<string>())
const route = useRoute()
const validTabs = new Set(['fleet', 'versions', 'history'])
const initialTab = validTabs.has(route.query.tab as string) ? (route.query.tab as 'fleet' | 'versions' | 'history') : 'fleet'
const activeTab = ref<'fleet' | 'versions' | 'history'>(initialTab)
const openDropdown = ref<'stage' | 'role' | 'process' | null>(null)

function toggleDropdown(name: 'stage' | 'role' | 'process') {
  openDropdown.value = openDropdown.value === name ? null : name
}

function onClickOutside(e: MouseEvent) {
  if (openDropdown.value && !(e.target as Element)?.closest('.filter-dropdown')) {
    openDropdown.value = null
  }
}

onMounted(() => {
  document.addEventListener('click', onClickOutside, true)
})
const deploysCollapsed = ref(false)
const deployTimeFilter = ref<'10m' | '24h' | 'all'>('all')
const justCopied = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null
let deployPollTimer: ReturnType<typeof setInterval> | null = null

const deployTimeFilters = [
  { label: '10m', value: '10m' as const },
  { label: '24h', value: '24h' as const },
  { label: 'All', value: 'all' as const },
]

const filteredDeploys = computed(() => {
  if (deployTimeFilter.value === 'all') return deploys.value
  const now = Date.now()
  const cutoff = deployTimeFilter.value === '10m' ? 10 * 60 * 1000 : 24 * 60 * 60 * 1000
  return deploys.value.filter(d => {
    // Always show active deploys
    if (d.state === 'running' || d.state === 'pending') return true
    const t = new Date(d.started_at).getTime()
    return now - t < cutoff
  })
})

async function copyDnsName(dnsName: string) {
  try {
    await navigator.clipboard.writeText(dnsName)
    justCopied.value = dnsName
    setTimeout(() => { if (justCopied.value === dnsName) justCopied.value = '' }, 1500)
  } catch {}
}

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
    list = list.filter(p =>
      p.hostname.toLowerCase().includes(q) ||
      p.process.toLowerCase().includes(q) ||
      p.role.toLowerCase().includes(q) ||
      p.version.toLowerCase().includes(q)
    )
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
  let rows = [...groups.values()]
  if (search.value) {
    const q = search.value.toLowerCase()
    rows = rows.filter(r =>
      r.stage.toLowerCase().includes(q) ||
      r.process.toLowerCase().includes(q) ||
      r.version.toLowerCase().includes(q) ||
      r.subject.toLowerCase().includes(q)
    )
  }
  rows.sort((a, b) => {
    let c = a.stage.localeCompare(b.stage)
    if (c !== 0) return c
    c = a.process.localeCompare(b.process)
    if (c !== 0) return c
    return b.count - a.count // most common version first
  })
  return rows
})

function stepsWithOutput(d: DeployStatus): { name: string; output: string }[] {
  return d.steps.filter(s => s.output && s.status !== 'failed')
}

function isMismatch(p: DeployProcess): boolean {
  if (!p.version) return false
  return p.version !== headSHA.value
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

const isExeOpsDeploying = computed(() => {
  return deploys.value.some(d =>
    d.process === 'exe-ops' && (d.state === 'running' || d.state === 'pending')
  )
})

function deployKey(stage: string, role: string, process: string, host: string): string {
  return `${stage}/${role}/${process}/${host}`
}

function isDeployable(p: DeployProcess): boolean {
  return deployableProcesses.has(p.process)
}

function isDeploying(p: DeployProcess): boolean {
  return activeDeployKeys.value.has(deployKey(p.stage, p.role, p.process, p.hostname))
}

const deployableStages = new Set(['staging', 'prod', 'global'])
const prodAllowedProcesses = new Set(['metricsd', 'cgtop', 'exeletd'])

function canDeploy(p: DeployProcess): boolean {
  if (!deployableStages.has(p.stage)) return false
  if (p.stage === 'prod' && !prodAllowedProcesses.has(p.process)) return false
  if (isDeploying(p)) return false
  if (!headSHA.value) return false
  // exe-ops deploys the deploy server itself; don't allow while other deploys are active
  if (p.process === 'exe-ops' && hasActiveDeploys.value) return false
  // don't allow any deploys while exe-ops is deploying
  if (isExeOpsDeploying.value) return false
  return true
}

function deployTitle(p: DeployProcess): string {
  if (!deployableStages.has(p.stage)) return 'Only staging, prod, and global deploys are allowed'
  if (p.stage === 'prod' && !prodAllowedProcesses.has(p.process)) return `Prod deploys not allowed for ${p.process}`
  if (isDeploying(p)) return 'Deploy in progress'
  if (!headSHA.value) return 'HEAD SHA unknown'
  if (p.process === 'exe-ops' && hasActiveDeploys.value) return 'Wait for active deploys to finish before deploying exe-ops'
  if (isExeOpsDeploying.value) return 'Wait for exe-ops deploy to finish'
  if (p.version === headSHA.value) return `Already at HEAD (${headSHA.value.slice(0, 7)})`
  let title = `Deploy ${headSHA.value.slice(0, 7)} to ${p.hostname}`
  if (headSubject.value) title += `\n${headSubject.value}`
  if (headDate.value) title += `\n${formatDate(headDate.value)}`
  return title
}

async function confirmDeploy(p: DeployProcess) {
  if (!canDeploy(p)) return
  confirmProc.value = p
  confirmCommits.value = []
  confirmLoading.value = true
  try {
    const commits = await fetchDeployCommits(p.version || '', headSHA.value)
    confirmCommits.value = commits || []
  } catch {
    // If we can't load commits, still show the modal
  } finally {
    confirmLoading.value = false
  }
}

function closeConfirm() {
  confirmProc.value = null
  confirmCommits.value = []
  customSHA.value = ''
}

async function doDeploy(p: DeployProcess) {
  if (!canDeploy(p)) return
  const sha = deploySHA.value
  closeConfirm()
  try {
    const status = await startDeploy({
      stage: p.stage,
      role: p.role,
      process: p.process,
      host: p.hostname,
      dns_name: p.dns_name,
      sha,
    })
    await loadDeploys()
    openLiveDeployDialog([status.id])
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
    headDate.value = inv.head_date
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

watch(hasActiveDeploys, (active, wasActive) => {
  startDeployPolling()
  // Refresh inventory immediately when deploys finish
  if (!active && wasActive) {
    load()
  }
})

onMounted(async () => {
  await Promise.all([load(), loadDeploys()])
  pollTimer = setInterval(load, 30000)
  startDeployPolling()
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  stopDeployPolling()
  document.removeEventListener('click', onClickOutside, true)
})
</script>

<style scoped>
.deploy-view {
  max-width: 1200px;
}

.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 1rem;
}

.page-header h1 {
  font-size: 1.5rem;
  font-weight: 600;
}

.page-subtitle {
  font-size: 0.8rem;
  color: var(--text-color-muted);
  margin-top: 0.25rem;
}

/* -- Head commit stat -- */
.head-commit {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  margin-bottom: 1rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
}

.head-commit-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 1.1rem;
  font-weight: 700;
  color: var(--primary-color);
  flex-shrink: 0;
}

.head-commit-sha:hover {
  color: var(--primary-hover);
  text-decoration: underline;
}

.head-commit-subject {
  font-size: 0.85rem;
  color: var(--text-color);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.head-commit-date {
  font-size: 0.75rem;
  color: var(--text-color-muted);
  flex-shrink: 0;
}

/* -- Tab bar -- */
.tab-bar {
  display: flex;
  gap: 0.25rem;
  margin-bottom: 1rem;
  border-bottom: 1px solid var(--surface-border);
}

.tab-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.5rem 1rem;
  font-size: 0.8rem;
  font-family: inherit;
  font-weight: 500;
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
  margin-bottom: -1px;
}

.tab-btn:hover {
  color: var(--text-color);
}

.tab-btn.active {
  color: var(--primary-color);
  border-bottom-color: var(--primary-color);
}

.tab-btn .pi {
  font-size: 0.8rem;
}

.tab-count {
  font-size: 0.65rem;
  font-weight: 600;
  background: var(--surface-border);
  color: var(--text-color-secondary);
  padding: 0.05rem 0.4rem;
  border-radius: 8px;
}

.tab-btn.active .tab-count {
  background: var(--primary-50);
  color: var(--primary-color);
}

.head-sha-badge {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.75rem 1rem;
  margin-bottom: 1rem;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  font-size: 0.8rem;
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

.head-sha-date {
  color: var(--text-color-muted);
  font-size: 0.65rem;
  opacity: 0.7;
}

/* -- Section headers (collapsible) -- */
.section-header {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  cursor: pointer;
  user-select: none;
  margin-bottom: 0.5rem;
}

.section-header:hover .section-title {
  color: var(--text-color);
}

.collapse-icon {
  font-size: 0.55rem;
  color: var(--text-color-muted);
}

.section-count {
  font-size: 0.65rem;
  color: var(--text-color-muted);
  background: var(--surface-border);
  padding: 0.05rem 0.35rem;
  border-radius: 8px;
  margin-left: 0.125rem;
}

.deploy-time-filters {
  display: flex;
  gap: 0.25rem;
  margin-left: auto;
}

.head-sha-section {
  margin-bottom: 1rem;
}

.summary-section {
  margin-bottom: 1.5rem;
}

/* -- Deploys section -- */
.deploys-section {
  margin-bottom: 1.5rem;
}

.section-title {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--text-color);
  margin-bottom: 0;
}

.deploys-list {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.deploy-card {
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 0.6rem 1rem;
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

.deploy-card-user {
  font-size: 0.65rem;
  color: var(--text-color-muted);
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
  gap: 0.5rem;
  margin-bottom: 1rem;
  flex-wrap: wrap;
}

/* -- Filter dropdowns -- */
.filter-dropdown {
  position: relative;
}

.dropdown-trigger {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.35rem 0.6rem;
  font-size: 0.75rem;
  font-family: inherit;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
  white-space: nowrap;
}

.dropdown-trigger:hover {
  border-color: var(--surface-border-bright);
  color: var(--text-color);
}

.dropdown-trigger.has-selection {
  border-color: var(--primary-color);
  color: var(--primary-color);
  background: var(--primary-50);
}

.dropdown-label {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-color-muted);
}

.dropdown-trigger.has-selection .dropdown-label {
  color: var(--primary-color);
  opacity: 0.7;
}

.dropdown-value {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.dropdown-chevron {
  font-size: 0.55rem;
  opacity: 0.6;
}

.dropdown-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  z-index: 200;
  min-width: 160px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
  padding: 0.375rem;
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
}

.dropdown-option {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.35rem 0.5rem;
  font-size: 0.75rem;
  color: var(--text-color-secondary);
  border-radius: 4px;
  cursor: pointer;
  transition: background 0.1s;
}

.dropdown-option:hover {
  background: var(--surface-hover);
  color: var(--text-color);
}

.dropdown-option input[type="checkbox"] {
  -webkit-appearance: none;
  appearance: none;
  width: 14px;
  height: 14px;
  border: 1px solid var(--surface-border-bright);
  border-radius: 3px;
  background: var(--surface-ground);
  cursor: pointer;
  position: relative;
  flex-shrink: 0;
}

.dropdown-option input[type="checkbox"]:checked {
  background: var(--primary-color);
  border-color: var(--primary-color);
}

.dropdown-option input[type="checkbox"]:checked::after {
  content: '';
  position: absolute;
  left: 3.5px;
  top: 1px;
  width: 4px;
  height: 8px;
  border: solid var(--primary-color-text);
  border-width: 0 2px 2px 0;
  transform: rotate(45deg);
}

.dropdown-clear {
  margin-top: 0.25rem;
  padding: 0.25rem 0.5rem;
  font-size: 0.65rem;
  font-family: inherit;
  background: none;
  border: none;
  border-top: 1px solid var(--surface-border);
  color: var(--text-color-muted);
  cursor: pointer;
  text-align: left;
  padding-top: 0.375rem;
}

.dropdown-clear:hover {
  color: var(--primary-color);
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
  border-radius: 8px;
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
  white-space: nowrap;
}

.copy-btn {
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  padding: 0.1rem 0.2rem;
  font-size: 0.55rem;
  vertical-align: middle;
  opacity: 0;
  transition: opacity 0.15s, color 0.15s;
}

.deploy-row:hover .copy-btn {
  opacity: 1;
}

.copy-btn:hover {
  color: var(--primary-color);
}

.debug-link {
  color: var(--text-color-muted);
  font-size: 0.6rem;
  margin-left: 0.25rem;
  vertical-align: middle;
}

.debug-link:hover {
  color: var(--primary-color);
}

.version-cell {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  min-width: 0;
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
  flex-shrink: 0;
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
  white-space: nowrap;
  min-width: 0;
  flex: 1;
}

.version-date {
  font-size: 0.65rem;
  color: var(--text-color-muted);
  opacity: 0.7;
  flex-shrink: 0;
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

.col-select {
  width: 1px;
  padding-left: 0.75rem !important;
  padding-right: 0 !important;
}

.row-checkbox {
  -webkit-appearance: none;
  appearance: none;
  width: 14px;
  height: 14px;
  border: 1px solid var(--surface-border-bright);
  border-radius: 3px;
  background: var(--surface-ground);
  cursor: pointer;
  position: relative;
  flex-shrink: 0;
  vertical-align: middle;
}

.row-checkbox:checked {
  background: var(--primary-color);
  border-color: var(--primary-color);
}

.row-checkbox:checked::after {
  content: '';
  position: absolute;
  left: 3.5px;
  top: 1px;
  width: 4px;
  height: 8px;
  border: solid var(--primary-color-text);
  border-width: 0 2px 2px 0;
  transform: rotate(45deg);
}

.row-checkbox:indeterminate {
  background: var(--primary-color);
  border-color: var(--primary-color);
}

.row-checkbox:indeterminate::after {
  content: '';
  position: absolute;
  left: 2px;
  top: 5px;
  width: 8px;
  height: 2px;
  background: var(--primary-color-text);
}

.deploy-row.row-selected {
  background: var(--primary-50);
}

.deploy-row.row-selected:hover {
  background: var(--primary-50);
}

/* -- Bulk action bar -- */
.bulk-action-bar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 1rem;
  margin-bottom: 0.5rem;
  background: var(--primary-50);
  border: 1px solid var(--primary-color);
  border-radius: 8px;
}

.bulk-count {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--primary-color);
}

.bulk-deploy-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.3rem 0.75rem;
  font-size: 0.75rem;
  font-family: inherit;
  font-weight: 600;
  background: var(--primary-color);
  border: none;
  border-radius: 4px;
  color: #fff;
  cursor: pointer;
  transition: opacity 0.15s;
}

.bulk-deploy-btn .pi {
  font-size: 0.6rem;
}

.bulk-deploy-btn:hover:not(:disabled) {
  opacity: 0.9;
}

.bulk-deploy-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.bulk-clear-btn {
  margin-left: auto;
  padding: 0.25rem 0.5rem;
  font-size: 0.7rem;
  font-family: inherit;
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
}

.bulk-clear-btn:hover {
  color: var(--text-color);
}

/* -- Bulk confirm modal targets -- */
.bulk-target-list {
  margin-top: 0.75rem;
}

.bulk-target-header {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  margin-bottom: 0.375rem;
}

.bulk-target-row {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.25rem 0;
  font-size: 0.75rem;
}

.bulk-target-process {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  font-weight: 600;
  color: var(--text-color);
}

.bulk-target-arrow {
  font-size: 0.5rem;
  color: var(--text-color-muted);
}

.bulk-target-host {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  color: var(--text-color-secondary);
}

.bulk-target-version {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  color: var(--text-color-muted);
  margin-left: auto;
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

.deploy-btn-confirm {
  border-color: var(--primary-color);
  color: #fff;
  background: var(--primary-color);
  font-weight: 600;
}

.deploy-btn-confirm:hover:not(:disabled) {
  opacity: 0.9;
}

.deploy-btn-cancel {
  color: var(--text-color-muted);
}

.metric-blank {
  color: var(--text-color-muted);
}

.loading-state {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 2rem;
  color: var(--text-color-secondary);
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
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  border-radius: 6px;
  margin-bottom: 1rem;
  font-size: 0.8rem;
}

.message-error {
  background: var(--red-subtle);
  color: var(--red-400);
  border: 1px solid rgba(248, 81, 73, 0.2);
}

@media (max-width: 991px) {
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

/* -- Deploy confirmation modal -- */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-dialog {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 560px;
  max-width: 90vw;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--surface-border);
}

.modal-title {
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--text-color);
}

.modal-mono {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
}

.modal-close {
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  padding: 0.25rem;
  font-size: 0.8rem;
}

.modal-close:hover {
  color: var(--text-color);
}

.modal-body {
  padding: 0.75rem 1rem;
  overflow-y: auto;
  flex: 1;
}

.modal-sha-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.8rem;
  margin-bottom: 0.375rem;
}

.modal-sha-label {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-muted);
  width: 70px;
  flex-shrink: 0;
}

.modal-sha-value {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.75rem;
  font-weight: 600;
  color: var(--primary-color);
  flex-shrink: 0;
}

.modal-sha-subject {
  font-size: 0.75rem;
  color: var(--text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modal-custom-sha {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.75rem;
  padding-top: 0.75rem;
  border-top: 1px solid var(--surface-border);
}

.modal-custom-sha-label {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-muted);
  white-space: nowrap;
  flex-shrink: 0;
}

.modal-custom-sha-input {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  padding: 0.3rem 0.5rem;
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  color: var(--text-color);
  outline: none;
  flex: 1;
  min-width: 0;
}

.modal-custom-sha-input:focus {
  border-color: var(--primary-color);
}

.modal-custom-sha-input::placeholder {
  color: var(--text-color-muted);
  font-family: inherit;
  font-size: 0.65rem;
}

.modal-custom-sha-error {
  font-size: 0.6rem;
  color: var(--red-400);
  white-space: nowrap;
  flex-shrink: 0;
}

.modal-sha-custom-badge {
  font-size: 0.55rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.1rem 0.35rem;
  border-radius: 3px;
  background: var(--yellow-subtle, rgba(255, 200, 0, 0.15));
  color: var(--yellow-400, #d4a017);
}

.modal-loading {
  padding: 1rem 0;
  color: var(--text-color-muted);
  font-size: 0.8rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.modal-commits {
  margin-top: 0.75rem;
}

.modal-commits-header {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--text-color-muted);
  margin-bottom: 0.375rem;
}

.modal-commit-list {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
}

.modal-commit {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.25rem 0;
  font-size: 0.75rem;
}

.modal-commit-sha {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  color: var(--primary-color);
  flex-shrink: 0;
}

.modal-commit-sha:hover {
  text-decoration: underline;
}

.modal-commit-subject {
  color: var(--text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.modal-commit-date {
  font-size: 0.65rem;
  color: var(--text-color-muted);
  flex-shrink: 0;
}

.modal-no-commits {
  padding: 1rem 0;
  color: var(--text-color-muted);
  font-size: 0.8rem;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  border-top: 1px solid var(--surface-border);
}

/* -- Live deploy progress dialog -- */
.live-deploy-dialog {
  width: 720px;
  max-width: 90vw;
  max-height: 80vh;
}

.live-deploy-dialog .modal-title {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.live-deploy-dialog .modal-title .pi {
  font-size: 0.85rem;
}

.live-deploy-content {
  display: flex;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.live-deploy-sidebar {
  width: 180px;
  flex-shrink: 0;
  border-right: 1px solid var(--surface-border);
  overflow-y: auto;
  padding: 0.375rem;
}

.live-deploy-node {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  padding: 0.375rem 0.5rem;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.7rem;
  transition: background 0.1s;
}

.live-deploy-node:hover {
  background: var(--surface-hover);
}

.live-deploy-node.active {
  background: var(--primary-50);
}

.node-icon {
  font-size: 0.6rem;
  flex-shrink: 0;
}

.node-done .node-icon,
.live-deploy-node.node-done .node-icon {
  color: var(--green-400);
}

.node-failed .node-icon,
.live-deploy-node.node-failed .node-icon {
  color: var(--red-400);
}

.node-label {
  display: flex;
  flex-direction: column;
  gap: 0.05rem;
  min-width: 0;
  overflow: hidden;
}

.node-process {
  font-weight: 600;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  color: var(--text-color);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.node-host {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.6rem;
  color: var(--text-color-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.live-deploy-detail {
  flex: 1;
  overflow-y: auto;
  padding: 0.75rem 1rem;
  min-width: 0;
}

.live-detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.25rem;
}

.live-detail-target {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  font-weight: 600;
  font-size: 0.8rem;
}

.live-detail-sha {
  margin-bottom: 0.75rem;
}

.live-steps-list {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.live-step {
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 0.5rem 0.75rem;
  font-size: 0.75rem;
}

.live-step-running {
  border-color: var(--primary-color);
  background: var(--primary-50);
}

.live-step-done {
  border-color: var(--surface-border);
}

.live-step-failed {
  border-color: var(--red-400);
  background: var(--red-subtle);
}

.live-step-header {
  display: flex;
  align-items: center;
  gap: 0.375rem;
}

.live-step-icon {
  font-size: 0.6rem;
  flex-shrink: 0;
}

.live-step-running .live-step-icon {
  color: var(--primary-color);
}

.live-step-done .live-step-icon {
  color: var(--green-400);
}

.live-step-failed .live-step-icon {
  color: var(--red-400);
}

.live-step-name {
  font-weight: 600;
  color: var(--text-color);
  text-transform: capitalize;
}

.live-step-duration {
  margin-left: auto;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  color: var(--text-color-muted);
}

.live-step-output {
  margin-top: 0.25rem;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  color: var(--text-color-secondary);
  white-space: pre-wrap;
  word-break: break-all;
}

.live-step-error {
  color: var(--red-400);
}

/* -- Floating action button for minimized live deploy -- */
.live-deploy-fab {
  position: fixed;
  bottom: 1.5rem;
  right: 1.5rem;
  z-index: 900;
  display: flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.5rem 1rem;
  background: var(--primary-color);
  color: #fff;
  border: none;
  border-radius: 20px;
  font-size: 0.8rem;
  font-weight: 600;
  font-family: inherit;
  cursor: pointer;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3);
  transition: opacity 0.15s;
}

.live-deploy-fab:hover {
  opacity: 0.9;
}

.live-deploy-fab .pi {
  font-size: 0.75rem;
}
</style>

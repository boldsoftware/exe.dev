<template>
  <div class="vm-detail-page">
    <!-- Breadcrumbs -->
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <router-link to="/" class="breadcrumb-link">Home</router-link>
      <span class="breadcrumb-sep">›</span>
      <router-link to="/" class="breadcrumb-link">VMs</router-link>
      <span class="breadcrumb-sep">›</span>
      <span class="breadcrumb-current">{{ vmName }}</span>
    </nav>

    <!-- Loading -->
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <!-- Error -->
    <div v-else-if="loadError" class="error-state">
      <p>{{ loadError }}</p>
      <button class="btn btn-secondary" @click="load">Retry</button>
    </div>

    <div v-else-if="box" class="vm-card">
      <!-- Header -->
      <div class="vm-header">
        <div class="vm-header-left">
          <StatusDot :status="box.status" />
          <h1 class="vm-name">{{ box.name }}</h1>
          <span v-if="box.proxyShare === 'public'" class="badge badge-public">PUBLIC</span>
          <span v-if="box.isTeamShared" class="badge badge-team">TEAM</span>
        </div>
        <div class="vm-actions">
          <a :href="box.proxyURL" class="action-pill" target="_blank" rel="noopener noreferrer" title="Open HTTPS">
            <i class="pi pi-globe"></i> HTTPS
          </a>
          <a :href="box.terminalURL" class="action-pill" target="_blank" rel="noopener noreferrer" title="Open Terminal">
            <i class="pi pi-chevron-right"></i> Terminal
          </a>
          <a v-if="box.shelleyURL" :href="box.shelleyURL" class="action-pill" target="_blank" rel="noopener noreferrer" title="Open Shelley">
            <i class="pi pi-sparkles"></i> Shelley
          </a>
          <button v-if="box.vscodeURL" class="action-pill" @click="editorModalOpen = true" title="Open Editor">
            <i class="pi pi-code"></i> Editor
          </button>
          <div class="junk-drawer-wrap">
            <button class="action-pill junk-btn" :class="{ active: drawerOpen }" @click.stop="drawerOpen = !drawerOpen" title="More actions">
              <i class="pi pi-ellipsis-h"></i>
            </button>
            <div v-if="drawerOpen" class="junk-drawer" @click.stop>
              <button class="drawer-item" @click="doAction('share')"><i class="pi pi-share-alt"></i> Share</button>
              <button v-if="hasTeam" class="drawer-item" @click="doAction('share-team')">
                <i class="pi pi-users"></i> {{ box.isTeamShared ? 'Unshare Team' : 'Share with Team' }}
              </button>
              <button class="drawer-item" @click="doAction('share-link')"><i class="pi pi-link"></i> Share Link</button>
              <button class="drawer-item" @click="doAction('copy')"><i class="pi pi-clone"></i> Copy</button>
              <button class="drawer-item" @click="doAction('rename')"><i class="pi pi-pencil"></i> Rename</button>
              <button class="drawer-item" @click="doAction('restart')"><i class="pi pi-refresh"></i> Restart</button>
              <button class="drawer-item danger" @click="doAction('delete')"><i class="pi pi-trash"></i> Delete</button>
            </div>
          </div>
        </div>
      </div>

      <!-- Subtitle -->
      <div class="vm-subtitle">
        <span v-if="box.region">{{ box.region }}</span>
        <span v-if="box.region && box.image" class="sep">·</span>
        <span v-if="box.image">{{ box.image }}</span>
        <span v-if="box.createdAt" class="sep">·</span>
        <span v-if="box.createdAt">Created {{ box.createdAt }}</span>
        <span v-if="uptimeDisplay" class="sep">·</span>
        <span v-if="uptimeDisplay">Up {{ uptimeDisplay }}</span>
      </div>

      <!-- SSH Field -->
      <div v-if="box.sshCommand" class="ssh-row">
        <span class="ssh-label">SSH</span>
        <code class="ssh-cmd">{{ box.sshCommand }}</code>
        <CopyButton :text="box.sshCommand" title="Copy SSH command" />
      </div>

      <!-- Tags -->
      <div v-if="box.displayTags && box.displayTags.length" class="tags-row">
        <span v-for="tag in box.displayTags" :key="tag" class="tag">#{{ tag }}</span>
      </div>

      <!-- Live Metrics -->
      <LiveMetrics v-if="box.status === 'running'" :vm-name="box.name" :vm-status="box.status" />

      <div class="section-divider"></div>

      <!-- Billing: two columns of rows under subheadings -->
      <div class="billing-columns">
        <!-- This Billing Period -->
        <div class="billing-section">
          <div class="section-heading">Usage<span v-if="periodLabel" class="section-heading-sub">{{ periodLabel }}</span></div>
          <div v-if="usageLoading" class="card-loading"><i class="pi pi-spin pi-spinner"></i></div>
          <template v-else-if="vmUsage">
            <div class="card-row">
              <span class="card-label">Disk (avg)</span>
              <span class="card-value">{{ vmUsage.display.disk_provisioned || '—' }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">Disk included</span>
              <span class="card-value">{{ vmUsage.display.included_disk || '—' }}</span>
            </div>
            <div class="card-row" :class="{ overage: vmUsage.display.overage_disk }">
              <span class="card-label">Disk overage</span>
              <span class="card-value">{{ vmUsage.display.overage_disk || 'none' }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">Bandwidth</span>
              <span class="card-value">{{ vmUsage.display.bandwidth || '—' }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">CPU time</span>
              <span class="card-value">{{ formatCPUTime(vmUsage.cpu_seconds) }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">I/O</span>
              <span class="card-value">{{ formatBytes(vmUsage.io_read_bytes + vmUsage.io_write_bytes) }}</span>
            </div>
          </template>
          <div v-else class="card-empty">No usage data for this period.</div>
        </div>

        <!-- Plan & Limits -->
        <div class="billing-section">
          <div class="section-heading">Plan &amp; Limits</div>
          <div v-if="profileLoading" class="card-loading"><i class="pi pi-spin pi-spinner"></i></div>
          <template v-else-if="profile">
            <div v-if="profile.credits.planName" class="card-row">
              <span class="card-label">Plan</span>
              <span class="card-value">{{ profile.credits.planName }}</span>
            </div>
            <div v-if="vmUsage?.display.included_disk" class="card-row">
              <span class="card-label">Disk included</span>
              <span class="card-value">{{ vmUsage.display.included_disk }}</span>
            </div>
            <div v-if="maxDiskDisplay !== '—'" class="card-row">
              <span class="card-label">Max disk</span>
              <span class="card-value">{{ maxDiskDisplay }}</span>
            </div>
            <div v-if="vmsUsedDisplay !== '—'" class="card-row">
              <span class="card-label">VMs used</span>
              <span class="card-value">{{ vmsUsedDisplay }}</span>
            </div>
            <div v-if="vmUsage?.display.overage_disk || vmUsage?.display.overage_bandwidth" class="card-row overage">
              <span class="card-label">Est. overage</span>
              <span class="card-value">{{ overageDisplay }}</span>
            </div>
          </template>
          <div v-else class="card-empty">Plan info unavailable.</div>
        </div>
      </div>

      <!-- LLM Usage for this VM -->
      <div v-if="llmUsage && llmUsage.models.length" class="billing-section llm-usage-section">
        <div class="section-heading">LLM Usage<span v-if="llmPeriodLabel" class="section-heading-sub">{{ llmPeriodLabel }}</span></div>
        <div class="card-row" v-for="m in llmUsage.models" :key="m.model + m.provider">
          <span class="card-label">{{ m.model }}</span>
          <span class="card-value">{{ m.cost }}</span>
        </div>
        <div class="card-row card-row-total">
          <span class="card-label">Total</span>
          <span class="card-value">{{ llmUsage.totalCost }}</span>
        </div>
      </div>

      <!-- Charts placeholder (hidden until implemented) -->
      <!-- <div class="section-placeholder">
        <div class="placeholder-title">Usage History</div>
        <div class="placeholder-body">Historical usage charts will appear here.</div>
      </div> -->
    </div>

    <!-- VM not found -->
    <div v-else class="error-state">
      <p>VM "{{ vmName }}" not found.</p>
      <router-link to="/" class="btn btn-secondary">Back to VMs</router-link>
    </div>

    <!-- Editor Picker Modal -->
    <div v-if="editorModalOpen" class="modal-overlay" @click="editorModalOpen = false">
      <div class="modal-dialog" role="dialog" aria-modal="true" @click.stop>
        <div class="modal-header">
          <span class="modal-title">Open in Editor</span>
          <button class="modal-close" @click="editorModalOpen = false">&times;</button>
        </div>
        <div class="modal-body">
          <div>
            <div class="field-label">Editor</div>
            <div class="editor-picker">
              <button
                v-for="ed in editors"
                :key="ed.value"
                class="editor-btn"
                :class="{ active: editorChoice === ed.value }"
                @click="editorChoice = ed.value; saveEditorChoice()"
              >{{ ed.label }}</button>
            </div>
          </div>
          <div>
            <div class="field-label">Working Directory</div>
            <input v-model="editorDir" class="field-input" />
          </div>
          <div>
            <div class="field-label">URL</div>
            <div class="editor-url-row">
              <code class="editor-url">{{ editorURL }}</code>
              <CopyButton :text="editorURL" title="Copy" />
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <a :href="editorURL" class="btn btn-primary" style="text-decoration:none; text-align:center;">Open Editor</a>
        </div>
      </div>
    </div>

    <!-- Command Modal -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :display-command="modal.displayCommand"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :default-value="modal.defaultValue"
      :danger="modal.danger"
      :success-format="modal.successFormat"
      :suggestions="modal.suggestions"
      @close="modal.visible = false"
      @success="onModalSuccess"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, reactive } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  fetchDashboard,
  fetchVMUsage,
  fetchBoxLLMUsage,
  fetchProfile,
  type BoxInfo,
  type VMUsageEntry,
  type ProfileData,
  type BoxLLMUsageResponse,
  shellQuote,
} from '../api/client'
import StatusDot from '../components/StatusDot.vue'
import CopyButton from '../components/CopyButton.vue'
import CommandModal from '../components/CommandModal.vue'
import LiveMetrics from '../components/LiveMetrics.vue'

const route = useRoute()
const router = useRouter()

const vmName = computed(() => route.params.name as string)

const loading = ref(true)
const loadError = ref('')
const box = ref<BoxInfo | null>(null)
const hasTeam = ref(false)

// Billing / usage
const usageLoading = ref(true)
const vmUsage = ref<VMUsageEntry | null>(null)
const periodStart = ref('')
const periodEnd = ref('')

// Profile
const profileLoading = ref(true)
const profile = ref<ProfileData | null>(null)

// LLM usage
const llmUsage = ref<BoxLLMUsageResponse | null>(null)

// Junk drawer
const drawerOpen = ref(false)

// Editor modal
const editorModalOpen = ref(false)
const editorChoice = ref(localStorage.getItem('preferred-editor') || 'vscode')
const editorDir = ref('/home/exedev')
const editors = [
  { value: 'vscode', label: 'VS Code' },
  { value: 'cursor', label: 'Cursor' },
  { value: 'zed', label: 'Zed' },
]

const editorURL = computed(() => {
  if (!box.value?.vscodeURL) return ''
  const baseURL = box.value.vscodeURL
  const match = baseURL.match(/^vscode:\/\/vscode-remote\/ssh-remote\+([^/]+)/)
  const connStr = match ? match[1] : box.value.name
  if (editorChoice.value === 'vscode') {
    return `vscode://vscode-remote/ssh-remote+${connStr}${editorDir.value}?windowId=_blank`
  } else if (editorChoice.value === 'cursor') {
    return `cursor://vscode-remote/ssh-remote+${connStr}${editorDir.value}?windowId=_blank`
  } else if (editorChoice.value === 'zed') {
    return `zed://ssh/${connStr}${editorDir.value}`
  }
  return baseURL
})

function saveEditorChoice() {
  localStorage.setItem('preferred-editor', editorChoice.value)
}

function fmtPeriodDate(s: string): string {
  return new Date(s).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

const periodLabel = computed(() => {
  if (!periodStart.value || !periodEnd.value) return ''
  return `${fmtPeriodDate(periodStart.value)} – ${fmtPeriodDate(periodEnd.value)}`
})

const llmPeriodLabel = computed(() => {
  if (!llmUsage.value?.periodStart || !llmUsage.value?.periodEnd) return ''
  return `${fmtPeriodDate(llmUsage.value.periodStart)} – ${fmtPeriodDate(llmUsage.value.periodEnd)}`
})

const uptimeDisplay = computed(() => {
  if (!box.value?.updatedAt || box.value.status !== 'running') return ''
  // updatedAt is approximate last-seen; use createdAt as proxy for uptime
  // Only show if box has been running for a meaningful time
  return ''
})

const maxDiskDisplay = computed(() => {
  if (!vmUsage.value) return '—'
  // Max disk comes from the plan; use the provisioned size as a proxy when unknown
  return vmUsage.value.display.disk_provisioned || '—'
})

const vmsUsedDisplay = computed(() => {
  if (!profile.value) return '—'
  const ti = profile.value.teamInfo
  if (ti) return `${ti.boxCount} / ${ti.maxBoxes}`
  return `${profile.value.boxes.length}`
})

const overageDisplay = computed(() => {
  const u = vmUsage.value
  if (!u) return '—'
  const parts: string[] = []
  if (u.display.overage_disk) parts.push(`disk: ${u.display.overage_disk}`)
  if (u.display.overage_bandwidth) parts.push(`bandwidth: ${u.display.overage_bandwidth}`)
  return parts.join(', ') || 'none'
})

function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function formatCPUTime(seconds: number): string {
  if (!seconds) return '0s'
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  return `${(seconds / 3600).toFixed(1)}h`
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const data = await fetchDashboard()
    const found = data.boxes.find(b => b.name === vmName.value) ?? null
    box.value = found
    hasTeam.value = data.hasTeam || false
    periodStart.value = data.billing.periodStart as unknown as string
    periodEnd.value = data.billing.periodEnd as unknown as string

    if (found) {
      // Load usage and profile in parallel, non-blocking
      fetchVMUsage(
        data.billing.periodStart as unknown as string,
        data.billing.periodEnd as unknown as string,
      ).then(usageResp => {
        const entry = usageResp.metrics?.find(m => m.vm_name === vmName.value) ?? null
        vmUsage.value = entry
        usageLoading.value = false
      }).catch(() => { usageLoading.value = false })

      fetchProfile().then(p => {
        profile.value = p
        profileLoading.value = false
      }).catch(() => { profileLoading.value = false })

      fetchBoxLLMUsage(vmName.value).then(u => {
        llmUsage.value = u
      }).catch(err => {
        console.error('Failed to load VM LLM usage:', err)
      })
    } else {
      usageLoading.value = false
      profileLoading.value = false
    }
  } catch (err: any) {
    loadError.value = err.message || 'Failed to load VM'
  } finally {
    loading.value = false
  }
}

// Command modal
const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  displayCommand: '',
  commandPrefix: '',
  inputPlaceholder: '',
  defaultValue: '',
  danger: false,
  successFormat: '',
  suggestions: [] as string[],
})

function openModal(opts: Partial<typeof modal>) {
  Object.assign(modal, {
    visible: true,
    title: '',
    description: '',
    command: '',
    displayCommand: '',
    commandPrefix: '',
    inputPlaceholder: '',
    defaultValue: '',
    danger: false,
    successFormat: '',
    suggestions: [],
    ...opts,
  })
  drawerOpen.value = false
}

function doAction(type: string) {
  if (!box.value) return
  const q = shellQuote(box.value.name)
  switch (type) {
    case 'share':
      openModal({
        title: 'Share VM',
        commandPrefix: `share add ${q}`,
        inputPlaceholder: 'user@example.com',
        description: 'Sharing allows the given user to access this VM\'s web server.',
      })
      break
    case 'share-team': {
      if (box.value.isTeamShared) {
        openModal({ title: 'Unshare from Team', command: `share remove ${q} team`, description: 'Remove team access.', danger: true })
      } else {
        openModal({ title: 'Share with Team', command: `share add ${q} team`, description: 'Share with all team members.' })
      }
      break
    }
    case 'share-link':
      openModal({
        title: 'Create Share Link',
        command: `share add-link ${q} --json`,
        displayCommand: `share add-link ${q}`,
        description: 'A share link allows anyone with the link to access this VM.',
        successFormat: 'share-link',
      })
      break
    case 'copy':
      openModal({ title: 'Copy VM', command: `cp ${q}`, description: 'Create a full copy of this VM.' })
      break
    case 'rename':
      openModal({ title: 'Rename VM', commandPrefix: `rename ${q}`, inputPlaceholder: 'new-name', description: 'Give this VM a new name.' })
      break
    case 'restart':
      openModal({ title: 'Restart VM', command: `restart ${q}`, description: 'Restart this VM.' })
      break
    case 'delete':
      openModal({ title: 'Delete VM', command: `rm ${q}`, danger: true, description: 'Permanently delete this VM and all its data. This cannot be undone.' })
      break
  }
}

async function onModalSuccess() {
  if (modal.title === 'Delete VM') {
    // Navigate back to VM list after deletion
    router.push('/')
    return
  }
  if (modal.title === 'Rename VM') {
    // Will navigate since name changed
    router.push('/')
    return
  }
  await load()
}

function onEscapeKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (drawerOpen.value) { drawerOpen.value = false; return }
  if (editorModalOpen.value) { editorModalOpen.value = false; return }
}

function onDocumentClick() {
  drawerOpen.value = false
}

onMounted(() => {
  load()
  document.addEventListener('keydown', onEscapeKey)
  document.addEventListener('click', onDocumentClick)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEscapeKey)
  document.removeEventListener('click', onDocumentClick)
})
</script>

<style scoped>
.vm-detail-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-width: 900px;
}

.vm-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 24px;
}

/* Breadcrumbs */
.breadcrumbs {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-color-muted);
}

.breadcrumb-link {
  color: var(--text-color-secondary);
  text-decoration: none;
}

.breadcrumb-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

.breadcrumb-sep {
  color: var(--text-color-muted);
}

.breadcrumb-current {
  color: var(--text-color);
  font-weight: 500;
}

/* Loading / error */
.loading-state {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.error-state {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  text-align: center;
  padding: 48px;
  color: var(--danger-text);
}

.error-state p {
  margin-bottom: 12px;
}

/* Header */
.vm-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}

.vm-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.vm-name {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}

.badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.badge-public {
  background: var(--badge-public-bg);
  color: var(--badge-public-text);
}

.badge-team {
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
}

.vm-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.action-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 6px 12px;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  font-size: 13px;
  font-family: inherit;
  line-height: 1;
  color: var(--btn-text);
  cursor: pointer;
  text-decoration: none;
  transition: all 0.15s;
  white-space: nowrap;
  box-sizing: border-box;
}

.action-pill:hover, .action-pill.active {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
  text-decoration: none;
}

.action-pill i {
  font-size: 12px;
}

/* Junk drawer */
.junk-drawer-wrap {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.junk-btn {
  padding: 6px 12px;
}

.junk-drawer {
  position: absolute;
  right: 0;
  top: calc(100% + 6px);
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0,0,0,0.12);
  min-width: 160px;
  z-index: 100;
  overflow: hidden;
}

.drawer-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 9px 14px;
  background: none;
  border: none;
  font-size: 13px;
  font-family: inherit;
  color: var(--text-color);
  cursor: pointer;
  text-align: left;
  transition: background 0.1s;
}

.drawer-item:hover {
  background: var(--surface-inset);
}

.drawer-item.danger {
  color: var(--danger-color);
}

.drawer-item i {
  font-size: 12px;
  width: 14px;
  text-align: center;
  color: var(--text-color-muted);
}

.drawer-item.danger i {
  color: var(--danger-color);
}

/* Subtitle */
.vm-subtitle {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-color-muted);
  flex-wrap: wrap;
}

.sep {
  opacity: 0.5;
}

/* SSH */
.ssh-row {
  display: flex;
  align-items: center;
  gap: 8px;
  background: var(--surface-inset, var(--surface-ground));
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 8px 12px;
}

.ssh-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  flex-shrink: 0;
}

.ssh-cmd {
  flex: 1;
  font-size: 12px;
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
  color: var(--code-text);
  word-break: break-all;
}

/* Tags */
.tags-row {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.tag {
  font-size: 11px;
  color: var(--tag-text);
  background: var(--tag-bg);
  padding: 2px 8px;
  border-radius: 3px;
}

/* Billing cards */
/* Section divider */
.section-divider {
  border: none;
  border-top: 1px solid var(--surface-border);
  margin: 0;
}

/* Billing columns */
.billing-columns {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}

.billing-section {
  display: flex;
  flex-direction: column;
}

.section-heading {
  font-size: 11px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-color-secondary);
  margin-bottom: 10px;
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.section-heading-sub {
  font-size: 10px;
  font-weight: 400;
  letter-spacing: 0;
  color: var(--text-color-muted);
  text-transform: none;
}

.card-loading {
  color: var(--text-color-muted);
  font-size: 13px;
  padding: 8px 0;
}

.card-empty {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 4px 0;
}

.card-row {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 8px;
  padding: 4px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--surface-border);
}

.card-row:last-child {
  border-bottom: none;
}

.card-label {
  color: var(--text-color-muted);
  font-size: 12px;
}

.card-value {
  font-weight: 500;
  color: var(--text-color);
  font-size: 12px;
  text-align: right;
}

.card-row.overage .card-value {
  color: var(--danger-color);
}

.llm-usage-section {
  border-top: 1px solid var(--surface-border);
  padding-top: 1rem;
  margin-top: 0.5rem;
}
.card-row-total {
  font-weight: 600;
}
/* Placeholder sections */
.section-placeholder {
  background: var(--surface-card);
  border: 1px dashed var(--surface-border);
  border-radius: 8px;
  padding: 24px 20px;
}

.placeholder-title {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-color-muted);
  margin-bottom: 8px;
}

.placeholder-body {
  font-size: 13px;
  color: var(--text-color-muted);
}

/* Editor modal */
.modal-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  background: var(--surface-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-dialog {
  background: var(--surface-card);
  border-radius: 12px;
  width: 480px;
  max-width: 90vw;
  box-shadow: 0 20px 40px rgba(0,0,0,0.15);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--surface-border);
}

.modal-title {
  font-weight: 600;
  font-size: 15px;
}

.modal-close {
  background: none;
  border: none;
  font-size: 22px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.modal-body {
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.modal-footer {
  padding: 12px 20px;
  border-top: 1px solid var(--surface-border);
  display: flex;
  justify-content: flex-end;
}

.field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  margin-bottom: 4px;
}

.field-input {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--input-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  background: var(--input-bg);
  color: var(--input-text);
  box-sizing: border-box;
}

.field-input:focus {
  border-color: var(--primary-color);
  outline: none;
}

.editor-picker {
  display: flex;
}

.editor-btn {
  flex: 1;
  padding: 7px 12px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  background: var(--input-bg);
  color: var(--text-color-secondary);
  border: 1px solid var(--input-border);
  transition: all 0.15s;
}

.editor-btn:first-child { border-radius: 6px 0 0 6px; }
.editor-btn:last-child { border-radius: 0 6px 6px 0; }
.editor-btn:not(:first-child) { border-left: none; }

.editor-btn.active {
  background: var(--text-color);
  color: var(--surface-ground);
  border-color: var(--text-color);
}

.editor-url-row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.editor-url {
  flex: 1;
  font-size: 12px;
  color: var(--code-text);
  background: var(--code-bg);
  padding: 6px 10px;
  border-radius: 4px;
  word-break: break-all;
  min-width: 0;
}

/* Buttons */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 16px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
  text-decoration: none;
  transition: all 0.15s;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover {
  filter: brightness(1.1);
  text-decoration: none;
}

.btn-secondary {
  background: var(--btn-bg);
  color: var(--btn-text);
  border-color: var(--btn-border);
}

.btn-secondary:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  text-decoration: none;
}

@media (max-width: 640px) {
  .billing-columns {
    grid-template-columns: 1fr;
  }
  .vm-header {
    flex-direction: column;
    align-items: flex-start;
  }
  .vm-actions {
    width: 100%;
  }
}
</style>

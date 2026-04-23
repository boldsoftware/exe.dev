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
      <!-- Header: status dot + name + badges -->
      <div class="vm-header">
        <span ref="emojiAnchor" class="emoji-anchor">
          <StatusDot
            :status="box.status"
            :emoji="box.emoji"
            clickable
            @edit="openEmojiPicker"
          />
        </span>
        <EmojiPicker
          :open="emojiOpen"
          :anchor-el="emojiAnchor"
          :current="box.emoji"
          :saving="emojiSaving"
          :error-msg="emojiError"
          @close="emojiOpen = false"
          @pick="onEmojiPick"
        />
        <h1 class="vm-name">{{ box.name }}</h1>
        <span v-if="box.totalShareCount > 0" class="badge badge-share" :title="`Shared with ${box.sharedUserCount} user(s) and ${box.shareLinkCount} link(s)`">
          👥 {{ box.totalShareCount }}
        </span>
        <span v-if="box.isTeamShared" class="badge badge-team">TEAM</span>
        <span v-if="box.proxyShare === 'public'" class="badge badge-public">PUBLIC</span>
      </div>

      <!-- Shared detail sections (same layout as the VM list expanded row) -->
      <VMDetailSections
        :box="box"
        :has-team="hasTeam"
        :show-usage-panel="false"
        @action="onDetailAction"
      />

      <!-- Creation Log -->
      <CreationLog v-if="box.status === 'creating'" :hostname="box.name" :streaming="true" @done="load" @fail="load" />
      <div v-else-if="box.hasCreationLog && showCreationLog" class="creation-log-wrap">
        <CreationLog :hostname="box.name" :streaming="false" />
      </div>
      <div v-else-if="box.hasCreationLog && !showCreationLog" class="creation-log-button">
        <button class="btn btn-secondary" @click="showCreationLog = true">View Creation Log</button>
      </div>

      <!-- Provisioned -->
      <div v-if="box.status === 'running' && provItems.length" class="provisioned-section">
        <div class="metrics-section-heading">Provisioned</div>
        <div class="provisioned-bar">
          <template v-for="(item, i) in provItems" :key="item.label">
            <div v-if="i > 0" class="prov-sep"></div>
            <div class="prov-item">
              <span class="prov-label">{{ item.label }}</span>
              <span class="prov-value">{{ item.value }}</span>
            </div>
          </template>
        </div>
      </div>

      <!-- Usage History Charts - temporarily hidden pending metrics updates
      <UsageChart :vm-name="box.name" :vm-status="box.status" />
      -->

      <div class="section-divider"></div>

      <!-- Billing Usage -->
      <div class="billing-section">
        <div class="section-heading">Compute Usage<span v-if="periodLabel" class="section-heading-sub">{{ periodLabel }}</span></div>
          <div v-if="usageLoading" class="card-loading"><i class="pi pi-spin pi-spinner"></i></div>
          <template v-else-if="vmUsage">
            <div class="card-row">
              <span class="card-label">Disk included</span>
              <span class="card-value">{{ vmUsage.display.included_disk || '—' }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">Disk provisioned</span>
              <span class="card-value">{{ vmUsage.display.disk_provisioned || '—' }}</span>
            </div>
            <div v-if="vmUsage.display.overage_disk" class="card-row overage">
              <span class="card-label">Disk overage</span>
              <span class="card-value">{{ vmUsage.display.overage_disk }}</span>
            </div>
            <div class="card-row card-row-spacer"></div>
            <div class="card-row">
              <span class="card-label">Bandwidth included</span>
              <span class="card-value">{{ vmUsage.display.included_bandwidth || '—' }}</span>
            </div>
            <div class="card-row">
              <span class="card-label">Bandwidth used</span>
              <span class="card-value">{{ vmUsage.display.bandwidth || '—' }}</span>
            </div>
            <div v-if="vmUsage.display.overage_bandwidth" class="card-row overage">
              <span class="card-label">Bandwidth overage</span>
              <span class="card-value">{{ vmUsage.display.overage_bandwidth }}</span>
            </div>
            <div class="card-row card-row-spacer"></div>
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

      <!-- Shelley Usage for this VM -->
      <div v-if="llmUsage && llmUsage.models.length" class="billing-section llm-usage-section">
        <div class="section-heading">Shelley Usage<span v-if="llmPeriodLabel" class="section-heading-sub">{{ llmPeriodLabel }}</span></div>
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
import { ref, computed, onMounted, onBeforeUnmount, reactive, defineAsyncComponent } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  fetchDashboard,
  fetchVMUsage,
  fetchBoxLLMUsage,
  fetchVMLiveMetrics,
  type BoxInfo,
  type VMUsageEntry,
  type BoxLLMUsageResponse,
  type VMLiveMetrics,
  shellQuote,
} from '../api/client'
import StatusDot from '../components/StatusDot.vue'
import EmojiPicker from '../components/EmojiPicker.vue'
import { useCommand } from '../composables/useCommand'
import CopyButton from '../components/CopyButton.vue'
import CommandModal from '../components/CommandModal.vue'
import UsageChart from '../components/UsageChart.vue'
import VMDetailSections from '../components/VMDetailSections.vue'
const CreationLog = defineAsyncComponent(() => import('../components/CreationLog.vue'))

const route = useRoute()
const router = useRouter()

const vmName = computed(() => route.params.name as string)

const loading = ref(true)
const loadError = ref('')
const box = ref<BoxInfo | null>(null)
const hasTeam = ref(false)
const allBoxes = ref<BoxInfo[]>([])

// Emoji picker
const emojiAnchor = ref<HTMLElement | null>(null)
const emojiOpen = ref(false)
const emojiSaving = ref(false)
const emojiError = ref('')
const emojiCmd = useCommand()

function openEmojiPicker() {
  emojiError.value = ''
  emojiOpen.value = true
}

async function onEmojiPick(emoji: string) {
  if (!box.value) return
  if (!emoji || emoji === box.value.emoji) {
    emojiOpen.value = false
    return
  }
  emojiSaving.value = true
  emojiError.value = ''
  const name = box.value.name
  const cmd = `emoji ${shellQuote(name)} ${shellQuote(emoji)}`
  const result = await emojiCmd.execute(cmd)
  emojiSaving.value = false
  if (result.success) {
    emojiOpen.value = false
    if (box.value) box.value = { ...box.value, emoji }
    load()
  } else {
    emojiError.value = result.output || result.error || 'Failed to update emoji'
  }
}

// Billing / usage
const usageLoading = ref(true)
const vmUsage = ref<VMUsageEntry | null>(null)
const periodStart = ref('')
const periodEnd = ref('')

// LLM usage
const llmUsage = ref<BoxLLMUsageResponse | null>(null)

// Provisioned specs (fetched once from live metrics endpoint)
const liveMetrics = ref<VMLiveMetrics | null>(null)

// Creation log
const showCreationLog = ref(false)

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
  return new Date(s).toLocaleDateString('en-US', { month: 'long', day: 'numeric', timeZone: 'UTC' })
}

const periodLabel = computed(() => {
  if (!periodStart.value || !periodEnd.value) return ''
  return `${fmtPeriodDate(periodStart.value)} – ${fmtPeriodDate(periodEnd.value)}`
})

const llmPeriodLabel = computed(() => {
  if (!llmUsage.value?.periodStart || !llmUsage.value?.periodEnd) return ''
  return `${fmtPeriodDate(llmUsage.value.periodStart)} – ${fmtPeriodDate(llmUsage.value.periodEnd)}`
})


function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
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

// --- Provisioned specs (only show fields that have data) ---
const provItems = computed(() => {
  if (!liveMetrics.value) return []
  const items: { label: string; value: string }[] = []
  if (liveMetrics.value.cpus) items.push({ label: 'vCPUs', value: String(liveMetrics.value.cpus) })
  if (liveMetrics.value.mem_capacity_bytes) items.push({ label: 'Memory', value: roundedMemoryGB(liveMetrics.value.mem_capacity_bytes) })
  if (liveMetrics.value.disk_capacity_bytes) items.push({ label: 'Disk', value: roundedGB(liveMetrics.value.disk_capacity_bytes) })
  return items
})

// Round memory to the nearest standard provisioned size (power-of-2 GiB).
// The reported value is often slightly under due to kernel/firmware overhead
// (e.g. 7.2 GiB for an 8 GB VM).
function roundedMemoryGB(bytes: number): string {
  if (!bytes) return '0 B'
  const gb = bytes / (1024 * 1024 * 1024)
  const sizes = [1, 2, 4, 8, 16, 32, 64, 128]
  for (const s of sizes) {
    if (gb <= s) return `${s} GB`
  }
  return `${Math.ceil(gb)} GB`
}

function roundedGB(bytes: number): string {
  if (!bytes) return '0 B'
  const gb = bytes / (1024 * 1024 * 1024)
  if (gb >= 1) return `${Math.round(gb)} GB`
  const mb = bytes / (1024 * 1024)
  if (mb >= 1) return `${Math.round(mb)} MB`
  const kb = bytes / 1024
  return `${Math.round(kb)} KB`
}

async function fetchProvisionedSpecs() {
  try {
    liveMetrics.value = await fetchVMLiveMetrics(vmName.value)
  } catch {
    // Silently ignore — provisioned bar just won't show
  }
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const data = await fetchDashboard()
    const found = data.boxes.find(b => b.name === vmName.value) ?? null
    box.value = found
    allBoxes.value = data.boxes
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

      fetchBoxLLMUsage(vmName.value).then(u => {
        llmUsage.value = u
      }).catch(err => {
        console.error('Failed to load VM LLM usage:', err)
      })
      // Fetch provisioned specs (single request, no polling)
      if (found.status === 'running') {
        fetchProvisionedSpecs()
      }
    } else {
      usageLoading.value = false
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
}

function onDetailAction(a: { type: string; boxName: string; extra?: any }) {
  if (a.type === 'open-editor') {
    editorModalOpen.value = true
    return
  }
  doAction(a.type, a.extra)
}

function doAction(type: string, extra?: any) {
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
    case 'remove-share':
      openModal({
        title: 'Remove Access',
        command: `share remove ${q} ${shellQuote(extra)}`,
        description: 'Revoke this user\'s access to the VM\'s web server.',
        danger: true,
      })
      break
    case 'remove-share-link':
      openModal({
        title: 'Remove Share Link',
        command: `share remove-link ${q} ${shellQuote(extra)}`,
        description: 'Revoke this share link. Users who were added explicitly will keep access.',
        danger: true,
      })
      break
    case 'add-tag': {
      // Suggest existing tags that aren't already on this VM.
      const existing = new Set(box.value.displayTags || [])
      const allKnownTags = new Set<string>()
      for (const b of allBoxes.value) {
        for (const t of b.displayTags || []) allKnownTags.add(t)
      }
      const suggestions = [...allKnownTags].filter(t => !existing.has(t)).sort((a, b) => a.localeCompare(b))
      openModal({
        title: 'Add Tag',
        commandPrefix: `tag ${q}`,
        inputPlaceholder: 'tag name (e.g. prod)',
        description: 'Tags are usually used for attaching integrations and organization.',
        suggestions,
      })
      break
    }
    case 'remove-tag':
      openModal({
        title: 'Remove Tag',
        command: `tag -d ${q} ${shellQuote(extra)}`,
        description: 'Remove this tag from the VM.',
        danger: true,
      })
      break
    case 'set-public':
      openModal({
        title: 'Make Public',
        command: `share set-public ${q}`,
        description: 'Anyone with the link can access this VM.',
      })
      break
    case 'set-private':
      openModal({
        title: 'Make Private',
        command: `share set-private ${q}`,
        description: 'Only you and shared users can access this VM.',
      })
      break

    case 'set-port': {
      const proxyURL = extra || ''
      let desc = 'The proxy port is the port on your VM that the HTTPS proxy connects to.'
      if (proxyURL) {
        try {
          const u = new URL(proxyURL)
          if (u.protocol === 'http:' || u.protocol === 'https:') {
            const safe = u.href.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
            desc = `The proxy port is the port on your VM that <a href="${safe}" target="_blank" rel="noopener noreferrer"><b>${safe}</b></a> connects to.`
          }
        } catch { /* invalid URL, use default description */ }
      }
      openModal({
        title: 'Set Proxy Port',
        commandPrefix: `share port ${q}`,
        inputPlaceholder: 'port (e.g. 8080)',
        description: desc,
      })
      break
    }
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
  if (editorModalOpen.value) { editorModalOpen.value = false; return }
}

onMounted(() => {
  load()
  document.addEventListener('keydown', onEscapeKey)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEscapeKey)
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
  gap: 10px;
  flex-wrap: wrap;
}

.emoji-anchor {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}

.vm-name {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}

.badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
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

.badge-share {
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
  font-weight: 500;
}

/* Creation Log */
.creation-log-wrap {
  margin: 0;
}

.creation-log-button {
  display: flex;
  align-items: center;
}

/* Billing cards */
/* Section divider */
.section-divider {
  border: none;
  border-top: 1px solid var(--surface-border);
  margin: 0;
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

.card-row-spacer {
  border-bottom: none;
  padding: 2px 0;
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

/* --- Provisioned bar --- */
.metrics-section-heading {
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: var(--text-color-muted);
  text-transform: uppercase;
  margin-bottom: 10px;
}

.provisioned-bar {
  display: flex;
  align-items: center;
  gap: 24px;
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 14px 20px;
  background: var(--surface-card);
  margin-bottom: 16px;
}

.prov-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.prov-label {
  font-size: 10px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.prov-value {
  font-size: 16px;
  font-weight: 700;
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
  color: var(--text-color);
}

.prov-sep {
  width: 1px;
  height: 24px;
  background: var(--surface-border);
}

@media (max-width: 768px) {
  .provisioned-bar {
    flex-wrap: wrap;
    gap: 12px;
  }
  .prov-sep {
    display: none;
  }
}



</style>

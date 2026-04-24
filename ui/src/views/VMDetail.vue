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

      <!-- Resource Pool (live) -->
      <div v-if="thisVMPool" class="pool-section">
        <div class="pool-title">Resource Pool (live)</div>

        <div class="pool-row">
          <span class="pool-label">vCPU</span>
          <div class="pool-track">
            <div class="pool-seg pool-seg-other" :style="{ left: '0', width: poolPct(thisVMPool.otherCPU, thisVMPool.maxCPU) }"></div>
            <div class="pool-seg pool-seg-this" :style="{ left: poolPct(thisVMPool.otherCPU, thisVMPool.maxCPU), width: poolPct(thisVMPool.thisCPU, thisVMPool.maxCPU) }"></div>
          </div>
          <span class="pool-values">{{ thisVMPool.thisCPU.toFixed(1) }} of {{ thisVMPool.maxCPU }}</span>
        </div>

        <div v-if="thisVMPool.maxMem > 0" class="pool-row">
          <span class="pool-label">Memory</span>
          <div class="pool-track">
            <div class="pool-seg pool-seg-other" :style="{ left: '0', width: poolPct(thisVMPool.otherMem, thisVMPool.maxMem) }"></div>
            <div class="pool-seg pool-seg-this" :style="{ left: poolPct(thisVMPool.otherMem, thisVMPool.maxMem), width: poolPct(thisVMPool.thisMem, thisVMPool.maxMem) }"></div>
          </div>
          <span class="pool-values">{{ poolFmtGB(thisVMPool.thisMem) }} of {{ poolFmtGB(thisVMPool.maxMem) }}</span>
        </div>

        <div class="pool-legend">
          <span><span class="legend-dot this"></span>{{ thisVMPool.vmName }}</span>
          <span><span class="legend-dot other"></span>other VMs</span>
        </div>
      </div>



      <div class="section-divider"></div>

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

    <!-- Resize Disk Modal -->
    <ResizeDiskModal
      :visible="resizeDiskOpen"
      :box-name="box ? box.name : ''"
      @close="resizeDiskOpen = false"
      @success="onResizeDiskSuccess"
    />

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
  fetchBoxLLMUsage,
  fetchVMLiveMetrics,
  fetchVMsLive,
  type BoxInfo,
  type BoxLLMUsageResponse,
  type VMLiveMetrics,
  type VMsLiveResponse,
  shellQuote,
} from '../api/client'
import StatusDot from '../components/StatusDot.vue'
import EmojiPicker from '../components/EmojiPicker.vue'
import { useCommand } from '../composables/useCommand'
import CopyButton from '../components/CopyButton.vue'
import CommandModal from '../components/CommandModal.vue'
import ResizeDiskModal from '../components/ResizeDiskModal.vue'
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

// LLM usage
const llmUsage = ref<BoxLLMUsageResponse | null>(null)

// Provisioned specs (fetched once from live metrics endpoint)
const liveMetrics = ref<VMLiveMetrics | null>(null)
const poolData = ref<VMsLiveResponse | null>(null)


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

const llmPeriodLabel = computed(() => {
  if (!llmUsage.value?.periodStart || !llmUsage.value?.periodEnd) return ''
  return `${fmtPeriodDate(llmUsage.value.periodStart)} – ${fmtPeriodDate(llmUsage.value.periodEnd)}`
})




async function fetchProvisionedSpecs() {
  try {
    liveMetrics.value = await fetchVMLiveMetrics(vmName.value)
  } catch {
    // Silently ignore — provisioned bar just won't show
  }
  try {
    poolData.value = await fetchVMsLive()
  } catch {
    // Silently ignore — pool section just won't show
  }
}

// Pool context computations for this VM
const thisVMPool = computed(() => {
  if (!poolData.value || !liveMetrics.value) return null
  const thisVM = poolData.value.vms.find(v => v.name === vmName.value)
  if (!thisVM) return null
  const pool = poolData.value.pool
  if (pool.cpu_max === 0) return null // unlimited plan, no pool bar

  const thisCPU = thisVM.cpu_percent / 100
  const otherCPU = Math.max(0, pool.cpu_used - thisCPU)
  const thisMem = thisVM.mem_bytes
  const otherMem = Math.max(0, pool.mem_used_bytes - thisMem)

  return {
    thisCPU,
    otherCPU,
    totalCPU: pool.cpu_used,
    maxCPU: pool.cpu_max,
    thisMem,
    otherMem,
    totalMem: pool.mem_used_bytes,
    maxMem: pool.mem_max_bytes,
    vmName: thisVM.name,
  }
})

function poolPct(value: number, max: number): string {
  if (max === 0) return '0%'
  return Math.min((value / max) * 100, 100) + '%'
}

function poolFmtGB(bytes: number): string {
  const gb = bytes / (1024 * 1024 * 1024)
  if (gb >= 1) return gb.toFixed(1) + ' GB'
  const mb = bytes / (1024 * 1024)
  return mb.toFixed(0) + ' MB'
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

    if (found) {
      // Load LLM usage and profile in parallel, non-blocking
      fetchBoxLLMUsage(vmName.value).then(u => {
        llmUsage.value = u
      }).catch(err => {
        console.error('Failed to load VM LLM usage:', err)
      })
      // Fetch provisioned specs (single request, no polling)
      if (found.status === 'running') {
        fetchProvisionedSpecs()
      }
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

const resizeDiskOpen = ref(false)

function onDetailAction(a: { type: string; boxName: string; extra?: any }) {
  if (a.type === 'open-editor') {
    editorModalOpen.value = true
    return
  }
  if (a.type === 'resize-disk') {
    resizeDiskOpen.value = true
    return
  }
  doAction(a.type, a.extra)
}

async function onResizeDiskSuccess() {
  try {
    liveMetrics.value = await fetchVMLiveMetrics(vmName.value)
  } catch { /* ignore refresh errors */ }
  await load()
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

.llm-usage-section {
  padding-top: 0;
  margin-top: 0;
}
.card-row-total {
  font-weight: 600;
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

/* Pool context section */
.pool-section {
  margin-top: 16px;
}
.pool-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}
.pool-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}
.pool-row:last-of-type { margin-bottom: 0; }
.pool-label {
  width: 55px;
  font-size: 12px;
  color: var(--text-color-secondary);
  flex-shrink: 0;
}
.pool-track {
  flex: 1;
  height: 12px;
  background: var(--surface-border);
  border-radius: 3px;
  overflow: hidden;
  position: relative;
}
.pool-seg {
  height: 100%;
  position: absolute;
  top: 0;
}
.pool-seg-this {
  background: var(--primary-color, #0969da);
  opacity: 0.85;
  z-index: 2;
}
.pool-seg-other {
  background: var(--text-color-secondary, #c8d1da);
  opacity: 0.4;
  z-index: 1;
}
.pool-values {
  min-width: 140px;
  text-align: right;
  font-size: 11px;
  flex-shrink: 0;
  font-weight: 500;
  white-space: nowrap;
}
.pool-legend {
  display: flex;
  gap: 16px;
  margin-top: 10px;
  font-size: 11px;
  color: var(--text-color-secondary);
}
.legend-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  margin-right: 4px;
  vertical-align: middle;
}
.legend-dot.this {
  background: var(--primary-color, #0969da);
  opacity: 0.85;
}
.legend-dot.other {
  background: var(--text-color-secondary, #c8d1da);
  opacity: 0.4;
}

@media (max-width: 768px) {
  .pool-values {
    min-width: 100px;
    font-size: 10px;
  }
}



</style>

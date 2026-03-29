<template>
  <div class="vm-list-page">
    <!-- Header -->
    <div class="section-header">
      <div class="section-left">
        <h2 class="section-title">My VMs</h2>
        <router-link to="/new" class="new-btn">+ New</router-link>
        <button class="new-btn" @click="promptModalOpen = true">✨ Prompt</button>
      </div>
      <div class="search-box">
        <i class="pi pi-search search-icon"></i>
        <input
          v-model="searchQuery"
          type="text"
          placeholder="Filter VMs..."
          class="search-input"
        />
        <button v-if="searchQuery" class="search-clear" @click="searchQuery = ''">&times;</button>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <!-- Error -->
    <div v-else-if="loadError" class="error-state">
      <p>Failed to load VMs: {{ loadError }}</p>
      <button class="new-btn" @click="loadDashboard">Retry</button>
    </div>

    <!-- VM List -->
    <div v-else-if="filteredBoxes.length > 0" class="boxes-list">
      <VMCard
        v-for="box in filteredBoxes"
        :key="box.name"
        :box="box"
        :expanded="expandedBoxes.has(box.name)"
        @toggle="toggleExpand(box.name)"
        @action="handleAction"
      />
    </div>
    <div v-else-if="!loading && boxes.length === 0" class="empty-state">
      <p>No VMs yet. Create one with:</p>
      <code class="ssh-cmd">{{ sshCommand }} new --name=myname</code>
    </div>
    <div v-else-if="!loading && filteredBoxes.length === 0" class="empty-state">
      <p>No VMs match "{{ searchQuery }}"</p>
    </div>

    <!-- Shared VMs -->
    <div v-if="sharedBoxes.length > 0" class="shared-section">
      <h2 class="section-title">Shared with you</h2>
      <div class="boxes-list">
        <div v-for="box in sharedBoxes" :key="box.name" class="shared-row">
          <StatusDot status="running" />
          <span class="box-name">{{ box.name }}</span>
          <span class="shared-owner">by {{ box.ownerEmail }}</span>
          <a :href="box.proxyURL" class="action-link" target="_blank" rel="noopener noreferrer">
            <i class="pi pi-external-link"></i>
          </a>
        </div>
      </div>
    </div>

    <!-- Team VMs -->
    <div v-if="teamBoxes.length > 0" class="shared-section">
      <h2 class="section-title">Team VMs</h2>
      <div class="boxes-list">
        <div v-for="box in teamBoxes" :key="box.name" class="shared-row">
          <StatusDot :status="box.status" />
          <span class="box-name">{{ box.name }}</span>
          <span class="shared-owner">by {{ box.creatorEmail }}</span>
          <span v-if="box.displayTags && box.displayTags.length" class="box-tags">
            <span v-for="tag in box.displayTags" :key="tag" class="tag">#{{ tag }}</span>
          </span>
          <CopyButton :text="box.sshCommand" title="SSH" />
          <a :href="box.proxyURL" class="action-link" target="_blank" rel="noopener noreferrer">
            <i class="pi pi-external-link"></i>
          </a>
        </div>
      </div>
    </div>

    <!-- Command Modal -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :default-value="modal.defaultValue"
      :danger="modal.danger"
      @close="closeModal"
      @success="onModalSuccess"
    />

    <!-- Editor Picker Modal -->
    <div v-if="editorModalOpen" class="prompt-overlay" @click="editorModalOpen = false">
      <div class="prompt-modal" role="dialog" aria-modal="true" @click.stop>
        <div class="prompt-header">
          <span class="prompt-title">Open in Editor</span>
          <button class="prompt-close" aria-label="Close" @click="editorModalOpen = false">&times;</button>
        </div>
        <div class="prompt-body">
          <div>
            <div class="prompt-field-label">Editor</div>
            <div class="editor-picker">
              <button
                v-for="ed in editors"
                :key="ed.value"
                type="button"
                class="editor-picker-btn"
                :class="{ active: editorChoice === ed.value }"
                @click="editorChoice = ed.value; saveEditorChoice()"
              >{{ ed.label }}</button>
            </div>
          </div>
          <div>
            <div class="prompt-field-label">Working Directory</div>
            <input v-model="editorDir" class="prompt-input" style="min-height: auto;" />
          </div>
          <div>
            <div class="prompt-field-label">URL</div>
            <div class="editor-url-row">
              <code class="editor-url">{{ editorURL }}</code>
              <CopyButton :text="editorURL" title="Copy Link" />
            </div>
          </div>
        </div>
        <div class="prompt-footer">
          <a :href="editorURL" class="prompt-submit" style="text-decoration:none; text-align:center;">Open Editor</a>
        </div>
      </div>
    </div>

    <!-- Prompt Shelley Modal -->
    <div v-if="promptModalOpen" class="prompt-overlay" @click="promptModalOpen = false">
      <div class="prompt-modal" role="dialog" aria-modal="true" @click.stop>
        <div class="prompt-header">
          <span class="prompt-title">Prompt Shelley</span>
          <button class="prompt-close" aria-label="Close" @click="promptModalOpen = false">&times;</button>
        </div>
        <form @submit.prevent="submitPrompt">
          <div class="prompt-body">
            <div>
              <div class="prompt-field-label">VM</div>
              <select v-model="promptVM" class="prompt-select">
                <option v-for="b in shelleyBoxes" :key="b.name" :value="b.name">{{ b.name }}</option>
                <option value="__new__">+ New VM</option>
              </select>
            </div>
            <div>
              <div class="prompt-field-label">Prompt</div>
              <textarea v-model="promptText" class="prompt-input" placeholder="Ask Shelley to do something..." autocomplete="off"></textarea>
            </div>
          </div>
          <div class="prompt-footer">
            <div v-if="promptError" class="prompt-error">{{ promptError }}</div>
            <button type="submit" class="prompt-submit" :disabled="promptSending">
              {{ promptSending ? 'Sending...' : 'Send' }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, reactive, watch } from 'vue'
import { fetchDashboard, runCommand, type BoxInfo, type SharedBoxInfo, type TeamBoxInfo, shellQuote } from '../api/client'
import VMCard from '../components/VMCard.vue'
import StatusDot from '../components/StatusDot.vue'
import CopyButton from '../components/CopyButton.vue'
import CommandModal from '../components/CommandModal.vue'

const loading = ref(true)
const loadError = ref('')
const boxes = ref<BoxInfo[]>([])
const sharedBoxes = ref<SharedBoxInfo[]>([])
const teamBoxes = ref<TeamBoxInfo[]>([])
const searchQuery = ref(new URLSearchParams(window.location.search).get('filter') || '')
const expandedBoxes = ref(new Set<string>())
const sshCommand = ref('')

// Editor picker state
const editorModalOpen = ref(false)
const editorBoxName = ref('')
const editorChoice = ref(localStorage.getItem('preferred-editor') || 'vscode')
const editorDir = ref('/home/exedev')
const editorURL = computed(() => {
  const box = boxes.value.find(b => b.name === editorBoxName.value)
  if (!box) return ''
  const baseURL = box.vscodeURL || ''
  // Extract connection string (e.g. boxname@host:port) from the vscode URL
  const match = baseURL.match(/^vscode:\/\/vscode-remote\/ssh-remote\+([^/]+)/)
  const connStr = match ? match[1] : box.name
  if (editorChoice.value === 'vscode') {
    return `vscode://vscode-remote/ssh-remote+${connStr}${editorDir.value}?windowId=_blank`
  } else if (editorChoice.value === 'cursor') {
    return `cursor://vscode-remote/ssh-remote+${connStr}${editorDir.value}?windowId=_blank`
  } else if (editorChoice.value === 'zed') {
    return `zed://ssh/${connStr}${editorDir.value}`
  }
  return baseURL
})

const editors = [
  { value: 'vscode', label: 'VS Code' },
  { value: 'cursor', label: 'Cursor' },
  { value: 'zed', label: 'Zed' },
]

function saveEditorChoice() {
  localStorage.setItem('preferred-editor', editorChoice.value)
}

// Prompt modal state
const promptModalOpen = ref(false)
const promptVM = ref('__new__')
const promptText = ref('')
const promptError = ref('')
const promptSending = ref(false)

const shelleyBoxes = computed(() => boxes.value.filter(b => b.shelleyURL))

const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  commandPrefix: '',
  inputPlaceholder: '',
  defaultValue: '',
  danger: false,
})

watch(searchQuery, (val) => syncFilterToURL(val))

// Sync filter to URL
function syncFilterToURL(val: string) {
  const url = new URL(window.location.href)
  if (val.trim()) {
    url.searchParams.set('filter', val.trim())
  } else {
    url.searchParams.delete('filter')
  }
  history.replaceState(null, '', url.toString())
}

const filteredBoxes = computed(() => {
  if (!searchQuery.value.trim()) return boxes.value
  const q = searchQuery.value.toLowerCase().trim()
  const tagQ = q.startsWith('#') ? q.slice(1) : q
  return boxes.value.filter(b =>
    b.name.toLowerCase().includes(q) ||
    (b.displayTags || []).some(t => t.toLowerCase().includes(tagQ))
  )
})

async function loadDashboard() {
  loading.value = true
  loadError.value = ''
  try {
    const data = await fetchDashboard()
    boxes.value = data.boxes
    sharedBoxes.value = data.sharedBoxes
    teamBoxes.value = data.teamBoxes
    sshCommand.value = data.sshCommand
    // Default prompt VM to first shelley-enabled box
    const sb = data.boxes.filter(b => b.shelleyURL)
    if (sb.length > 0) promptVM.value = sb[0].name

    // Auto-expand when filtered to a single result (e.g. redirected from /create-vm)
    if (searchQuery.value.trim()) {
      const q = searchQuery.value.toLowerCase().trim()
      const tagQ = q.startsWith('#') ? q.slice(1) : q
      const matched = data.boxes.filter(b =>
        b.name.toLowerCase().includes(q) ||
        (b.displayTags || []).some(t => t.toLowerCase().includes(tagQ))
      )
      if (matched.length === 1) {
        expandedBoxes.value.add(matched[0].name)
      }
    }
  } catch (err: any) {
    console.error('Failed to load dashboard:', err)
    loadError.value = err.message || 'Failed to load data'
  } finally {
    loading.value = false
  }
}

function onEscapeKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (editorModalOpen.value) { editorModalOpen.value = false; return }
  if (promptModalOpen.value) { promptModalOpen.value = false; return }
}

onMounted(() => {
  loadDashboard()
  document.addEventListener('keydown', onEscapeKey)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEscapeKey)
})

function toggleExpand(name: string) {
  if (expandedBoxes.value.has(name)) {
    expandedBoxes.value.delete(name)
  } else {
    expandedBoxes.value.add(name)
  }
}

interface ActionEvent {
  type: string
  boxName: string
  extra?: any
}

function handleAction(action: ActionEvent) {
  const q = shellQuote(action.boxName)
  switch (action.type) {
    case 'open-editor':
      editorBoxName.value = action.boxName
      editorDir.value = '/home/exedev'
      editorModalOpen.value = true
      break
    case 'rename':
      openModal({ title: 'Rename VM', commandPrefix: `rename ${q}`, inputPlaceholder: 'new-name' })
      break
    case 'restart':
      openModal({ title: 'Restart VM', command: `restart ${q}` })
      break
    case 'delete':
      openModal({ title: 'Delete VM', command: `rm ${q}`, danger: true })
      break
    case 'share':
      openModal({
        title: 'Share VM',
        commandPrefix: `share add ${q}`,
        inputPlaceholder: 'user@example.com',
        description: 'Sharing allows the given user to access this VM\'s web server. <a href="/docs/sharing" target="_blank" rel="noopener noreferrer">Docs</a>',
      })
      break
    case 'share-link':
      openModal({
        title: 'Create Share Link',
        command: `share add-link ${q}`,
        description: 'A share link allows anyone with the link to create an account and access this VM\'s web server. <a href="/docs/sharing" target="_blank" rel="noopener noreferrer">Docs</a>',
      })
      break
    case 'remove-share':
      openModal({
        title: 'Remove Access',
        command: `share remove ${q} ${shellQuote(action.extra)}`,
        danger: true,
      })
      break
    case 'remove-share-link':
      openModal({
        title: 'Remove Share Link',
        command: `share remove-link ${q} ${shellQuote(action.extra)}`,
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
      const proxyURL = action.extra || ''
      let desc = 'The proxy port is the port on your VM that the HTTPS proxy connects to.'
      if (proxyURL) {
        // Sanitize URL: only allow http(s) schemes to prevent XSS
        try {
          const u = new URL(proxyURL)
          if (u.protocol === 'http:' || u.protocol === 'https:') {
            const safe = u.href.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
            desc = `The proxy port is the port on your VM that <a href="${safe}" target="_blank" rel="noopener noreferrer" rel="noopener noreferrer"><b>${safe}</b></a> connects to.`
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
    case 'add-tag':
      openModal({
        title: 'Add Tag',
        commandPrefix: `tag ${q}`,
        inputPlaceholder: 'tag name (e.g. prod)',
      })
      break
    case 'remove-tag':
      openModal({
        title: 'Remove Tag',
        command: `tag -d ${q} ${shellQuote(action.extra)}`,
        danger: true,
      })
      break
  }
}

function openModal(opts: Partial<typeof modal>) {
  Object.assign(modal, {
    visible: true,
    title: '',
    description: '',
    command: '',
    commandPrefix: '',
    inputPlaceholder: '',
    defaultValue: '',
    danger: false,
    ...opts,
  })
}

function closeModal() {
  modal.visible = false
}

async function onModalSuccess() {
  // Reload data after successful action
  try {
    const data = await fetchDashboard()
    boxes.value = data.boxes
    sharedBoxes.value = data.sharedBoxes
    teamBoxes.value = data.teamBoxes
  } catch (err) {
    console.error('Failed to reload dashboard:', err)
  }
}

async function submitPrompt() {
  const text = promptText.value.trim()
  if (!text) return

  if (promptVM.value === '__new__') {
    window.location.href = '/new?prompt=' + encodeURIComponent(text)
    return
  }

  promptSending.value = true
  promptError.value = ''

  try {
    const result = await runCommand(`shelley prompt ${shellQuote(promptVM.value)} ${shellQuote(text)}`)
    if (!result.success) {
      throw new Error(result.error || result.output || 'Request failed')
    }
    const inner = JSON.parse(result.output || '{}')
    if (inner.shelley_url) {
      window.location.href = inner.shelley_url
    } else {
      throw new Error('No conversation URL returned')
    }
  } catch (err: any) {
    promptError.value = err.message || 'Failed to send prompt'
  } finally {
    promptSending.value = false
  }
}
</script>

<style scoped>
.vm-list-page {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.section-left {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  white-space: nowrap;
}

.new-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 4px 10px;
  height: 30px;
  box-sizing: border-box;
  background: var(--btn-bg);
  color: var(--btn-text);
  border: 1px solid var(--btn-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  text-decoration: none;
  cursor: pointer;
  transition: all 0.15s;
  white-space: nowrap;
}

.new-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  text-decoration: none;
}

.search-box {
  position: relative;
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
  max-width: 200px;
}

.search-icon {
  position: absolute;
  left: 10px;
  font-size: 12px;
  color: var(--text-color-muted);
  pointer-events: none;
}

.search-input {
  padding: 4px 28px 4px 30px;
  border: 1px solid var(--input-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  background: var(--input-bg);
  color: var(--input-text);
  outline: none;
  width: 100%;
  min-width: 0;
}

.search-input:focus {
  border-color: var(--primary-color);
}

.search-clear {
  position: absolute;
  right: 6px;
  background: none;
  border: none;
  font-size: 16px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.boxes-list {
  display: flex;
  flex-direction: column;
  gap: 1px;
  background: var(--surface-border);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  overflow: hidden;
}

.loading-state {
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.error-state {
  text-align: center;
  padding: 48px;
  color: var(--danger-text);
}

.error-state p {
  margin-bottom: 12px;
}

.empty-state {
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.empty-state p {
  margin-bottom: 12px;
}

.ssh-cmd {
  display: block;
  margin-top: 8px;
  padding: 8px 12px;
  background: var(--code-bg);
  border-radius: 4px;
  font-size: 13px;
  font-family: var(--font-mono);
  color: var(--code-text);
  word-break: break-all;
}

.shared-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.shared-row {
  background: var(--surface-card);
  padding: 10px 16px;
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
}

.box-name {
  font-weight: 500;
}

.shared-owner {
  color: var(--text-color-muted);
  font-size: 11px;
}

.action-link {
  color: var(--text-color-secondary);
  margin-left: auto;
}

.action-link:hover {
  color: var(--text-color);
}

.box-tags {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.tag {
  font-size: 11px;
  color: var(--tag-text);
  background: var(--tag-bg);
  padding: 1px 6px;
  border-radius: 3px;
}

@media (max-width: 768px) {
  .section-header {
    gap: 6px;
  }
  .section-left {
    gap: 6px;
  }
  .new-btn {
    padding: 4px 8px;
    font-size: 12px;
  }
  .search-box {
    max-width: none;
  }
  .boxes-list {
    border-radius: 0;
    border-left: none;
    border-right: none;
    margin-left: -8px;
    margin-right: -8px;
  }
  .shared-section .boxes-list {
    border-radius: 0;
    border-left: none;
    border-right: none;
    margin-left: -8px;
    margin-right: -8px;
  }
  .vm-list-page {
    gap: 16px;
  }
}

/* Prompt modal */
.prompt-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: var(--surface-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.prompt-modal {
  background: var(--surface-card);
  border-radius: 12px;
  width: 480px;
  max-width: 90vw;
  box-shadow: 0 20px 40px rgba(0,0,0,0.15);
}

.prompt-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--surface-border);
}

.prompt-title {
  font-weight: 600;
  font-size: 15px;
}

.prompt-close {
  background: none;
  border: none;
  font-size: 22px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.prompt-close:hover {
  color: var(--text-color);
}

.prompt-body {
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.prompt-field-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  margin-bottom: 4px;
}

.prompt-select {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--input-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  background: var(--input-bg);
  color: var(--input-text);
}

.prompt-select:focus {
  border-color: var(--primary-color);
  outline: none;
}

.prompt-input {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  resize: vertical;
  min-height: 80px;
}

.prompt-input:focus {
  border-color: var(--primary-color);
  outline: none;
}

.prompt-footer {
  padding: 12px 20px;
  border-top: 1px solid var(--surface-border);
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.prompt-error {
  color: var(--danger-color);
  font-size: 12px;
  flex: 1;
}

.prompt-submit {
  padding: 8px 20px;
  background: var(--text-color);
  color: var(--surface-ground);
  border: none;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
}

.prompt-submit:hover {
  filter: brightness(1.1);
}

.editor-picker {
  display: flex;
  gap: 0;
}

.editor-picker-btn {
  flex: 1;
  padding: 8px 12px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  background: var(--input-bg);
  color: var(--text-color-secondary);
  border: 1px solid var(--input-border);
  transition: all 0.15s;
}

.editor-picker-btn:first-child {
  border-radius: 6px 0 0 6px;
}

.editor-picker-btn:last-child {
  border-radius: 0 6px 6px 0;
}

.editor-picker-btn:not(:first-child) {
  border-left: none;
}

.editor-picker-btn.active {
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

.prompt-submit:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>

<template>
  <div class="integrations-page">
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="loadError" class="error-state">
      <p>Failed to load integrations: {{ loadError }}</p>
      <button class="btn btn-secondary" @click="loadIntegrations">Retry</button>
    </div>

    <template v-else-if="data">
      <!-- Page header -->
      <div class="page-title">Integrations</div>
      <p class="page-subtitle">Connect your VMs to external services. <a href="/docs/integrations" target="_blank" rel="noopener noreferrer">Learn more →</a></p>

      <div v-if="inlineMessage" class="inline-msg" :class="inlineMessageIsError ? 'inline-error' : 'inline-success'">
        {{ inlineMessage }}
        <button class="inline-msg-dismiss" @click="inlineMessage = ''">&times;</button>
      </div>

      <!-- GitHub Accounts -->
      <section class="card">
        <div class="card-header-row">
          <h2 class="card-title">
            <i class="pi pi-github"></i> GitHub
          </h2>
        </div>
        <p class="section-desc">Linking to the exe.dev GitHub app allows you to connect VMs to git repos. <a href="/docs/integrations-github" target="_blank" rel="noopener noreferrer">Learn more →</a></p>

        <div class="subsection-title">Connected Accounts</div>
        <div v-if="!data.githubEnabled" class="empty-msg">
          GitHub integration is not available on this server.
        </div>
        <div v-else-if="data.githubAccounts.length === 0" class="gh-install-section">
          <div style="margin-bottom: 12px;">
            <a class="btn btn-primary" :href="'https://github.com/apps/' + data.githubAppSlug + '/installations/new'" target="_blank" rel="noopener noreferrer">
              <i class="pi pi-github"></i> Install the exe.dev app
            </a>
          </div>
          <div class="gh-install-hint">
            If the app is already installed on the accounts you need,
            <a class="btn btn-secondary" href="/github/setup">link to installed apps</a>
          </div>
        </div>

        <div v-for="acct in data.githubAccounts" :key="acct.installationID" class="gh-account-row">
          <div class="gh-account-info">
            <span class="gh-login">{{ acct.githubLogin }}</span>
            <span v-if="acct.githubLogin !== acct.targetLogin" class="text-muted">
              installed on <strong>{{ acct.targetLogin }}</strong>
            </span>
            <span v-if="verifyResults[acct.installationID]" class="gh-verify-result" :class="verifyResults[acct.installationID].isError ? 'gh-verify-error' : 'gh-verify-ok'">
              {{ verifyResults[acct.installationID].message }}
            </span>
          </div>
          <div class="gh-account-actions">
            <template v-if="!unlinkingAccounts.has(acct.installationID)">
              <button class="btn btn-secondary" :disabled="verifyingAccounts.has(acct.installationID)" @click="verifyGitHub(acct.installationID)">
                <i v-if="verifyingAccounts.has(acct.installationID)" class="pi pi-spin pi-spinner" style="font-size: 10px;"></i>
                {{ verifyingAccounts.has(acct.installationID) ? 'Verifying...' : 'Verify' }}
              </button>
              <button class="btn btn-danger" @click="unlinkGitHub(acct.installationID)">Unlink</button>
            </template>
            <template v-else>
              <span class="text-muted" style="font-size: 11px; margin-right: 8px;">Confirm unlink?</span>
              <button class="btn btn-danger" @click="confirmUnlinkGitHub(acct.installationID)">Yes</button>
              <button class="btn btn-secondary" @click="cancelUnlinkGitHub(acct.installationID)">Cancel</button>
            </template>
          </div>
        </div>

        <div v-if="data.githubEnabled && data.githubAppSlug && data.githubAccounts.length > 0" style="margin-top: 8px; display: flex; gap: 8px; flex-wrap: wrap;">
          <a class="btn btn-secondary" :href="'https://github.com/apps/' + data.githubAppSlug + '/installations/new'" target="_blank" rel="noopener noreferrer">
            Install on another account
          </a>
          <a class="btn btn-secondary" href="/github/setup">
            Link another account
          </a>
          <a class="btn btn-secondary" :href="'https://github.com/apps/' + data.githubAppSlug" target="_blank" rel="noopener noreferrer">
            Configure GitHub App on GitHub
          </a>
        </div>

        <!-- Repo Integrations -->
        <template v-if="data.githubEnabled && data.githubAccounts.length > 0">
          <div class="subsection-title" style="margin-top: 16px;">
            Repository Integrations
          </div>
          <div v-if="data.githubIntegrations.length === 0" class="empty-msg">
            No repo integrations yet.
          </div>
        </template>
        <div v-for="ig in data.githubIntegrations" :key="ig.name" class="integration-row">
          <div class="integration-info">
            <div class="integration-header">
              <span class="integration-name">{{ ig.name }}</span>
              <span v-if="ig.repositories.length > 0" class="text-muted">
                {{ ig.repositories.join(', ') }}
              </span>
            </div>
            <div class="integration-attachments">
              <span v-for="a in ig.attachments" :key="a" class="attachment-tag">
                {{ a }}
                <button class="attachment-tag-remove" @click="detachSpec(ig.name, a)" :title="'Detach from ' + a">&times;</button>
              </span>
              <button class="attachment-tag-add" @click="attachViaCommand(ig.name)" title="Attach">
                {{ ig.attachments.length > 0 ? '+' : '+ attach' }}
              </button>
            </div>
            <div v-if="ig.repositories.length > 0" class="git-clone-row">
              <code>git clone {{ integrationScheme }}://{{ ig.name }}.int.{{ boxHost }}/{{ ig.repositories[0] }}.git</code>
              <CopyButton :text="`git clone ${integrationScheme}://${ig.name}.int.${boxHost}/${ig.repositories[0]}.git`" title="Copy" />
            </div>
          </div>
          <div class="integration-actions">
            <button class="btn btn-danger" @click="removeIntegration(ig.name)">Remove</button>
          </div>
        </div>

        <!-- Add button -->
        <div v-if="data?.githubEnabled && data?.githubAccounts.length > 0" style="margin-top: 12px;">
          <button class="btn btn-secondary" @click="openAddGitHubRepo">
            <i class="pi pi-plus" style="font-size: 11px;"></i> Add Repository Integration
          </button>
        </div>
      </section>

      <!-- HTTP Proxy Integrations -->
      <section class="card">
        <div class="card-header-row">
          <h2 class="card-title">
            <i class="pi pi-globe"></i> HTTP Proxy
          </h2>
        </div>
        <p class="section-desc">Proxy integrations let your VMs access authenticated HTTP services.</p>

        <div v-if="data.proxyIntegrations.length === 0" class="empty-msg">
          No HTTP proxy integrations created yet.
        </div>

        <div v-for="ig in data.proxyIntegrations" :key="ig.name" class="integration-row">
          <div class="integration-info">
            <div class="integration-header">
              <span class="integration-name">{{ ig.name }}</span>
              <span class="text-muted">{{ ig.target }}</span>
              <span v-if="ig.hasHeader" class="badge badge-blue">header</span>
              <span v-if="ig.hasBasicAuth" class="badge badge-yellow">auth</span>
            </div>
            <div class="integration-attachments">
              <span v-for="a in ig.attachments" :key="a" class="attachment-tag">
                {{ a }}
                <button class="attachment-tag-remove" @click="detachSpec(ig.name, a)" :title="'Detach from ' + a">&times;</button>
              </span>
              <button class="attachment-tag-add" @click="attachViaCommand(ig.name)" title="Attach">
                {{ ig.attachments.length > 0 ? '+' : '+ attach' }}
              </button>
            </div>
          </div>
          <div class="integration-actions">
            <button class="btn btn-danger" @click="removeIntegration(ig.name)">Remove</button>
          </div>
        </div>

        <!-- Add button -->
        <div style="margin-top: 12px;">
          <button class="btn btn-secondary" @click="openAddHTTPProxy">
            <i class="pi pi-plus" style="font-size: 11px;"></i> Add HTTP Proxy Integration
          </button>
        </div>
      </section>

      <!-- Push Notifications -->
      <section v-if="data.hasPushTokens" class="card">
        <h2 class="card-title">
          <i class="pi pi-bell"></i> Notifications
        </h2>
        <p class="section-desc">The <strong>notify</strong> integration is built-in and attached to all your VMs. It sends push notifications to your devices.</p>
        <div class="integration-row">
          <div class="integration-info">
            <span class="integration-name">notify</span>
            <span class="text-muted">push notifications to device</span>
            <div class="text-muted" style="font-size: 11px; margin-top: 2px;">attached to auto:all · built-in</div>
          </div>
        </div>
        <div class="section-desc" style="margin-top: 12px;">
          Usage from a VM:
          <div style="margin-top: 4px;">
            <code style="font-size: 11px; display: block; padding: 8px; background: var(--surface-ground); border-radius: 4px; overflow-x: auto;">
              curl -X POST {{ integrationScheme }}://notify.int.{{ boxHost }}/ -H 'Content-Type: application/json' -d '{"title":"Done","body":"Task finished"}'
            </code>
          </div>
        </div>
      </section>
    </template>

    <!-- Command Modal (for remove, detach, attach-single) -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :danger="modal.danger"
      @close="modal.visible = false"
      @success="reload"
    />

    <!-- Add GitHub Repo Modal -->
    <div v-if="ghModal.visible" class="modal-overlay" @click.self="closeGhModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Add Repository Integration">
        <div class="modal-header">
          <h3>Add Repository Integration</h3>
          <button class="modal-close" aria-label="Close" @click="closeGhModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Repository</label>
            <div class="repo-combobox" ref="repoComboboxRef">
              <input
                v-model="repoSearch"
                class="form-input"
                :placeholder="loadingRepos ? 'Loading repos...' : 'Search repositories...'"
                :disabled="loadingRepos"
                @focus="repoDropdownOpen = true"
                @input="repoDropdownOpen = true"
              />
              <div v-if="repoDropdownOpen && !loadingRepos && filteredRepos.length > 0" class="repo-dropdown">
                <div
                  v-for="repo in filteredRepos"
                  :key="repo.full_name"
                  class="repo-option"
                  :class="{ 'repo-option-selected': ghModal.repo === repo.full_name }"
                  @mousedown.prevent="selectRepo(repo)"
                >
                  <span class="repo-option-name">{{ repo.full_name }}</span>
                  <span v-if="repo.description" class="repo-option-sub">{{ repo.description }}</span>
                </div>
              </div>
              <div v-if="repoDropdownOpen && !loadingRepos && repoSearch && filteredRepos.length === 0" class="repo-dropdown">
                <div class="repo-option repo-option-empty">No matching repositories</div>
              </div>
            </div>
          </div>
          <div class="form-row">
            <label>Name</label>
            <input v-model="ghModal.name" class="form-input" :placeholder="ghModal.repo ? ghModal.repo.replace(/\//g, '-') : 'integration-name'" />
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="ghAttachRef">
              <div class="multi-select-tags" v-if="ghModal.attachments.length > 0">
                <span v-for="a in ghModal.attachments" :key="a" class="multi-select-tag">
                  {{ a }}
                  <button class="multi-select-tag-remove" @click="removeGhAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="ghAttachInputRef"
                v-model="ghAttachSearch"
                class="form-input"
                placeholder="Search VMs, tags..."
                @focus="ghAttachOpen = true"
                @input="ghAttachOpen = true"
              />
              <div v-if="ghAttachOpen && filteredGhAttachOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredGhAttachOptions"
                  :key="opt"
                  class="attach-option"
                  @mousedown.prevent="addGhAttachment(opt)"
                >
                  {{ opt }}
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code>{{ ghBuiltCommand || 'Select a repository to preview command' }}</code>
          </div>
          <div v-if="ghModal.result" class="cmd-result" :class="ghModal.result.success ? 'success' : 'error'">
            {{ ghModal.result.output || ghModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="ghModal.result?.success" class="btn btn-primary" @click="closeGhModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeGhModal">Cancel</button>
            <button class="btn btn-primary" :disabled="!ghBuiltCommand || ghModal.running" @click="runGhCommand">
              {{ ghModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Add HTTP Proxy Modal -->
    <div v-if="proxyModal.visible" class="modal-overlay" @click.self="closeProxyModal">
      <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Add HTTP Proxy Integration">
        <div class="modal-header">
          <h3>Add HTTP Proxy Integration</h3>
          <button class="modal-close" aria-label="Close" @click="closeProxyModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Name</label>
            <input v-model="proxyModal.name" class="form-input" placeholder="my-api" />
          </div>
          <div class="form-row">
            <label>Target URL</label>
            <input v-model="proxyModal.target" class="form-input" placeholder="https://api.example.com" />
          </div>
          <div class="form-row">
            <label>Auth method</label>
            <select v-model="proxyModal.authMethod" class="form-input">
              <option value="none">None</option>
              <option value="bearer">Bearer Token</option>
              <option value="header">Custom Header</option>
            </select>
          </div>
          <div v-if="proxyModal.authMethod === 'bearer'" class="form-row">
            <label>Token</label>
            <input v-model="proxyModal.bearer" type="password" autocomplete="new-password" class="form-input" placeholder="your-bearer-token" />
          </div>
          <div v-if="proxyModal.authMethod === 'header'" class="form-row">
            <label>Header</label>
            <input v-model="proxyModal.header" class="form-input" placeholder="Authorization: Bearer ..." />
          </div>
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="proxyAttachRef">
              <div class="multi-select-tags" v-if="proxyModal.attachments.length > 0">
                <span v-for="a in proxyModal.attachments" :key="a" class="multi-select-tag">
                  {{ a }}
                  <button class="multi-select-tag-remove" @click="removeProxyAttachment(a)">&times;</button>
                </span>
              </div>
              <input
                ref="proxyAttachInputRef"
                v-model="proxyAttachSearch"
                class="form-input"
                placeholder="Search VMs, tags..."
                @focus="proxyAttachOpen = true"
                @input="proxyAttachOpen = true"
              />
              <div v-if="proxyAttachOpen && filteredProxyAttachOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredProxyAttachOptions"
                  :key="opt"
                  class="attach-option"
                  @mousedown.prevent="addProxyAttachment(opt)"
                >
                  {{ opt }}
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code>{{ proxyBuiltCommand || 'Fill in name and target to preview command' }}</code>
          </div>
          <div v-if="proxyModal.result" class="cmd-result" :class="proxyModal.result.success ? 'success' : 'error'">
            {{ proxyModal.result.output || proxyModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="proxyModal.result?.success" class="btn btn-primary" @click="closeProxyModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeProxyModal">Cancel</button>
            <button class="btn btn-primary" :disabled="!proxyBuiltCommand || proxyModal.running" @click="runProxyCommand">
              {{ proxyModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- Attach Modal (for adding attachments to existing integrations) -->
    <div v-if="attachModal.visible" class="modal-overlay" @click.self="closeAttachModal">
      <div class="modal-panel modal-panel-narrow" role="dialog" aria-modal="true" aria-label="Attach Integration">
        <div class="modal-header">
          <h3>Attach {{ attachModal.name }}</h3>
          <button class="modal-close" aria-label="Close" @click="closeAttachModal">&times;</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <label>Attach to</label>
            <div class="multi-select" ref="attachModalRef">
              <div class="multi-select-tags" v-if="attachModal.attachments.length > 0">
                <span v-for="a in attachModal.attachments" :key="a" class="multi-select-tag">
                  {{ a }}
                  <button class="multi-select-tag-remove" @click="removeAttachModalItem(a)">&times;</button>
                </span>
              </div>
              <input
                ref="attachModalInputRef"
                v-model="attachModalSearch"
                class="form-input"
                placeholder="Search VMs, tags..."
                @focus="attachModalOpen = true"
                @input="attachModalOpen = true"
              />
              <div v-if="attachModalOpen && filteredAttachModalOptions.length > 0" class="attach-dropdown">
                <div
                  v-for="opt in filteredAttachModalOptions"
                  :key="opt"
                  class="attach-option"
                  @mousedown.prevent="addAttachModalItem(opt)"
                >
                  {{ opt }}
                </div>
              </div>
            </div>
          </div>
          <div class="cmd-preview">
            <code>{{ attachBuiltCommand || 'Select an attachment target' }}</code>
          </div>
          <div v-if="attachModal.result" class="cmd-result" :class="attachModal.result.success ? 'success' : 'error'">
            {{ attachModal.result.output || attachModal.result.error }}
          </div>
        </div>
        <div class="modal-footer">
          <button v-if="attachModal.result?.success" class="btn btn-primary" @click="closeAttachModal">Done</button>
          <template v-else>
            <button class="btn btn-secondary" @click="closeAttachModal">Cancel</button>
            <button class="btn btn-primary" :disabled="!attachBuiltCommand || attachModal.running" @click="runAttachCommand">
              {{ attachModal.running ? 'Running...' : 'Run' }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { fetchIntegrations, shellQuote, runCommand, type IntegrationsData } from '../api/client'
import CommandModal from '../components/CommandModal.vue'
import CopyButton from '../components/CopyButton.vue'

const loading = ref(true)
const loadError = ref('')
const data = ref<IntegrationsData | null>(null)
const githubRepos = ref<any[]>([])
const loadingRepos = ref(false)
const unlinkingAccounts = ref<Set<number>>(new Set())
const verifyResults = reactive<Record<number, { message: string; isError: boolean }>>({})
const verifyingAccounts = ref<Set<number>>(new Set())
const inlineMessage = ref('')
const inlineMessageIsError = ref(false)

// Repo combobox state
const repoSearch = ref('')
const repoDropdownOpen = ref(false)
const repoComboboxRef = ref<HTMLElement | null>(null)

// Attachment multi-select refs (for outside-click and focus)
const ghAttachRef = ref<HTMLElement | null>(null)
const proxyAttachRef = ref<HTMLElement | null>(null)
const ghAttachInputRef = ref<HTMLInputElement | null>(null)
const proxyAttachInputRef = ref<HTMLInputElement | null>(null)

function showError(msg: string) {
  inlineMessage.value = msg
  inlineMessageIsError.value = true
}

const integrationScheme = ref('https')
const boxHost = ref(window.location.hostname)

// Simple command modal (for remove, detach, attach-single)
const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  commandPrefix: '',
  inputPlaceholder: '',
  danger: false,
})

// GitHub add modal
const ghModal = reactive({
  visible: false,
  repo: '',
  name: '',
  attachments: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

const ghAttachSearch = ref('')
const ghAttachOpen = ref(false)

// HTTP Proxy add modal
const proxyModal = reactive({
  visible: false,
  name: '',
  target: '',
  authMethod: 'none',
  bearer: '',
  header: '',
  attachments: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

const proxyAttachSearch = ref('')
const proxyAttachOpen = ref(false)

// Attach modal (for attaching to existing integrations)
const attachModal = reactive({
  visible: false,
  name: '',
  existingAttachments: [] as string[],
  attachments: [] as string[],
  running: false,
  result: null as { success: boolean; output: string; error: string } | null,
})

const attachModalSearch = ref('')
const attachModalOpen = ref(false)
const attachModalRef = ref<HTMLElement | null>(null)
const attachModalInputRef = ref<HTMLInputElement | null>(null)

// All possible attachment options
const allAttachOptions = computed(() => {
  if (!data.value) return []
  const opts: string[] = ['auto:all']
  for (const tag of data.value.allTags) {
    opts.push(`tag:${tag}`)
  }
  for (const box of data.value.boxes) {
    opts.push(`vm:${box.name}`)
  }
  return opts
})

function filterAttachOptions(search: string, selected: string[]) {
  const q = search.toLowerCase().trim()
  return allAttachOptions.value
    .filter(o => !selected.includes(o))
    .filter(o => !q || o.toLowerCase().includes(q))
}

const filteredGhAttachOptions = computed(() => filterAttachOptions(ghAttachSearch.value, ghModal.attachments))
const filteredProxyAttachOptions = computed(() => filterAttachOptions(proxyAttachSearch.value, proxyModal.attachments))
const filteredAttachModalOptions = computed(() => filterAttachOptions(attachModalSearch.value, [...attachModal.attachments, ...attachModal.existingAttachments]))

const filteredRepos = computed(() => {
  const q = repoSearch.value.toLowerCase().trim()
  if (!q) return githubRepos.value.slice(0, 50)
  return githubRepos.value.filter(r =>
    r.full_name.toLowerCase().includes(q) ||
    (r.description || '').toLowerCase().includes(q)
  ).slice(0, 50)
})

// GitHub modal command builder
const ghBuiltCommand = computed(() => {
  if (!ghModal.repo) return ''
  const name = ghModal.name.trim() || ghModal.repo.replace(/\//g, '-')
  let cmd = `integrations add github --name=${shellQuote(name)} --repository=${shellQuote(ghModal.repo)}`
  for (const a of ghModal.attachments) {
    cmd += ` --attach=${shellQuote(a)}`
  }
  return cmd
})

// Proxy modal command builder
const proxyBuiltCommand = computed(() => {
  if (!proxyModal.name.trim() || !proxyModal.target.trim()) return ''
  let cmd = `integrations add http-proxy --name=${shellQuote(proxyModal.name.trim())} --target=${shellQuote(proxyModal.target.trim())}`
  if (proxyModal.authMethod === 'bearer' && proxyModal.bearer.trim()) {
    cmd += ` --bearer=${shellQuote(proxyModal.bearer.trim())}`
  } else if (proxyModal.authMethod === 'header' && proxyModal.header.trim()) {
    cmd += ` --header=${shellQuote(proxyModal.header.trim())}`
  }
  for (const a of proxyModal.attachments) {
    cmd += ` --attach=${shellQuote(a)}`
  }
  return cmd
})

// Attach modal command builder
const attachBuiltCommand = computed(() => {
  if (attachModal.attachments.length === 0) return ''
  let cmd = `integrations attach ${shellQuote(attachModal.name)}`
  for (const a of attachModal.attachments) {
    cmd += ` ${shellQuote(a)}`
  }
  return cmd
})

// GitHub attachment helpers
function addGhAttachment(opt: string) {
  if (!ghModal.attachments.includes(opt)) {
    ghModal.attachments.push(opt)
  }
  ghAttachSearch.value = ''
  nextTick(() => {
    ghAttachInputRef.value?.focus()
    ghAttachOpen.value = true
  })
}

function removeGhAttachment(opt: string) {
  ghModal.attachments = ghModal.attachments.filter(a => a !== opt)
}

// Proxy attachment helpers
function addProxyAttachment(opt: string) {
  if (!proxyModal.attachments.includes(opt)) {
    proxyModal.attachments.push(opt)
  }
  proxyAttachSearch.value = ''
  nextTick(() => {
    proxyAttachInputRef.value?.focus()
    proxyAttachOpen.value = true
  })
}

function removeProxyAttachment(opt: string) {
  proxyModal.attachments = proxyModal.attachments.filter(a => a !== opt)
}

// Attach modal helpers
function addAttachModalItem(opt: string) {
  if (!attachModal.attachments.includes(opt)) {
    attachModal.attachments.push(opt)
  }
  attachModalSearch.value = ''
  nextTick(() => {
    attachModalInputRef.value?.focus()
    attachModalOpen.value = true
  })
}

function removeAttachModalItem(opt: string) {
  attachModal.attachments = attachModal.attachments.filter(a => a !== opt)
}

// Close dropdowns on outside click
function onDocClick(e: MouseEvent) {
  if (repoComboboxRef.value && !repoComboboxRef.value.contains(e.target as Node)) {
    repoDropdownOpen.value = false
  }
  if (ghAttachRef.value && !ghAttachRef.value.contains(e.target as Node)) {
    ghAttachOpen.value = false
  }
  if (proxyAttachRef.value && !proxyAttachRef.value.contains(e.target as Node)) {
    proxyAttachOpen.value = false
  }
  if (attachModalRef.value && !attachModalRef.value.contains(e.target as Node)) {
    attachModalOpen.value = false
  }
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  loadIntegrations()
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
})

async function reload() {
  try {
    data.value = await fetchIntegrations()
  } catch (err) {
    console.error('Failed to reload integrations:', err)
  }
}

async function loadIntegrations() {
  loading.value = true
  loadError.value = ''
  try {
    data.value = await fetchIntegrations()
    if (data.value.integrationScheme) integrationScheme.value = data.value.integrationScheme
    if (data.value.boxHost) boxHost.value = data.value.boxHost
  } catch (err: any) {
    console.error('Failed to load integrations:', err)
    loadError.value = err.message || 'Failed to load data'
  } finally {
    loading.value = false
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
    danger: false,
    ...opts,
  })
}

function removeIntegration(name: string) {
  openModal({
    title: 'Remove Integration',
    command: `integrations remove ${shellQuote(name)}`,
    danger: true,
  })
}

function attachViaCommand(name: string) {
  // Find existing attachments for this integration
  const ig = [...(data.value?.githubIntegrations || []), ...(data.value?.proxyIntegrations || [])].find(i => i.name === name)
  attachModal.visible = true
  attachModal.name = name
  attachModal.existingAttachments = ig?.attachments || []
  attachModal.attachments = []
  attachModal.running = false
  attachModal.result = null
  attachModalSearch.value = ''
  nextTick(() => {
    attachModalInputRef.value?.focus()
    attachModalOpen.value = true
  })
}

function closeAttachModal() {
  attachModal.visible = false
  if (attachModal.result?.success) reload()
}

async function runAttachCommand() {
  const cmd = attachBuiltCommand.value
  if (!cmd) return
  attachModal.running = true
  attachModal.result = null
  try {
    const res = await runCommand(cmd)
    attachModal.result = { success: !!res.success, output: res.output || '', error: res.error || res.output || '' }
  } catch (err: any) {
    attachModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    attachModal.running = false
  }
}

function detachSpec(integrationName: string, spec: string) {
  openModal({
    title: 'Detach Integration',
    command: `integrations detach ${shellQuote(integrationName)} ${shellQuote(spec)}`,
  })
}

// GitHub add modal
async function openAddGitHubRepo() {
  ghModal.visible = true
  ghModal.repo = ''
  ghModal.name = ''
  ghModal.attachments = []
  ghModal.running = false
  ghModal.result = null
  repoSearch.value = ''
  ghAttachSearch.value = ''
  loadingRepos.value = true
  try {
    const resp = await fetch('/github/repos')
    const result = await resp.json()
    if (result.success) {
      githubRepos.value = result.repos || []
    } else {
      showError('Failed to load repos: ' + (result.error || 'unknown error'))
    }
  } catch (err: any) {
    showError('Failed to load repos: ' + err.message)
  } finally {
    loadingRepos.value = false
  }
}

function selectRepo(repo: any) {
  ghModal.repo = repo.full_name
  repoSearch.value = repo.full_name
  repoDropdownOpen.value = false
  // Don't auto-fill name — let placeholder show the default
}

function closeGhModal() {
  ghModal.visible = false
  if (ghModal.result?.success) reload()
}

async function runGhCommand() {
  const cmd = ghBuiltCommand.value
  if (!cmd) return
  ghModal.running = true
  ghModal.result = null
  try {
    const res = await runCommand(cmd)
    ghModal.result = { success: !!res.success, output: res.output || '', error: res.error || res.output || '' }
  } catch (err: any) {
    ghModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    ghModal.running = false
  }
}

// HTTP Proxy add modal
function openAddHTTPProxy() {
  proxyModal.visible = true
  proxyModal.name = ''
  proxyModal.target = ''
  proxyModal.authMethod = 'none'
  proxyModal.bearer = ''
  proxyModal.header = ''
  proxyModal.attachments = []
  proxyModal.running = false
  proxyModal.result = null
  proxyAttachSearch.value = ''
}

function closeProxyModal() {
  proxyModal.visible = false
  if (proxyModal.result?.success) reload()
}

async function runProxyCommand() {
  const cmd = proxyBuiltCommand.value
  if (!cmd) return
  proxyModal.running = true
  proxyModal.result = null
  try {
    const res = await runCommand(cmd)
    proxyModal.result = { success: !!res.success, output: res.output || '', error: res.error || res.output || '' }
  } catch (err: any) {
    proxyModal.result = { success: false, output: '', error: err.message || 'Network error' }
  } finally {
    proxyModal.running = false
  }
}

function verifyGitHub(installationID: number) {
  delete verifyResults[installationID]
  verifyingAccounts.value.add(installationID)
  fetch(`/github/verify?installation_id=${installationID}`)
    .then(r => r.json())
    .then(result => {
      if (result.success) {
        const count = result.repo_count ?? 0
        verifyResults[installationID] = { message: `${count} repo${count !== 1 ? 's' : ''} accessible`, isError: false }
      } else {
        verifyResults[installationID] = { message: result.error || 'unknown error', isError: true }
      }
    })
    .catch(err => {
      verifyResults[installationID] = { message: err.message, isError: true }
    })
    .finally(() => {
      verifyingAccounts.value.delete(installationID)
    })
}

function unlinkGitHub(installationID: number) {
  unlinkingAccounts.value.add(installationID)
}

function cancelUnlinkGitHub(installationID: number) {
  unlinkingAccounts.value.delete(installationID)
}

function confirmUnlinkGitHub(installationID: number) {
  fetch('/github/unlink', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ installation_id: installationID }),
  })
    .then(r => r.json())
    .then(result => {
      if (result.success) {
        unlinkingAccounts.value.delete(installationID)
        reload()
      } else {
        showError('Unlink failed: ' + (result.error || 'unknown error'))
        unlinkingAccounts.value.delete(installationID)
      }
    })
    .catch(err => {
      showError('Unlink failed: ' + err.message)
      unlinkingAccounts.value.delete(installationID)
    })
}
</script>

<style scoped>
.integrations-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.page-title {
  font-size: 24px;
  font-weight: 700;
  color: var(--text-color);
  margin-bottom: -8px;
}

.page-subtitle {
  font-size: 14px;
  color: var(--text-color-muted);
  margin-bottom: 4px;
}

.page-subtitle a {
  color: var(--text-color-secondary);
}

.section-desc {
  font-size: 13px;
  color: var(--text-color-muted);
  margin-bottom: 12px;
  line-height: 1.5;
}

.section-desc a {
  color: var(--text-color-secondary);
}

.subsection-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 8px;
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

.inline-msg {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 16px;
}

.inline-error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}

.inline-success {
  background: var(--success-bg);
  color: var(--success-text);
  border: 1px solid var(--success-border);
}

.inline-msg-dismiss {
  margin-left: auto;
  background: none;
  border: none;
  color: inherit;
  cursor: pointer;
  font-size: 16px;
  padding: 0 4px;
}

.card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.card-header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.card-header-row .card-title {
  margin-bottom: 0;
}

.empty-msg {
  color: var(--text-color-muted);
  font-size: 13px;
}

.text-muted {
  color: var(--text-color-muted);
  font-size: 12px;
}

/* GitHub accounts */
.gh-install-section {
  padding: 8px 0;
}

.gh-install-hint {
  font-size: 13px;
  color: var(--text-color-muted);
}

.gh-verify-result {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 3px;
}

.gh-verify-ok {
  color: var(--success-text);
  background: var(--success-bg);
}

.gh-verify-error {
  color: var(--danger-text);
  background: var(--danger-bg);
}

.gh-account-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
}

.gh-account-info {
  display: flex;
  align-items: center;
  gap: 8px;
}

.gh-login {
  font-weight: 500;
  font-size: 13px;
}

.gh-account-actions {
  display: flex;
  gap: 4px;
}

/* Integrations */
.integration-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  padding: 10px 0;
  border-bottom: 1px solid var(--surface-subtle);
  gap: 12px;
}

.integration-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  flex: 1;
}

.integration-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.integration-attachments {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
}

.integration-name {
  font-weight: 500;
  font-size: 13px;
}

.integration-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

.attachment-tag {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--tag-bg);
  color: var(--text-color-secondary);
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
}

.badge-blue {
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
}

.badge-yellow {
  background: var(--badge-public-bg);
  color: var(--badge-public-text);
}

/* Modal overlay */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-panel {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 520px;
  max-width: 90vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}

.modal-panel-narrow {
  width: 420px;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--surface-border);
}

.modal-header h3 {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
}

.modal-close {
  background: none;
  border: none;
  font-size: 20px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.modal-body {
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 20px;
  border-top: 1px solid var(--surface-border);
}

/* Form rows */
.form-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.form-row label {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-color-secondary);
}

.form-input {
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  box-sizing: border-box;
}

.form-input:focus {
  border-color: var(--primary-color);
  outline: none;
}

select.form-input {
  cursor: pointer;
}

/* Repo combobox */
.repo-combobox {
  position: relative;
}

.repo-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  max-height: 240px;
  overflow-y: auto;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 100;
  margin-top: 2px;
}

.repo-option {
  padding: 8px 10px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.repo-option:hover {
  background: var(--surface-hover);
}

.repo-option-selected {
  background: var(--surface-subtle);
}

.repo-option-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-color);
}

.repo-option-sub {
  font-size: 11px;
  color: var(--text-color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.repo-option-empty {
  color: var(--text-color-muted);
  font-size: 12px;
  cursor: default;
}

.repo-option-empty:hover {
  background: transparent;
}

/* Multi-select attachment picker */
.multi-select {
  position: relative;
}

.multi-select-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 4px;
}

.multi-select-tag {
  font-size: 11px;
  padding: 2px 6px;
  background: var(--tag-bg);
  color: var(--text-color-secondary);
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
}

.multi-select-tag-remove {
  background: none;
  border: none;
  color: var(--text-color-secondary);
  cursor: pointer;
  padding: 0 1px;
  font-size: 13px;
  line-height: 1;
}

.multi-select-tag-remove:hover {
  color: var(--danger-color);
}

.attach-dropdown {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  max-height: 200px;
  overflow-y: auto;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  z-index: 100;
  margin-top: 2px;
}

.attach-option {
  padding: 6px 10px;
  cursor: pointer;
  font-size: 12px;
  color: var(--text-color);
}

.attach-option:hover {
  background: var(--surface-hover);
}

/* Command preview */
.cmd-preview {
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 8px 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
  word-break: break-all;
  color: var(--text-color-secondary);
}

.cmd-result {
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  white-space: pre-wrap;
}

.cmd-result.success {
  background: var(--success-bg);
  color: var(--success-text);
  border: 1px solid var(--success-border);
}

.cmd-result.error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}

/* Buttons */
.btn {
  padding: 5px 12px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.15s;
  text-decoration: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover:not(:disabled) {
  filter: brightness(1.1);
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

.btn-danger {
  background: var(--btn-bg);
  color: var(--danger-color);
  border-color: var(--danger-border);
}

.btn-danger:hover {
  background: var(--danger-bg);
}

/* Attachment tags with remove button */
.attachment-tag-remove {
  background: none;
  border: none;
  color: var(--text-color-secondary);
  cursor: pointer;
  padding: 0 2px;
  margin-left: 2px;
  font-size: 14px;
  line-height: 1;
}

.attachment-tag-remove:hover {
  color: var(--danger-color);
}

.attachment-tag-add {
  font-size: 10px;
  padding: 1px 6px;
  background: var(--surface-ground);
  color: var(--text-color-secondary);
  border: 1px dashed var(--surface-border);
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s;
}

.attachment-tag-add:hover {
  background: var(--surface-hover);
  border-color: var(--text-color-secondary);
}

/* Git clone row */
.git-clone-row {
  font-size: 11px;
  color: var(--text-color-muted);
  display: flex;
  align-items: center;
  gap: 4px;
}

.git-clone-row code {
  background: var(--surface-ground);
  padding: 2px 6px;
  border-radius: 3px;
  font-family: var(--font-mono);
}
</style>

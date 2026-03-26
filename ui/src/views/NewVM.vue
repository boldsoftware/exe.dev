<template>
  <div class="new-vm-page">
    <form class="new-vm-form" @submit.prevent="createVM">
      <h1 class="form-title">Create a New VM</h1>

      <div v-if="errorMessage" class="form-error">{{ errorMessage }}</div>

      <!-- Hostname -->
      <div class="form-group">
        <div class="hostname-group">
          <input
            v-model="hostname"
            type="text"
            class="form-input hostname-input"
            placeholder="my-project"
            required
            autocomplete="off"
            autocapitalize="none"
            @input="onHostnameInput"
          />
          <span class="hostname-suffix">.exe.xyz</span>
        </div>
        <div v-if="hostnameHint" class="form-hint" :class="{ error: !hostnameOk }">
          {{ hostnameHint }}
        </div>
      </div>

      <!-- Prompt with segmented control -->
      <div class="form-group">
        <div v-if="ideas.length > 0" class="prompt-header">
          <div class="seg-control">
            <button type="button" class="seg-btn" :class="{ active: mode === 'describe' }" @click="mode = 'describe'">Describe</button>
            <button type="button" class="seg-btn" :class="{ active: mode === 'templates' }" @click="openDrawer">Ideas</button>
          </div>
        </div>
        <textarea
          v-model="prompt"
          class="form-input prompt-input"
          placeholder="Build a blog..."
          rows="4"
        ></textarea>
        <!-- Template pill -->
        <div v-if="selectedIdea" class="template-pill">
          <img v-if="isURL(selectedIdea.icon_url)" class="template-pill-img" :src="selectedIdea.icon_url" alt="" />
          <span v-else class="template-pill-icon">{{ selectedIdea.icon_url || '\uD83D\uDCE6' }}</span>
          <span class="template-pill-label">Idea:</span>
          <span class="template-pill-title">{{ selectedIdea.title }}</span>
          <button type="button" class="template-pill-clear" title="Clear template" @click="clearIdea">&times;</button>
        </div>
      </div>

      <!-- Options -->
      <details class="options-section">
        <summary class="options-toggle">Options</summary>
        <div class="options-body">
          <div class="form-group">
            <label class="form-label">Image</label>
            <input
              v-model="image"
              type="text"
              class="form-input"
              placeholder="exeuntu (default)"
              autocomplete="off"
            />
          </div>
        </div>
      </details>

      <input v-if="ideaSlug" type="hidden" name="idea_slug" :value="ideaSlug" />

      <button type="submit" class="submit-btn" :disabled="!canSubmit || submitting">
        {{ submitting ? 'Creating...' : 'Create VM' }}
      </button>
    </form>

    <!-- Ideas drawer -->
    <div v-if="drawerOpen" class="template-drawer open">
      <div class="drawer-backdrop" @click="closeDrawer"></div>
      <div class="drawer-panel">
        <div class="drawer-header">
          <h2>Ideas</h2>
          <button type="button" class="drawer-close" aria-label="Close" @click="closeDrawer">&times;</button>
        </div>
        <div class="drawer-sections">
          <template v-for="cat in categories" :key="cat.slug">
            <section v-if="ideasInCategory(cat.slug).length > 0" class="template-section">
              <h3 class="section-title">{{ cat.label }}</h3>
              <div class="drawer-grid">
                <button
                  v-for="idea in ideasInCategory(cat.slug)"
                  :key="idea.slug"
                  type="button"
                  class="template-card"
                  :class="{ selected: selectedIdea?.slug === idea.slug }"
                  @click="selectIdea(idea)"
                >
                  <div class="card-header">
                    <img v-if="isURL(idea.icon_url)" class="card-icon-img" :src="idea.icon_url" alt="" />
                    <span v-else class="card-icon-emoji">{{ idea.icon_url || '\uD83D\uDCE6' }}</span>
                  </div>
                  <div class="card-title">{{ idea.title }}</div>
                  <div class="card-desc">{{ idea.short_description }}</div>
                  <div class="card-footer">
                    <div class="card-rating">
                      <span v-for="i in 5" :key="i" class="star" :class="{ filled: i <= Math.round(idea.avg_rating) }">&starf;</span>
                      <span v-if="idea.rating_count > 0" class="rating-count">({{ idea.rating_count }})</span>
                    </div>
                  </div>
                </button>
              </div>
            </section>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { checkHostname } from '../api/client'

const route = useRoute()
const router = useRouter()

const hostname = ref('')
const prompt = ref((route.query.prompt as string) || '')
const image = ref((route.query.image as string) || '')
const inviteCode = ref((route.query.invite as string) || '')
const errorMessage = ref('')
const hostnameHint = ref('')
const hostnameOk = ref(false)
const submitting = ref(false)
const mode = ref<'describe' | 'templates'>('describe')
const drawerOpen = ref(false)

// Show error from redirect (e.g. vm_limit)
const errorParam = route.query.error as string
if (errorParam === 'vm_limit') {
  errorMessage.value = 'You have reached the maximum number of VMs allowed on your plan. Delete an existing VM to create a new one.'
}

// Ideas
interface Idea {
  slug: string
  title: string
  short_description: string
  category: string
  prompt: string
  icon_url: string
  screenshot_url: string
  featured: boolean
  avg_rating: number
  rating_count: number
  vm_shortname: string
  image: string
  deploy_count: number
}

const ideas = ref<Idea[]>([])
const selectedIdea = ref<Idea | null>(null)
const ideaSlug = computed(() => selectedIdea.value?.slug || '')

const categories = [
  { slug: 'dev-tools', label: 'Dev Tools' },
  { slug: 'web-apps', label: 'Web Apps' },
  { slug: 'ai-ml', label: 'AI & ML' },
  { slug: 'databases', label: 'Databases' },
  { slug: 'games', label: 'Games & Fun' },
  { slug: 'self-hosted', label: 'Self-Hosted' },
  { slug: 'other', label: 'Other' },
]

function ideasInCategory(slug: string) {
  return ideas.value.filter(i => i.category === slug)
}

function isURL(s: string): boolean {
  return !!s && (s.startsWith('http://') || s.startsWith('https://') || s.startsWith('/'))
}

// Random hostname generation (mirrors boxname.Random() in Go)
const words = [
  'alpha','bravo','charlie','delta','echo','foxtrot','golf','hotel','india','juliet',
  'kilo','lima','mike','november','oscar','papa','quebec','romeo','sierra','tango',
  'uniform','victor','whiskey','xray','yankee','zulu',
  'able','baker','dog','early','waltz','george','how','item','jig','king','love','nan',
  'oboe','prep','queen','roger','sweet','tare','uncle','victory','william','extra',
  'yolk','zebra',
  'earth','wind','fire','water','stone','tree','river','mountain','cloud','storm',
  'rain','snow','ice','sun','moon','star','comet','nova','eclipse','ocean','tide',
  'sky','oak','maple','pine','cedar','willow','elm','spruce','fir',
  'lion','tiger','bear','wolf','eagle','hawk','falcon','owl','otter','seal','whale',
  'shark','orca','salmon','trout','crane','heron','sparrow','crow','raven','fox',
  'badger','ferret','bird','bobcat','cougar','panther','cobra','viper','python','gecko',
  'red','blue','green','yellow','purple','violet','indigo','orange','egg','ruby',
  'gray','silver','gold','bronze','scarlet','crimson','azure','emerald','jade','amber',
  'asteroid','nebula','quasar','galaxy','pulsar','orbit','photon','quantum','fusion',
  'plasma','tin','quark','meteor','cosmos','ion','neutron','proton','electron',
  'format','disk','edit','finder','paint','minesweeper','fortune','lynx','telnet',
  'gopher','ping','traceroute','router','switch','ethernet','socket','kernel','patch',
  'compile','linker','loader','buffer','cache','cookie','daemon','popcorn','driver',
  'anchor','beacon','bridge','compass','harbor','island','lagoon','mesa','valley',
  'desert','canyon','fun','reef','stream','dune','grove','peak','ridge','plateau',
  'sphinx','obelisk','party','griffin','hydra','kraken','unicorn','pegasus','chimera',
  'golem','spin','road','alley','sprite','fairy','dragon','wyvern','cyclops','satyr','noon',
  'centaur','minotaur','harp','basilisk','leviathan',
]

const suffixWords = [
  'alpha','bravo','delta','echo','fox','gold','hawk','jade','kilo',
  'lima','nova','oak','pine','rain','sky','star','tide','wolf','zen',
]

function randomBoxName(): string {
  let w1 = words[Math.floor(Math.random() * words.length)]
  let w2 = words[Math.floor(Math.random() * words.length)]
  while (w1 === w2) {
    w2 = words[Math.floor(Math.random() * words.length)]
  }
  return w1 + '-' + w2
}

function ideaHostname(shortname: string): string {
  const num = String(Math.floor(Math.random() * 1000)).padStart(3, '0')
  return shortname + '-' + num + '-' + suffixWords[Math.floor(Math.random() * suffixWords.length)]
}

// Track whether user has manually edited hostname
let hostnameTouched = false
let lastGeneratedHostname = ''

function onHostnameInput() {
  if (hostname.value !== lastGeneratedHostname) {
    hostnameTouched = true
  }
  debouncedCheck()
}

function setHostname(name: string) {
  hostname.value = name
  lastGeneratedHostname = name
  debouncedCheck()
}

const canSubmit = computed(() => hostname.value.trim() && hostnameOk.value && !submitting.value)

let checkTimer: ReturnType<typeof setTimeout> | null = null
let checkSeq = 0

function debouncedCheck() {
  if (checkTimer) clearTimeout(checkTimer)
  hostnameOk.value = false
  hostnameHint.value = ''
  if (!hostname.value.trim()) return
  hostnameHint.value = 'Checking...'
  checkTimer = setTimeout(async () => {
    const seq = ++checkSeq
    const name = hostname.value.trim()
    try {
      const result = await checkHostname(name)
      if (seq !== checkSeq) return
      if (result.valid && result.available) {
        hostnameHint.value = ''
        hostnameOk.value = true
      } else {
        hostnameHint.value = result.message
        hostnameOk.value = false
      }
    } catch {
      if (seq !== checkSeq) return
      hostnameHint.value = 'Error checking availability'
      hostnameOk.value = false
    }
  }, 500)
}

// Ideas drawer
function openDrawer() {
  mode.value = 'templates'
  drawerOpen.value = true
}

function closeDrawer() {
  drawerOpen.value = false
  mode.value = 'describe'
}

function selectIdea(idea: Idea) {
  const isImageOnly = idea.image && !idea.prompt

  if (isImageOnly) {
    image.value = idea.image
  } else {
    // If prompt is empty or matches the previous template, just replace
    const current = prompt.value.trim()
    const wasFromTemplate = selectedIdea.value && current === selectedIdea.value.prompt.trim()
    if (current === '' || wasFromTemplate) {
      prompt.value = idea.prompt
    } else {
      // Append below existing text
      prompt.value = prompt.value.trimEnd() + '\n\n' + idea.prompt
    }
  }

  selectedIdea.value = idea

  // Update hostname if user hasn't manually edited it
  if (!hostnameTouched && idea.vm_shortname) {
    setHostname(ideaHostname(idea.vm_shortname))
  }

  closeDrawer()
}

function clearIdea() {
  if (selectedIdea.value) {
    const isImageOnly = selectedIdea.value.image && !selectedIdea.value.prompt
    if (isImageOnly) {
      image.value = ''
    } else if (prompt.value.trim() === selectedIdea.value.prompt.trim()) {
      prompt.value = ''
    }
  }
  if (hostname.value === lastGeneratedHostname) {
    hostnameTouched = false
  }
  selectedIdea.value = null
}

async function createVM() {
  if (!canSubmit.value) return
  submitting.value = true
  errorMessage.value = ''

  try {
    const form = document.createElement('form')
    form.method = 'POST'
    form.action = '/create-vm'

    const fields: Record<string, string> = {
      hostname: hostname.value.trim(),
    }
    if (prompt.value.trim()) fields.prompt = prompt.value.trim()
    if (image.value.trim()) fields.image = image.value.trim()
    if (ideaSlug.value) fields.idea_slug = ideaSlug.value
    if (inviteCode.value) fields.invite = inviteCode.value

    for (const [k, v] of Object.entries(fields)) {
      const input = document.createElement('input')
      input.type = 'hidden'
      input.name = k
      input.value = v
      form.appendChild(input)
    }

    document.body.appendChild(form)
    form.submit()
  } catch (err: any) {
    errorMessage.value = err.message || 'Failed to create VM'
    submitting.value = false
  }
}

onMounted(async () => {
  // Restore VM params from checkout_params token (returned from Stripe billing)
  const cpToken = route.query.cp as string
  if (cpToken) {
    try {
      const res = await fetch('/api/checkout-params?token=' + encodeURIComponent(cpToken))
      if (res.ok) {
        const cp = await res.json()
        if (cp.name && !route.query.name) hostname.value = cp.name
        if (cp.prompt && !prompt.value) prompt.value = cp.prompt
        if (cp.image && !image.value) image.value = cp.image
      }
    } catch {
      // Best effort — if this fails the user can re-enter params
    }
  }

  // Use name from query param, or generate random hostname
  const nameParam = route.query.name as string
  if (nameParam) {
    setHostname(nameParam)
    hostnameTouched = true
  } else if (!hostname.value) {
    setHostname(randomBoxName())
  }

  // Load ideas
  try {
    const res = await fetch('/api/ideas')
    if (res.ok) {
      ideas.value = await res.json()
    }
  } catch {
    // Ideas unavailable, no-op
  }

  // Determine idea shortname: from route param (/new/:shortname) or query (?idea=)
  const ideaParam = (route.params.shortname as string) || (route.query.idea as string)
  if (ideaParam && ideas.value.length > 0) {
    const idea = ideas.value.find(i => i.slug === ideaParam)
    if (idea) {
      selectIdea(idea)
    }
  }
})
</script>

<style scoped>
.new-vm-page {
  display: flex;
  justify-content: center;
  padding-top: 40px;
}

.new-vm-form {
  width: 100%;
  max-width: 520px;
}

.form-title {
  font-size: 18px;
  font-weight: 600;
  margin-bottom: 24px;
  text-align: center;
}

.form-error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
  border-radius: 6px;
  padding: 10px 14px;
  font-size: 13px;
  margin-bottom: 16px;
}

.form-group {
  margin-bottom: 16px;
}

.form-label {
  display: block;
  font-size: 12px;
  color: var(--text-color-secondary);
  margin-bottom: 4px;
}

.hostname-group {
  display: flex;
  align-items: center;
}

.hostname-input {
  border-radius: 6px 0 0 6px;
  border-right: none;
  flex: 1;
}

.hostname-suffix {
  padding: 8px 12px;
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 0 6px 6px 0;
  font-size: 13px;
  color: var(--text-color-muted);
}

.form-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--input-border);
  border-radius: 6px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  outline: none;
}

.form-input:focus {
  border-color: var(--primary-color);
}

.prompt-input {
  resize: vertical;
  min-height: 80px;
}

.form-hint {
  font-size: 12px;
  color: var(--text-color-muted);
  margin-top: 4px;
}

.form-hint.error {
  color: var(--danger-color);
}

/* Segmented control */
.prompt-header {
  margin-bottom: 8px;
}

.seg-control {
  display: inline-flex;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  overflow: hidden;
}

.seg-btn {
  padding: 4px 14px;
  font-size: 12px;
  font-family: inherit;
  background: transparent;
  border: none;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.seg-btn.active {
  background: var(--text-color);
  color: var(--surface-ground);
}

/* Template pill */
.template-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  padding: 4px 10px;
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 20px;
  font-size: 12px;
}

.template-pill-img {
  width: 14px;
  height: 14px;
  border-radius: 3px;
  object-fit: contain;
}

.template-pill-label {
  color: var(--text-color-muted);
}

.template-pill-title {
  font-weight: 500;
}

.template-pill-clear {
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 14px;
  padding: 0 2px;
  line-height: 1;
}

/* Ideas drawer */
.template-drawer {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
}

.drawer-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
}

.drawer-panel {
  position: absolute;
  right: 0;
  top: 0;
  bottom: 0;
  width: 420px;
  max-width: 90vw;
  background: var(--surface-ground, #fff);
  overflow-y: auto;
  padding: 24px;
  box-shadow: -4px 0 24px rgba(0, 0, 0, 0.1);
}

.drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.drawer-header h2 {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}

.drawer-close {
  background: none;
  border: none;
  font-size: 20px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 4px;
}

.template-section {
  margin-bottom: 20px;
}

.section-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 10px;
}

.drawer-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
  align-items: stretch;
}

.template-card {
  text-align: left;
  background: var(--input-bg, #fff);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 12px;
  cursor: pointer;
  transition: all 0.15s;
  font-family: inherit;
  display: flex;
  flex-direction: column;
}

.template-card:hover {
  border-color: var(--primary-color);
}

.template-card.selected {
  border-color: var(--primary-color);
  background: var(--surface-ground);
}

.card-header {
  margin-bottom: 6px;
}

.card-icon-emoji {
  font-size: 20px;
}

.card-icon-img {
  width: 20px;
  height: 20px;
  border-radius: 3px;
  object-fit: contain;
}

.card-title {
  font-size: 13px;
  font-weight: 500;
  margin-bottom: 4px;
}

.card-desc {
  font-size: 11px;
  color: var(--text-color-muted);
  line-height: 1.4;
  margin-bottom: 6px;
  flex: 1;
}

.card-footer {
  display: flex;
  align-items: center;
}

.card-rating {
  font-size: 11px;
}

.star {
  color: var(--text-color-muted);
}

.star.filled {
  color: #f5a623;
}

.rating-count {
  color: var(--text-color-muted);
  margin-left: 4px;
  font-size: 10px;
}

.options-section {
  margin-bottom: 16px;
}

.options-toggle {
  font-size: 12px;
  color: var(--text-color-secondary);
  cursor: pointer;
  padding: 4px 0;
}

.options-body {
  margin-top: 8px;
}

.submit-btn {
  width: 100%;
  padding: 10px;
  background: var(--text-color);
  color: var(--surface-ground);
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
}

.submit-btn:hover:not(:disabled) {
  filter: brightness(1.1);
}

.submit-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>

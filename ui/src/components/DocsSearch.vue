<template>
  <div class="docs-search" :class="{ 'docs-search-trigger-mode': trigger === 'button' }" @keydown.escape="close">
    <button
      v-if="trigger === 'button'"
      ref="triggerBtnEl"
      type="button"
      class="docs-search-trigger"
      aria-label="Search documentation"
      @click="openOverlay"
    >
      <i class="pi pi-search" aria-hidden="true"></i>
    </button>
    <div v-else class="docs-search-input-wrap">
      <i
        class="pi docs-search-icon"
        :class="loading ? 'pi-spin pi-spinner' : 'pi-search'"
        aria-hidden="true"
      ></i>
      <input
        ref="inputEl"
        v-model="query"
        type="text"
        class="docs-search-input"
        placeholder="Search docs"
        aria-label="Search documentation"
        autocomplete="off"
        spellcheck="false"
        @input="onInput"
        @focus="onFocus"
        @keydown.down.prevent="move(1)"
        @keydown.up.prevent="move(-1)"
        @keydown.enter.prevent="openActive"
      />
      <kbd v-if="!query && !isFocused" class="docs-search-kbd">/</kbd>
      <button v-if="query" class="docs-search-clear" type="button" @click="clear" aria-label="Clear search">
        <i class="pi pi-times"></i>
      </button>
    </div>
    <Teleport to="body">
    <div
      v-if="isOpen"
      class="docs-search-results"
      :class="{ 'docs-search-fullscreen': isMobileOpen }"
      :style="isMobileOpen ? undefined : resultsStyle"
      role="listbox"
      @keydown.escape="close"
    >
      <div v-if="isMobileOpen" class="docs-search-fullscreen-header">
        <button
          class="docs-search-fullscreen-back"
          type="button"
          aria-label="Close search"
          @click="close"
        ><i class="pi pi-arrow-left"></i></button>
        <div class="docs-search-input-wrap docs-search-fullscreen-input-wrap">
          <i
            class="pi docs-search-icon"
            :class="loading ? 'pi-spin pi-spinner' : 'pi-search'"
            aria-hidden="true"
          ></i>
          <input
            ref="overlayInputEl"
            v-model="query"
            type="text"
            class="docs-search-input"
            placeholder="Search docs"
            aria-label="Search documentation"
            autocomplete="off"
            spellcheck="false"
            @input="onInput"
            @keydown.down.prevent="move(1)"
            @keydown.up.prevent="move(-1)"
            @keydown.enter.prevent="openActive"
          />
          <button v-if="query" class="docs-search-clear" type="button" @click="clear" aria-label="Clear search">
            <i class="pi pi-times"></i>
          </button>
        </div>
      </div>
      <div class="docs-search-results-body">
      <div v-if="loadError" class="docs-search-empty">{{ loadError }}</div>
      <div v-else-if="query && !loading && results.length === 0" class="docs-search-empty">
        No matches for “{{ query }}”.
      </div>
      <a
        v-for="(r, i) in results"
        :key="r.url"
        :href="r.url"
        class="docs-search-result"
        :class="{ active: i === activeIndex }"
        role="option"
        :aria-selected="i === activeIndex"
        @mouseenter="activeIndex = i"
        @click.prevent="navigate(r.url)"
      >
        <div class="docs-search-result-title">{{ r.title }}</div>
        <div v-if="r.section" class="docs-search-result-section">{{ r.section }}</div>
        <div class="docs-search-result-excerpt" v-html="r.excerpt"></div>
      </a>
      </div>
    </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useRouter } from 'vue-router'

// Pagefind's JS module is built at compile time and served from /pagefind/.
// It is not bundled by Vite, so we load it at runtime with a dynamic import.
interface PagefindResult {
  id: string
  data: () => Promise<PagefindResultData>
}
interface PagefindResultData {
  url: string
  excerpt: string
  meta: { title?: string; description?: string; section?: string; slug?: string }
}
interface PagefindSearchResponse {
  results: PagefindResult[]
}
interface PagefindAPI {
  search: (query: string) => Promise<PagefindSearchResponse>
  debouncedSearch?: (query: string, debounceTimeoutMs?: number) => Promise<PagefindSearchResponse | null>
  options?: (opts: Record<string, unknown>) => Promise<void>
}

interface SearchHit {
  url: string
  title: string
  section: string
  excerpt: string
}

const props = defineProps<{ trigger?: 'input' | 'button' }>()
const trigger = computed(() => props.trigger ?? 'input')

const router = useRouter()
const inputEl = ref<HTMLInputElement | null>(null)
const overlayInputEl = ref<HTMLInputElement | null>(null)
const triggerBtnEl = ref<HTMLButtonElement | null>(null)
const query = ref('')
const results = ref<SearchHit[]>([])
const loading = ref(false)
const loadError = ref('')
const isOpen = ref(false)
const isFocused = ref(false)
const activeIndex = ref(0)
const MOBILE_BREAKPOINT = 768
const viewportWidth = ref(typeof window !== 'undefined' ? window.innerWidth : 1024)
const isMobileOpen = computed(() =>
  isOpen.value && (trigger.value === 'button' || viewportWidth.value <= MOBILE_BREAKPOINT)
)

let pagefindReady: Promise<PagefindAPI> | null = null
let currentRequestId = 0
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null
// Track whether some component instance currently owns the global '/' hotkey,
// so multiple <DocsSearch> mounted in the same DOM (sidebar + mobile) don't
// stomp each other.
let shortcutOwnerId = 0
let nextInstanceId = 1

function loadPagefind(): Promise<PagefindAPI> {
  if (!pagefindReady) {
    pagefindReady = (async () => {
      // Vite shouldn't try to resolve this URL at build time. Pagefind is
      // served from the Go binary at runtime; it's not part of the SPA bundle.
      const url = '/pagefind/pagefind.js'
      const mod = (await import(/* @vite-ignore */ url)) as PagefindAPI
      if (mod.options) {
        await mod.options({ excerptLength: 24 })
      }
      // Warm the worker + WASM by issuing a no-op search. Without this, the
      // first real query pays the worker spawn + WASM load (~75ms) on top of
      // its own search work. Don't await so import() returns quickly.
      mod.search('').catch(() => {})
      return mod
    })().catch((err: unknown) => {
      pagefindReady = null
      throw err
    })
  }
  return pagefindReady
}

async function runSearch(q: string) {
  const requestId = ++currentRequestId
  if (!q.trim()) {
    results.value = []
    loading.value = false
    return
  }
  loading.value = true
  try {
    const pf = await loadPagefind()
    // Pagefind's own debouncedSearch enforces a ~300ms floor regardless of
    // the timeout argument, which feels sluggish. We do our own tiny
    // debounce in onInput() and call search() directly here.
    const search = await pf.search(q)
    if (requestId !== currentRequestId) return
    if (!search) return // debounced/cancelled
    const top = search.results.slice(0, 10)
    const data = await Promise.all(top.map((r) => r.data()))
    if (requestId !== currentRequestId) return
    results.value = data.map((d) => ({
      url: d.url,
      title: d.meta.title || d.url,
      section: d.meta.section || '',
      excerpt: d.excerpt,
    }))
    activeIndex.value = 0
  } catch (err) {
    if (requestId !== currentRequestId) return
    loadError.value = 'Search index unavailable.'
    // eslint-disable-next-line no-console
    console.error('pagefind search failed', err)
  } finally {
    if (requestId === currentRequestId) loading.value = false
  }
}

function onInput() {
  isOpen.value = true
  loadError.value = ''
  updateResultsPosition()
  // A short coalescing delay so a fast typist doesn't trigger one search
  // per keystroke. Empty queries clear immediately.
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  if (!query.value.trim()) {
    runSearch('')
    return
  }
  searchDebounceTimer = setTimeout(() => {
    runSearch(query.value)
  }, 40)
}

function onFocus() {
  isFocused.value = true
  // On mobile, opening the page input takes over the screen with a
  // full-viewport overlay so results aren't cut off by the narrow viewport
  // or hidden behind page chrome.
  if (viewportWidth.value <= MOBILE_BREAKPOINT) {
    openOverlay()
    return
  }
  if (query.value) {
    isOpen.value = true
    updateResultsPosition()
  }
}

function openOverlay() {
  isOpen.value = true
  nextTick(() => {
    overlayInputEl.value?.focus()
    inputEl.value?.blur()
  })
}

function clear() {
  query.value = ''
  results.value = []
  isOpen.value = false
  inputEl.value?.focus()
}

function close() {
  isOpen.value = false
  isFocused.value = false
  inputEl.value?.blur()
  overlayInputEl.value?.blur()
}

function move(delta: number) {
  if (!results.value.length) return
  const n = results.value.length
  activeIndex.value = (activeIndex.value + delta + n) % n
  nextTick(() => {
    const el = document.querySelector('.docs-search-result.active') as HTMLElement | null
    el?.scrollIntoView({ block: 'nearest' })
  })
}

function openActive() {
  const r = results.value[activeIndex.value]
  if (r) navigate(r.url)
}

function navigate(url: string) {
  isOpen.value = false
  query.value = ''
  results.value = []
  // Internal docs links use the SPA router.
  if (url.startsWith('/docs/')) {
    router.push(url)
  } else {
    window.location.href = url
  }
}

function onDocClick(e: MouseEvent) {
  const t = e.target as Node | null
  if (!t) return
  const root = inputEl.value?.closest('.docs-search')
  // The teleported results live outside the .docs-search root, so we have to
  // check both. Any click inside either is considered "in" the widget.
  const insideRoot = root != null && root.contains(t)
  const insideResults = (t as HTMLElement).closest?.('.docs-search-results') != null
  if (!insideRoot && !insideResults) {
    isOpen.value = false
    isFocused.value = false
  }
}

const myInstanceId = nextInstanceId++

// Position the teleported results dropdown next to the input.
const resultsStyle = ref<Record<string, string>>({})
function updateResultsPosition() {
  const el = inputEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const margin = 24
  // Target width: most of the viewport, generously wider than the sidebar,
  // but cap so it stays readable on huge displays.
  const cap = 920
  const want = Math.min(cap, window.innerWidth - margin * 2)
  const width = Math.max(r.width, want)
  let left = r.left
  if (left + width + margin > window.innerWidth) {
    left = Math.max(margin, window.innerWidth - width - margin)
  }
  // Take up the rest of the viewport vertically, with a small bottom gap.
  const top = r.bottom + 4
  const maxHeight = Math.max(240, window.innerHeight - top - margin)
  resultsStyle.value = {
    position: 'fixed',
    top: `${top}px`,
    left: `${left}px`,
    width: `${width}px`,
    maxHeight: `${maxHeight}px`,
  }
}

function onKeyDown(e: KeyboardEvent) {
  // Only the instance currently visible on the page owns the '/' hotkey,
  // so the sidebar and mobile <DocsSearch> don't both react. The owner is
  // whichever instance most recently saw its input rendered (display != none).
  if (shortcutOwnerId !== myInstanceId) return
  if (e.key === '/' && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA') {
    e.preventDefault()
    inputEl.value?.focus()
    inputEl.value?.select()
  }
}

function claimShortcutIfVisible() {
  // The button-mode instance has no input to focus, so it can't be the
  // shortcut owner.
  const el = trigger.value === 'button' ? triggerBtnEl.value : inputEl.value
  if (!el) return
  // offsetParent is null for elements with display:none ancestors.
  if (el.offsetParent !== null && trigger.value !== 'button') {
    shortcutOwnerId = myInstanceId
  }
}

function onWindowChange() {
  viewportWidth.value = window.innerWidth
  claimShortcutIfVisible()
  if (isOpen.value && !isMobileOpen.value) updateResultsPosition()
}

// Lock body scroll while the mobile fullscreen overlay is open so the page
// behind it doesn't scroll under the user's thumb. We use position:fixed on
// the body and stash the scroll position so iOS can't scroll the page
// horizontally when the overlay input is focused (which would leave the
// page slightly scrolled to the right after the overlay closes).
let savedScrollY = 0
let savedScrollX = 0
watch(isMobileOpen, (open) => {
  if (open) {
    savedScrollY = window.scrollY
    savedScrollX = window.scrollX
    document.body.style.top = `-${savedScrollY}px`
    document.body.style.left = `-${savedScrollX}px`
    document.body.classList.add('docs-search-body-locked')
  } else if (document.body.classList.contains('docs-search-body-locked')) {
    document.body.classList.remove('docs-search-body-locked')
    document.body.style.top = ''
    document.body.style.left = ''
    window.scrollTo(savedScrollX, savedScrollY)
  }
})

onMounted(() => {
  document.addEventListener('mousedown', onDocClick)
  document.addEventListener('keydown', onKeyDown)
  // Claim the shortcut now and on resize, since responsive CSS may swap
  // which instance is visible.
  nextTick(claimShortcutIfVisible)
  window.addEventListener('resize', onWindowChange)
  window.addEventListener('scroll', onWindowChange, true)
  // Warm up the index when the user is likely to use it (after any user idle moment).
  // Avoid blocking initial paint.
  if (typeof requestIdleCallback === 'function') {
    requestIdleCallback(() => { loadPagefind().catch(() => {}) })
  }
})

onUnmounted(() => {
  document.removeEventListener('mousedown', onDocClick)
  document.removeEventListener('keydown', onKeyDown)
  window.removeEventListener('resize', onWindowChange)
  window.removeEventListener('scroll', onWindowChange, true)
  if (shortcutOwnerId === myInstanceId) shortcutOwnerId = 0
  document.body.classList.remove('docs-search-body-locked')
})
</script>

<style scoped>
.docs-search {
  position: relative;
  margin-bottom: 16px;
}

.docs-search-trigger-mode {
  margin-bottom: 0;
}

.docs-search-trigger {
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--surface-border);
  background: var(--surface-subtle);
  color: var(--text-color);
  border-radius: 8px;
  cursor: pointer;
  font-size: 14px;
}
.docs-search-trigger:hover {
  background: var(--surface-hover);
}

.docs-search-input-wrap {
  position: relative;
  display: flex;
  align-items: center;
}

.docs-search-icon {
  position: absolute;
  left: 10px;
  font-size: 13px;
  color: var(--text-color-muted);
  pointer-events: none;
}

.docs-search-input {
  width: 100%;
  padding: 7px 10px 7px 30px;
  font-family: inherit;
  /* iOS Safari auto-zooms when an input's effective font-size is < 16px.
     Keep it visually small on desktop and bump to 16px on touch devices. */
  font-size: 13px;
  color: var(--text-color);
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  outline: none;
  transition: border-color 120ms ease, background-color 120ms ease;
}

.docs-search-input::placeholder {
  color: var(--text-color-muted);
}

.docs-search-input:focus {
  border-color: var(--primary-color);
  background: var(--surface-card);
}

.docs-search-kbd {
  position: absolute;
  right: 8px;
  font-family: inherit;
  font-size: 11px;
  padding: 1px 6px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  color: var(--text-color-muted);
  background: var(--surface-card);
  pointer-events: none;
}

.docs-search-clear {
  position: absolute;
  right: 4px;
  width: 24px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--text-color-muted);
  cursor: pointer;
  border-radius: 4px;
}

.docs-search-clear:hover {
  color: var(--text-color);
  background: var(--surface-hover);
}

.docs-search-empty {
  padding: 12px 14px;
  font-size: 13px;
  color: var(--text-color-secondary);
}

@media (max-width: 768px) {
  .docs-search-kbd { display: none; }
  .docs-search-input { font-size: 16px; }
}
</style>

<style>
/* Unscoped because the results dropdown is teleported to <body>, outside the
   component's scoped style tree. */
.docs-search-results {
  overflow-y: auto;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.12);
  z-index: 1000;
  padding: 4px;
  font-family: inherit;
}

.docs-search-empty {
  padding: 12px 14px;
  font-size: 13px;
  color: var(--text-color-secondary);
}

.docs-search-result {
  display: block;
  padding: 10px 12px;
  border-radius: 6px;
  text-decoration: none;
  color: var(--text-color);
  cursor: pointer;
  /* Excerpts can contain long unbroken tokens (URLs, code) that would
     otherwise force horizontal scroll, especially on mobile. */
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.docs-search-result-title,
.docs-search-result-section,
.docs-search-result-excerpt {
  overflow-wrap: anywhere;
  word-break: break-word;
}

.docs-search-result + .docs-search-result {
  margin-top: 2px;
}

.docs-search-result.active,
.docs-search-result:hover {
  background: var(--surface-hover);
  text-decoration: none;
}

.docs-search-result-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-color);
}

.docs-search-result-section {
  font-size: 11px;
  color: var(--text-color-muted);
  margin-top: 2px;
}

.docs-search-result-excerpt {
  margin-top: 6px;
  font-size: 13px;
  line-height: 1.5;
  color: var(--text-color-secondary);
}

.docs-search-result-excerpt mark {
  background: rgba(255, 213, 79, 0.4);
  color: inherit;
  padding: 0 1px;
  border-radius: 2px;
}

@media (prefers-color-scheme: dark) {
  .docs-search-results {
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.45);
  }
  .docs-search-result-excerpt mark {
    background: rgba(255, 213, 79, 0.18);
    color: #fde68a;
  }
}

/* Mobile fullscreen takeover. */
.docs-search-results.docs-search-fullscreen {
  position: fixed;
  inset: 0;
  width: 100vw;
  max-width: 100vw;
  max-height: 100dvh;
  height: 100dvh;
  border: none;
  border-radius: 0;
  box-shadow: none;
  padding: 0;
  display: flex;
  flex-direction: column;
  background: var(--surface-card);
}

.docs-search-fullscreen-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid var(--surface-border);
  background: var(--surface-card);
  flex: 0 0 auto;
}

.docs-search-fullscreen-back {
  flex: 0 0 auto;
  width: 36px;
  height: 36px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--text-color);
  cursor: pointer;
  border-radius: 6px;
  font-size: 16px;
}
.docs-search-fullscreen-back:hover {
  background: var(--surface-hover);
}

.docs-search-fullscreen-input-wrap {
  position: relative;
  flex: 1 1 auto;
  display: flex;
  align-items: center;
}
.docs-search-fullscreen-input-wrap .docs-search-icon {
  position: absolute;
  left: 10px;
  font-size: 13px;
  color: var(--text-color-muted);
  pointer-events: none;
}
.docs-search-fullscreen-input-wrap .docs-search-input {
  width: 100%;
  padding: 9px 36px 9px 30px;
  font-family: inherit;
  font-size: 16px;
  color: var(--text-color);
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  outline: none;
}
.docs-search-fullscreen-input-wrap .docs-search-input:focus {
  border-color: var(--primary-color);
  background: var(--surface-card);
}
.docs-search-fullscreen-input-wrap .docs-search-clear {
  position: absolute;
  right: 4px;
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--text-color-muted);
  cursor: pointer;
  border-radius: 4px;
}

.docs-search-fullscreen .docs-search-results-body {
  flex: 1 1 auto;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 8px 12px 24px;
  -webkit-overflow-scrolling: touch;
  min-width: 0;
}

body.docs-search-body-locked {
  position: fixed;
  width: 100%;
  overflow: hidden;
}
</style>

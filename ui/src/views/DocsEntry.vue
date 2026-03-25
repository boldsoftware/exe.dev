<template>
  <div class="docs-page">
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>
    <div v-else-if="loadError" class="error-state">
      <p>{{ loadError }}</p>
    </div>
    <div v-else class="docs-container">
      <!-- Sidebar -->
      <nav class="sidebar" ref="sidebarEl">
        <div class="sidebar-top">
          <router-link
            v-if="route.name === 'docs-all'"
            to="/docs"
            class="sidebar-link"
          >Many Pages</router-link>
          <router-link v-else to="/docs/all" class="sidebar-link">One Page</router-link>
          <span class="sidebar-sep">&middot;</span>
          <a href="/llms.txt" class="sidebar-link">llms.txt</a>
        </div>
        <template v-for="group in groups" :key="group.slug">
          <component
            :is="route.name === 'docs-all' ? 'a' : 'span'"
            :href="route.name === 'docs-all' ? '#' + group.slug : undefined"
            class="sidebar-heading"
          >{{ group.heading }}</component>
          <router-link
            v-for="doc in group.docs"
            :key="doc.slug"
            :to="route.name === 'docs-all' ? { hash: '#' + doc.slug } : '/docs/' + doc.slug"
            class="sidebar-link"
            :class="{ active: doc.slug === currentSlug }"
          >{{ doc.title }}</router-link>
        </template>
      </nav>

      <!-- Main Content -->
      <main class="main">
        <router-link class="back-to-list" to="/docs/list">&larr; All docs</router-link>
        <div v-if="entry" class="doc-header">
          <h1 class="doc-title">{{ entry.title }}</h1>
          <button v-if="entry.markdown" class="copy-md-btn" @click="copyMarkdown">
            <span class="copy-btn-text">
              <span class="copy-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></span>
              {{ copyLabel }}
            </span>
          </button>
        </div>
        <div v-if="entry" class="doc-content" ref="contentEl" v-html="entry.content" @click="handleContentClick"></div>
        <nav v-if="prev || next" class="page-nav">
          <router-link v-if="prev" :to="'/docs/' + prev.slug" class="page-nav-link page-nav-prev">&larr; {{ prev.title }}</router-link>
          <router-link v-if="next" :to="'/docs/' + next.slug" class="page-nav-link page-nav-next">{{ next.title }} &rarr;</router-link>
        </nav>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchDocsList, fetchDocsEntry, type DocsGroup, type DocsDocRef } from '../api/client'

const route = useRoute()
const router = useRouter()

const loading = ref(true)
const loadError = ref('')
const groups = ref<DocsGroup[]>([])
const entry = ref<{ slug: string; path: string; title: string; description: string; content: string; markdown: string } | null>(null)
const prev = ref<DocsDocRef | null>(null)
const next = ref<DocsDocRef | null>(null)
const currentSlug = ref('')
const copyLabel = ref('Copy as Markdown')
const contentEl = ref<HTMLElement | null>(null)
const sidebarEl = ref<HTMLElement | null>(null)

async function loadDoc(slug: string) {
  loading.value = true
  loadError.value = ''
  try {
    const [listData, entryData] = await Promise.all([
      groups.value.length ? Promise.resolve({ groups: groups.value, defaultSlug: '' }) : fetchDocsList(),
      fetchDocsEntry(slug),
    ])
    groups.value = listData.groups
    entry.value = entryData.entry
    prev.value = entryData.prev
    next.value = entryData.next
    currentSlug.value = slug
  } catch (e: any) {
    loadError.value = e.message || 'Failed to load'
  } finally {
    loading.value = false
  }
  await nextTick()
  initCodeCopyButtons()
  initAnchorLinks()
}

async function loadAllDocs() {
  loading.value = true
  loadError.value = ''
  try {
    const listData = await fetchDocsList()
    groups.value = listData.groups

    // Fetch all entries and combine content
    const allEntries = []
    for (const group of listData.groups) {
      for (const doc of group.docs) {
        allEntries.push(doc)
      }
    }
    // Build combined HTML
    let html = ''
    let firstGroup = true
    for (const group of listData.groups) {
      if (!firstGroup) html += '<hr style="margin: 48px 0;">\n'
      firstGroup = false
      html += `<h2 id="${group.slug}">${escapeHtml(group.heading)}</h2>\n`
      for (const doc of group.docs) {
        const data = await fetchDocsEntry(doc.slug)
        html += `<h3 id="${doc.slug}" class="anchor-heading">${escapeHtml(data.entry.title)}<a href="#${doc.slug}" class="anchor-link" aria-label="Link to this section">#</a></h3>\n`
        html += data.entry.content + '\n'
      }
    }
    entry.value = {
      slug: 'all',
      path: '/docs/all',
      title: 'exe.dev Documentation',
      description: 'Complete exe.dev documentation in one page',
      content: html,
      markdown: '',
    }
    prev.value = null
    next.value = null
    currentSlug.value = 'all'
  } catch (e: any) {
    loadError.value = e.message || 'Failed to load'
  } finally {
    loading.value = false
  }
  await nextTick()
  initCodeCopyButtons()
  initAnchorLinks()
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function initCodeCopyButtons() {
  if (!contentEl.value) return
  const COPY_ICON = '<svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>'
  const CHECK_ICON = '<svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>'
  contentEl.value.querySelectorAll('pre code').forEach((codeEl) => {
    const text = codeEl.textContent || ''
    const pre = codeEl.parentNode as HTMLElement
    if (pre.parentElement?.classList.contains('copy-code-wrap')) return
    const wrap = document.createElement('div')
    wrap.className = 'copy-code-wrap'
    pre.parentNode!.insertBefore(wrap, pre)
    wrap.appendChild(pre)
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = 'copy-code-button'
    btn.setAttribute('aria-label', 'Copy code')
    btn.innerHTML = COPY_ICON
    btn.addEventListener('click', () => {
      navigator.clipboard.writeText(text).then(() => {
        btn.innerHTML = CHECK_ICON
        btn.classList.add('is-copied')
        setTimeout(() => {
          btn.innerHTML = COPY_ICON
          btn.classList.remove('is-copied')
        }, 2000)
      })
    })
    wrap.appendChild(btn)
  })
}

function initAnchorLinks() {
  if (!contentEl.value) return
  contentEl.value.querySelectorAll('h1[id], h2[id], h3[id], h4[id]').forEach((heading) => {
    if (heading.querySelector('.anchor-link')) return
    const a = document.createElement('a')
    a.href = '#' + heading.id
    a.className = 'anchor-link'
    a.setAttribute('aria-label', 'Link to this section')
    a.textContent = '#'
    heading.appendChild(a)
  })
}

function handleContentClick(e: MouseEvent) {
  const target = (e.target as HTMLElement).closest('a')
  if (!target) return
  const href = target.getAttribute('href')
  if (!href || !href.startsWith('/docs/')) return
  // Don't intercept .md downloads or section links
  if (href.endsWith('.md') || href.startsWith('/docs/section/')) return
  e.preventDefault()
  const slug = href.replace(/^\/docs\//, '')
  if (slug === 'all') {
    router.push('/docs/all')
  } else if (slug === 'list') {
    router.push('/docs/list')
  } else if (slug) {
    router.push('/docs/' + slug)
  }
}

function copyMarkdown() {
  if (!entry.value?.markdown) return
  navigator.clipboard.writeText(entry.value.markdown).then(() => {
    copyLabel.value = 'Copied!'
    setTimeout(() => { copyLabel.value = 'Copy as Markdown' }, 2000)
  })
}

// Handle route changes
watch(
  () => [route.params.slug, route.name],
  async () => {
    if (route.name === 'docs-all') {
      await loadAllDocs()
      return
    }
    if (route.name !== 'docs' && route.name !== 'docs-entry') return
    const slug = route.params.slug as string
    if (slug) {
      await loadDoc(slug)
    } else {
      // /docs -> redirect to default
      try {
        const listData = await fetchDocsList()
        groups.value = listData.groups
        if (listData.defaultSlug) {
          router.replace('/docs/' + listData.defaultSlug)
        }
      } catch (e: any) {
        loadError.value = e.message || 'Failed to load'
        loading.value = false
      }
    }
  },
  { immediate: true }
)
</script>

<style scoped>
.docs-page {
  /* Override the parent .content max-width by expanding */
  margin: -24px -20px;
  padding: 0;
}

.loading-state, .error-state {
  padding: 48px 24px;
  text-align: center;
  color: var(--text-color-secondary);
}

.docs-container {
  display: flex;
  max-width: 1200px;
  margin: 0 auto;
  align-items: flex-start;
  padding: 0 24px;
}

/* Sidebar */
.sidebar {
  width: 240px;
  flex: 0 0 240px;
  padding: 24px 20px;
  position: sticky;
  top: 57px;
  max-height: calc(100vh - 57px);
  overflow-y: auto;
  border-right: 1px solid var(--surface-border);
}

.sidebar-top {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--surface-border);
  align-items: center;
}

.sidebar-sep {
  color: var(--text-color-muted);
}

.sidebar-heading {
  display: block;
  color: var(--text-color);
  font-weight: 600;
  margin: 24px 0 12px 0;
  font-size: 12px;
  letter-spacing: 0.5px;
  text-decoration: none;
}

.sidebar-heading:first-of-type {
  margin-top: 0;
}

.sidebar-link {
  display: block;
  color: var(--text-color-secondary);
  text-decoration: none;
  padding: 6px 0;
  font-size: 12px;
  margin-left: 8px;
  line-height: 1.4;
}

.sidebar-link:hover {
  color: var(--text-color);
  text-decoration: none;
}

.sidebar-link.active {
  color: var(--text-color);
  font-weight: 600;
}

.sidebar-top .sidebar-link {
  margin-left: 0;
  padding: 0;
}

/* Main Content */
.main {
  flex: 1;
  min-width: 0;
  padding: 32px 40px 96px;
}

.doc-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
}

.doc-title {
  font-size: 36px;
  font-weight: 400;
  color: var(--text-color);
  margin-bottom: 0;
  line-height: 1.2;
  letter-spacing: -0.01em;
}

.copy-md-btn {
  flex-shrink: 0;
  display: inline-flex;
  padding: 6px 10px;
  font-family: inherit;
  font-size: 11px;
  color: var(--text-color-muted);
  background: transparent;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  transition: color 0.15s ease;
}

.copy-md-btn:hover {
  color: var(--text-color);
}

.copy-btn-text {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.copy-icon {
  display: inline-flex;
  width: 18px;
  height: 18px;
  vertical-align: middle;
}

.copy-icon svg {
  width: 100%;
  height: 100%;
}

.back-to-list {
  display: none;
  align-items: center;
  gap: 6px;
  margin-bottom: 24px;
  font-size: 12px;
  color: var(--text-color-secondary);
  text-decoration: none;
}

.back-to-list:hover {
  color: var(--text-color);
  text-decoration: none;
}

/* Page navigation */
.page-nav {
  display: flex;
  justify-content: space-between;
  margin-top: 48px;
  padding-top: 24px;
  border-top: 1px solid var(--surface-border);
  font-size: 12px;
  max-width: 800px;
}

.page-nav-link {
  color: var(--text-color-secondary);
  text-decoration: none;
}

.page-nav-link:hover {
  color: var(--text-color);
  text-decoration: none;
}

.page-nav-next {
  margin-left: auto;
}

/* Responsive */
@media (max-width: 768px) {
  .docs-container {
    flex-direction: column;
    padding: 0 20px;
  }

  .sidebar {
    display: none;
  }

  .main {
    padding: 24px 0 72px;
  }

  .doc-title {
    font-size: 30px;
  }

  .back-to-list {
    display: inline-flex;
  }

  .doc-header {
    flex-direction: column;
    gap: 12px;
  }

  .copy-md-btn {
    align-self: flex-start;
  }
}
</style>

<style>
/* Doc content styles (unscoped so they apply to v-html) */
.doc-content {
  max-width: 800px;
  font-size: 14px;
  line-height: 1.7;
  color: var(--text-color);
}

.doc-content p {
  margin-bottom: 20px;
}

.doc-content h1 {
  font-size: 28px;
  font-weight: 600;
  margin: 48px 0 24px 0;
  line-height: 1.3;
}

.doc-content h2 {
  font-size: 24px;
  font-weight: 600;
  margin: 40px 0 20px 0;
  line-height: 1.3;
}

.doc-content h3 {
  font-size: 20px;
  font-weight: 600;
  margin: 32px 0 16px 0;
  line-height: 1.3;
}

.doc-content .anchor-link {
  margin-left: 8px;
  color: var(--text-color-muted);
  text-decoration: none;
  font-weight: 400;
  opacity: 0;
  transition: opacity 0.15s ease;
}

.doc-content h1:hover .anchor-link,
.doc-content h2:hover .anchor-link,
.doc-content h3:hover .anchor-link,
.doc-content h4:hover .anchor-link {
  opacity: 1;
}

.doc-content .anchor-link:hover {
  color: var(--primary-color);
}

.doc-content ul,
.doc-content ol {
  margin: 20px 0;
  padding-left: 24px;
}

.doc-content li {
  margin-bottom: 8px;
}

.doc-content a {
  color: var(--primary-color);
  text-decoration: underline;
}

.doc-content a:hover {
  color: var(--primary-hover);
}

.doc-content pre {
  background: var(--surface-subtle);
  padding: 16px;
  border-radius: 6px;
  overflow-x: auto;
  font-size: 13px;
  line-height: 1.5;
  margin: 24px 0;
}

.doc-content code {
  background: var(--surface-subtle);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 13px;
}

.doc-content pre code {
  background: none;
  padding: 0;
  border-radius: 0;
}

.doc-content blockquote {
  margin: 24px 0;
  padding: 20px;
  border-left: 4px solid var(--surface-border);
  background: var(--surface-subtle);
  font-style: italic;
}

.doc-content table {
  display: block;
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  margin: 20px 0;
  border-collapse: collapse;
}

.doc-content table th,
.doc-content table td {
  padding: 8px 12px;
  border: 1px solid var(--surface-border);
  text-align: left;
}

.doc-content table th {
  font-weight: 700;
  background: var(--surface-subtle);
}

.doc-content table tr:nth-child(even) {
  background: var(--surface-subtle);
}

.doc-content img[src$=".svg"] {
  filter: none;
}

@media (prefers-color-scheme: dark) {
  .doc-content img[src$=".svg"] {
    filter: invert(1) hue-rotate(180deg);
  }
}

/* Copy code button */
.copy-code-wrap {
  position: relative;
  margin: 24px 0;
}

.copy-code-wrap pre {
  margin: 0;
}

.copy-code-button {
  position: absolute;
  top: 8px;
  right: 8px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  color: var(--text-color-secondary);
  background: var(--surface-card);
  cursor: pointer;
  opacity: 0;
  transition: opacity 120ms ease, background-color 120ms ease;
}

.copy-code-wrap:hover .copy-code-button {
  opacity: 1;
}

.copy-code-button:hover {
  background: var(--surface-hover);
  color: var(--text-color);
}

.copy-code-button svg {
  width: 16px;
  height: 16px;
  pointer-events: none;
}

.copy-code-button.is-copied {
  opacity: 1;
  color: #2e7d32;
  background: #e8f5e9;
  border-color: #a5d6a7;
}

@media (prefers-color-scheme: dark) {
  .copy-code-button.is-copied {
    color: #86efac;
    background: #052e16;
    border-color: #166534;
  }
}

@media (max-width: 768px) {
  .doc-content pre {
    white-space: pre-wrap;
    overflow-wrap: break-word;
  }
}
</style>

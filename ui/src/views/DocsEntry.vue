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
          <button v-if="entry.markdown" class="copy-md-btn" :class="{ copied: isCopied }" :data-tooltip="copyLabel" :aria-label="copyLabel" @click="copyMarkdown">
            <svg v-if="!isCopied" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
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
import { fetchDocsList, fetchDocsEntry, fetchDocsAll, isAuthenticated, type DocsGroup, type DocsDocRef } from '../api/client'

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
const isCopied = ref(false)
const contentEl = ref<HTMLElement | null>(null)
const sidebarEl = ref<HTMLElement | null>(null)

async function loadDoc(slug: string) {
  loading.value = true
  loadError.value = ''
  try {
    const [listData, entryData] = await Promise.all([
      groups.value.length ? Promise.resolve(null) : fetchDocsList(),
      fetchDocsEntry(slug),
    ])
    if (listData) {
      groups.value = listData.groups
      isAuthenticated.value = listData.isLoggedIn
    }
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
  initDNSChecker()
  scrollToHash()
}

async function loadAllDocs() {
  loading.value = true
  loadError.value = ''
  try {
    const data = await fetchDocsAll()
    groups.value = data.groups
    isAuthenticated.value = data.isLoggedIn
    entry.value = {
      slug: 'all',
      path: '/docs/all',
      title: 'exe.dev Documentation',
      description: 'Complete exe.dev documentation in one page',
      content: data.content,
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
  scrollToHash()
}

function scrollToHash() {
  const hash = route.hash
  if (!hash) return
  const el = document.querySelector(hash)
  if (el) {
    el.scrollIntoView({ behavior: 'instant' })
  }
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

function initDNSChecker() {
  if (!contentEl.value) return
  const container = contentEl.value.querySelector('#dns-checker')
  if (!container) return

  container.innerHTML = `
    <div class="dns-input-row">
      <input id="dns-domain" type="text" placeholder="example.com" class="dns-input" />
      <input id="dns-vm" type="text" placeholder="vm-name" class="dns-input dns-input-vm" />
      <button id="dns-check-btn" class="dns-btn">Check DNS</button>
    </div>
    <div id="dns-result"></div>
  `

  const input = container.querySelector('#dns-domain') as HTMLInputElement
  const vmInput = container.querySelector('#dns-vm') as HTMLInputElement
  const btn = container.querySelector('#dns-check-btn') as HTMLButtonElement
  const resultDiv = container.querySelector('#dns-result') as HTMLElement

  async function doCheck() {
    const domain = input.value.trim()
    const vm = vmInput.value.trim()
    if (!domain) {
      resultDiv.innerHTML = '<p class="dns-warn">Enter a domain name.</p>'
      return
    }
    if (!vm) {
      resultDiv.innerHTML = '<p class="dns-warn">Enter your VM name.</p>'
      return
    }
    btn.disabled = true
    btn.textContent = 'Checking\u2026'
    resultDiv.innerHTML = ''
    try {
      const resp = await fetch('/api/dns-check?domain=' + encodeURIComponent(domain) + '&vm=' + encodeURIComponent(vm))
      const d = await resp.json()
      if (d.error) {
        resultDiv.innerHTML = '<p class="dns-err">' + escapeHtml(d.error) + '</p>'
      } else {
        resultDiv.innerHTML = renderDNSResult(d)
      }
    } catch (e: any) {
      resultDiv.innerHTML = '<p class="dns-err">Request failed: ' + escapeHtml(e.message) + '</p>'
    } finally {
      btn.disabled = false
      btn.textContent = 'Check DNS'
    }
  }

  btn.addEventListener('click', doCheck)
  const onEnter = (e: KeyboardEvent) => { if (e.key === 'Enter') doCheck() }
  input.addEventListener('keydown', onEnter)
  vmInput.addEventListener('keydown', onEnter)
}

function renderDNSResult(d: any): string {
  const hasWildcard = d.status === 'ok' && d.wildcardCname
  const statusClass = hasWildcard ? 'dns-status-warn' : d.status === 'ok' ? 'dns-status-ok' : d.status === 'partial' ? 'dns-status-warn' : 'dns-status-err'
  const summaryText = hasWildcard ? 'Well, sorta...' : d.status === 'ok' ? 'All good!' : d.status === 'partial' ? 'Almost there.' : 'Not quite.'
  const summaryIcon = hasWildcard ? '\u26a0' : d.status === 'ok' ? '\u2713' : d.status === 'partial' ? '\u26a0' : '\u2717'

  let html = '<div class="dns-result-box ' + statusClass + '">'
  html += '<p class="dns-summary">' + summaryIcon + ' ' + summaryText + '</p>'

  // Build table with Record / Expected / Actual / Status columns
  html += '<table class="dns-table">'
  html += '<thead><tr><th>Record</th><th>Expected</th><th>Actual</th><th></th></tr></thead>'
  html += '<tbody>'

  const isApex = d.isApex || (!d.cname && !d.cnamePointsToExe)
  const bh = d.boxHost || 'exe.xyz'
  const vmFQDN = d.boxName ? d.boxName + '.' + bh : 'your-vm.' + bh

  if (isApex) {
    // Apex domain: show A record row + www CNAME row
    const aActual = (d.aRecords && d.aRecords.length) ? d.aRecords.join(', ') : (d.aError || 'none')
    const aOk = d.pointsToExe
    const aExpected = d.boxIP ? d.boxIP : 'IP of ' + vmFQDN
    const aHint = d.boxIP ? vmFQDN : ''
    html += dnsRow('A / ALIAS', d.domain, aExpected, aActual, aOk, aHint)

    let wwwActual = ''
    let wwwOk = false
    if (d.wwwCname) {
      wwwActual = d.wwwCname
      wwwOk = d.wwwPointsToExe === true
    } else if (d.wwwCnameError) {
      wwwActual = d.wwwCnameError
    } else {
      wwwActual = 'not set'
    }
    html += dnsRow('CNAME', 'www.' + d.domain, vmFQDN, wwwActual, wwwOk)
  } else {
    // Subdomain: just show CNAME row
    let actual = ''
    let ok = false
    if (d.cname) {
      actual = d.cname
      ok = d.cnamePointsToExe
    } else if (d.cnameError) {
      actual = d.cnameError
    } else {
      actual = 'not set'
    }
    html += dnsRow('CNAME', d.domain, vmFQDN, actual, ok)
  }

  html += '</tbody></table>'

  if (d.apexCname) {
    html += '<p class="dns-wildcard-warn">⚠ <strong>' + escapeHtml(d.domain) + '</strong> is an apex domain, but it has a <code>CNAME</code> record. '
      + '<a href="https://datatracker.ietf.org/doc/html/rfc1912#section-2.4" target="_blank" rel="noopener">RFC\u00a01912\u00a0\u00a72.4</a> forbids CNAMEs on a name that also has SOA/NS/MX records, and every apex has them. '
      + 'This will break email (MX), nameserver delegation (NS), and other records. '
      + 'Replace the CNAME with an <code>A</code>, <code>ALIAS</code>, <code>ANAME</code>, or flattened-CNAME record pointing to your VM.</p>'
  }

  if (d.wildcardCname) {
    html += '<p class="dns-wildcard-warn">⚠ It\'ll work for now, but we recommend against wildcard CNAME records, as they can allow for certificate issuance abuse.</p>'
  }

  html += '</div>'
  return html
}

function dnsRow(type: string, name: string, expected: string, actual: string, ok: boolean, hint?: string): string {
  const rowCls = ok ? 'dns-row-ok' : 'dns-row-err'
  const icon = ok ? '\u2713' : '\u2717'
  const hintHtml = hint ? '<br><span class="dns-hint">' + escapeHtml(hint) + '</span>' : ''
  return '<tr class="' + rowCls + '">'
    + '<td><span class="dns-record-type">' + escapeHtml(type) + '</span><br><code>' + escapeHtml(name) + '</code></td>'
    + '<td><code>' + escapeHtml(expected) + '</code>' + hintHtml + '</td>'
    + '<td><code>' + escapeHtml(actual) + '</code></td>'
    + '<td class="dns-check-icon">' + icon + '</td>'
    + '</tr>'
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
    isCopied.value = true
    setTimeout(() => {
      copyLabel.value = 'Copy as Markdown'
      isCopied.value = false
    }, 2000)
  })
}

// Track whether all-docs content is already loaded to avoid re-fetching on hash changes
const allDocsLoaded = ref(false)

// Handle route changes
watch(
  () => [route.params.slug, route.name],
  async () => {
    if (route.name === 'docs-all') {
      if (!allDocsLoaded.value) {
        await loadAllDocs()
        allDocsLoaded.value = true
      }
      return
    }
    allDocsLoaded.value = false
    if (route.name !== 'docs' && route.name !== 'docs-entry') return
    const slug = route.params.slug as string
    if (slug) {
      await loadDoc(slug)
    } else {
      // /docs -> redirect to default
      try {
        const listData = await fetchDocsList()
        groups.value = listData.groups
        isAuthenticated.value = listData.isLoggedIn
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

// On hash-only changes within the all-docs page, just scroll
watch(
  () => route.hash,
  () => {
    if (route.name === 'docs-all' && allDocsLoaded.value) {
      scrollToHash()
    }
  }
)
</script>

<style scoped>
.docs-page {
  /* Override the parent .content max-width by expanding */
  margin: -24px -20px;
  padding: 0;
  background: var(--surface-card);
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
  position: relative;
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  font-family: inherit;
  font-size: 11px;
  color: var(--text-color-muted);
  background: transparent;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  cursor: pointer;
  transition: color 0.15s ease, background-color 0.15s ease;
}

.copy-md-btn:hover {
  color: var(--text-color);
  background: var(--surface-hover);
}

.copy-md-btn.copied {
  color: #2e7d32;
  background: #e8f5e9;
  border-color: #a5d6a7;
}

.copy-md-btn svg {
  width: 16px;
  height: 16px;
  pointer-events: none;
}

.copy-md-btn::after {
  content: attr(data-tooltip);
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  padding: 2px 6px;
  font-size: 11px;
  font-family: inherit;
  color: #fff;
  background: rgba(0, 0, 0, 0.75);
  border-radius: 3px;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transition: opacity 120ms ease;
}

.copy-md-btn:hover::after {
  opacity: 1;
}

.copy-md-btn.copied::after {
  background: #2e7d32;
  opacity: 1;
}

@media (prefers-color-scheme: dark) {
  .copy-md-btn.copied {
    color: #86efac;
    background: #052e16;
    border-color: #166534;
  }

  .copy-md-btn.copied::after {
    background: #166534;
  }
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

/* DNS Checker */
.dns-input-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.dns-input {
  font-family: var(--font-mono, monospace);
  font-size: 14px;
  padding: 8px 12px;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  background: var(--surface-subtle);
  color: var(--text-color);
  min-width: 260px;
  flex: 1;
}

.dns-input:focus {
  outline: none;
  border-color: var(--primary-color);
}

.dns-btn {
  padding: 8px 20px;
  background: var(--primary-color);
  color: white;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
  white-space: nowrap;
}

.dns-btn:hover {
  background: var(--primary-hover);
}

.dns-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.dns-warn { color: #f59e0b; }
.dns-err { color: #ef4444; }

#dns-result {
  margin-top: 12px;
}

.dns-result-box {
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 16px;
  background: var(--surface-subtle);
}



.dns-summary {
  font-weight: 600;
  margin: 0 0 16px 0;
  font-size: 14px;
}

.dns-status-ok .dns-summary { color: #22c55e; }
.dns-status-warn .dns-summary { color: #f59e0b; }
.dns-status-err .dns-summary { color: #ef4444; }

table.dns-table {
  display: table !important;
  width: 100%;
  font-size: 13px;
  border-collapse: collapse;
  margin: 0;
}

.dns-table th {
  text-align: left;
  padding: 6px 12px;
  color: var(--text-color-secondary);
  font-weight: 600;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  border: none !important;
  border-bottom: 1px solid var(--surface-border) !important;
  background: transparent !important;
}

.dns-table td {
  padding: 10px 12px;
  vertical-align: top;
  border: none !important;
  border-bottom: 1px solid var(--surface-border) !important;
  color: var(--text-color);
  background: transparent !important;
}

.dns-table tr:last-child td {
  border-bottom: none !important;
}

.dns-table tr:nth-child(even) {
  background: transparent !important;
}

.dns-table code {
  font-size: 12px;
  background: none !important;
  padding: 0;
  color: inherit;
}

.dns-record-type {
  font-weight: 600;
  font-size: 12px;
  color: var(--text-color-secondary);
}

.dns-hint {
  font-size: 11px;
  color: var(--text-color-muted);
}

.dns-row-ok td {
  color: var(--text-color);
}

.dns-row-err td {
  background: rgba(239, 68, 68, 0.08) !important;
}

.dns-row-err .dns-check-icon {
  color: #ef4444;
}

.dns-row-ok .dns-check-icon {
  color: #22c55e;
}

.dns-check-icon {
  text-align: center;
  font-size: 16px;
  width: 32px;
  font-weight: 700;
}

.dns-wildcard-warn {
  margin: 12px 0 0;
  padding: 10px 14px;
  background: rgba(245, 158, 11, 0.10);
  border: 1px solid rgba(245, 158, 11, 0.3);
  border-radius: 6px;
  color: #b45309;
  font-size: 13px;
  line-height: 1.5;
}

@media (prefers-color-scheme: dark) {
  .dns-wildcard-warn {
    color: #fbbf24;
    background: rgba(245, 158, 11, 0.08);
    border-color: rgba(245, 158, 11, 0.25);
  }
}

@media (max-width: 768px) {
  .dns-input {
    min-width: 0;
    width: 100%;
  }
  .dns-table th:nth-child(2),
  .dns-table td:nth-child(2) {
    display: none;
  }
}
</style>

#!/usr/bin/env node
// Build a Pagefind search index from the docs markdown content.
//
// Reads docs/content/*.md (frontmatter + markdown body), renders to HTML,
// feeds each entry into Pagefind, and writes the resulting index to
// ui/dist/pagefind/. The dist files are then embedded into the Go binary
// via ui/embed.go and served by execore at /pagefind/*.

import { readFile, readdir } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, resolve } from 'node:path'
import * as pagefind from 'pagefind'
import { marked } from 'marked'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '..', '..')
const contentDir = join(repoRoot, 'docs', 'content')
const outDir = join(repoRoot, 'ui', 'dist', 'pagefind')

function parseFrontmatter(src) {
  // Returns { meta, body }. If no leading '---' block, meta is empty.
  if (!src.startsWith('---')) return { meta: {}, body: src }
  const end = src.indexOf('\n---', 3)
  if (end < 0) return { meta: {}, body: src }
  const fm = src.slice(3, end).trim()
  const body = src.slice(end + 4).replace(/^\r?\n/, '')
  const meta = {}
  for (const rawLine of fm.split(/\r?\n/)) {
    const line = rawLine.replace(/^\s+/, '')
    if (!line || line.startsWith('#')) continue
    const i = line.indexOf(':')
    if (i < 0) continue
    const key = line.slice(0, i).trim()
    let val = line.slice(i + 1).trim()
    if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
      val = val.slice(1, -1)
    }
    if (val === 'true') meta[key] = true
    else if (val === 'false') meta[key] = false
    else meta[key] = val
  }
  return { meta, body }
}

// walkMarkdown recursively yields .md file paths under root, relative to root
// (using forward slashes). Mirrors the Go side's fs.WalkDir traversal so
// docs in subdirectories like docs/content/teams/sso.md are indexed too.
async function walkMarkdown(root, prefix = '') {
  const out = []
  const entries = await readdir(root, { withFileTypes: true })
  for (const ent of entries) {
    if (ent.name.startsWith('.')) continue
    const rel = prefix ? `${prefix}/${ent.name}` : ent.name
    if (ent.isDirectory()) {
      out.push(...(await walkMarkdown(join(root, ent.name), rel)))
    } else if (ent.isFile() && ent.name.endsWith('.md')) {
      out.push(rel)
    }
  }
  return out
}

async function main() {
  if (!existsSync(contentDir)) {
    console.error(`docs content not found at ${contentDir}`)
    process.exit(1)
  }

  const { index } = await pagefind.createIndex({
    rootSelector: 'main',
    keepIndexUrl: false,
  })
  if (!index) {
    throw new Error('pagefind.createIndex returned no index')
  }

  const entries = await walkMarkdown(contentDir)
  let added = 0
  let skipped = 0

  for (const file of entries) {
    const slug = file.replace(/\.md$/, '')
    const raw = await readFile(join(contentDir, file), 'utf8')
    const { meta, body } = parseFrontmatter(raw)

    // Match docs/store.go: Published defaults to true unless explicitly
    // set to false. Only index docs that are visibly linked on the public
    // site, i.e. published == true and not unlinked. Preview/draft docs are
    // excluded from the search index even though they may be reachable
    // directly, since /pagefind/* assets are served without auth.
    const published = meta.published !== false && meta.published !== 'false'
    if (!published) {
      skipped++
      continue
    }
    if (meta.unlinked) {
      skipped++
      continue
    }

    const html = marked.parse(body, { async: false })
    const title = meta.title || slug
    const description = meta.description || ''
    const heading = meta.subheading || ''

    // Wrap so Pagefind has a clear root and a stable title.
    // Use the inline `key:value` form so we can set arbitrary metadata
    // (section, slug, description) without injecting visible text. The host
    // element itself is excluded from the indexed body, but pagefind still
    // reads its meta attribute.
    const metaSpans = [
      heading ? `<span hidden data-pagefind-meta="section:${escapeAttr(heading)}"></span>` : '',
      `<span hidden data-pagefind-meta="slug:${escapeAttr(slug)}"></span>`,
      description ? `<span hidden data-pagefind-meta="description:${escapeAttr(description)}"></span>` : '',
    ].filter(Boolean).join('')

    const fullHtml = [
      '<!doctype html><html lang="en"><head><meta charset="utf-8">',
      `<title>${escapeHtml(title)}</title>`,
      '</head><body><main data-pagefind-body>',
      metaSpans,
      `<h1>${escapeHtml(title)}</h1>`,
      description ? `<p>${escapeHtml(description)}</p>` : '',
      html,
      '</main></body></html>',
    ].join('')

    const res = await index.addHTMLFile({
      url: `/docs/${slug}`,
      content: fullHtml,
    })
    if (res.errors && res.errors.length) {
      console.error(`pagefind errors for ${file}:`, res.errors)
      process.exit(1)
    }
    added++
  }

  const writeRes = await index.writeFiles({ outputPath: outDir })
  if (writeRes.errors && writeRes.errors.length) {
    console.error('pagefind write errors:', writeRes.errors)
    process.exit(1)
  }

  await pagefind.close()
  console.log(`pagefind: indexed ${added} docs (${skipped} skipped) -> ${outDir}`)
}

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

// escapeAttr escapes a string for use as the value portion of
// data-pagefind-meta="key:value". The whole attribute already lives in
// double quotes, so we need to escape '"' as well as the literal ',' and
// ':' characters that pagefind uses as separators.
function escapeAttr(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/,/g, '&#44;')
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})

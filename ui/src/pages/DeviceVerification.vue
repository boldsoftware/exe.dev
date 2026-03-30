<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle text-lg mb-3">{{ page.email }}</p>
      <h1 class="heading mb-6">Authorize SSH key</h1>
      <div class="code-block">
        <p class="code-block-label">SSH KEY</p>
        <pre><code>{{ page.publicKey }}</code></pre>
      </div>
      <form method="POST" :action="page.formAction">
        <input type="hidden" name="token" :value="page.token">
        <button type="submit" class="btn-primary">Authorize</button>
      </form>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  formAction: string
  email: string
  publicKey: string
  token: string
}

const page = pageData<PageData>()
</script>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 48px;
}

.page-content {
  width: 100%;
  max-width: 42rem;
}

.heading {
  font-size: 1.875rem;
  font-weight: 600;
  line-height: 1.2;
}

.subtitle {
  color: var(--text-color-secondary);
}

.text-lg {
  font-size: 1.125rem;
}

.code-block {
  background: var(--surface-subtle);
  border-radius: 4px;
  margin-bottom: 1.5rem;
}

.code-block-label {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 12px 16px 0;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.code-block pre {
  padding: 8px 16px 16px;
  font-size: 14px;
  white-space: pre-wrap;
  word-break: break-all;
  border: none;
  background: none;
  margin: 0;
}

.code-block code {
  background: none;
  padding: 0;
}

.btn-primary {
  display: inline-block;
  padding: 12px 24px;
  font-size: 14px;
  font-family: inherit;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  text-decoration: none;
  background: var(--text-color);
  color: var(--surface-ground);
}
.btn-primary:hover { opacity: 0.85; }

.mb-3 { margin-bottom: 0.75rem; }
.mb-6 { margin-bottom: 1.5rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

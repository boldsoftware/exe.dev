<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading error-heading mb-6">Error</h1>
      <p class="message mb-6">{{ page.message }}</p>
      <p v-if="page.command" class="subtitle mb-6">Command: <code>{{ page.command }}</code></p>
      <a :href="'/auth' + (page.queryString ? '?' + page.queryString : '')" class="link-subtle">Try again</a>
      <p v-if="page.traceId" class="trace mt-8">trace: {{ page.traceId }}</p>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  message: string
  command: string
  queryString: string
  traceId: string
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
  font-size: 2.25rem;
  font-weight: 600;
  line-height: 1.2;
}

.error-heading {
  color: var(--danger-color);
}

.message {
  font-size: 1.25rem;
}

.subtitle {
  color: var(--text-color-secondary);
}

.subtitle code {
  background: var(--surface-subtle);
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 14px;
}

.link-subtle {
  color: var(--text-color-secondary);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.link-subtle:hover {
  color: var(--text-color);
}

.trace {
  color: var(--text-color-muted);
  font-size: 14px;
}

.mb-6 { margin-bottom: 1.5rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

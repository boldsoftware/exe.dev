<template>
  <div class="page">
    <main class="page-content">
      <p class="heading">READY</p>
      <p class="subtitle mt-4">Your payment info has been saved.</p>
      <div v-if="page.source === 'exemenu'">
        <p class="subtitle mt-8">Return to your terminal to continue.</p>
      </div>
      <div v-else class="mt-8">
        <a href="/new" class="btn-primary">Create a New VM</a>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  webHost: string
  source: string
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
  font-size: 3.75rem;
  font-weight: 600;
  letter-spacing: 0.1em;
  line-height: 1;
}

.subtitle {
  color: var(--text-color-secondary);
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
.btn-primary:hover { opacity: 0.85; text-decoration: none; color: var(--surface-ground); }

.mt-4 { margin-top: 1rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
  .heading { font-size: 2.25rem; }
}
</style>

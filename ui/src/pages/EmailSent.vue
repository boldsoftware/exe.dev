<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading mb-6">Check your email</h1>
      <p class="subtitle"></p>
      <div v-if="page.devUrl" class="mt-8">
        <p class="muted small">(dev mode)</p>
        <a :href="page.devUrl" class="dev-link">{{ page.devUrl }}</a>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  devUrl: string
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

.subtitle {
  color: var(--text-color-secondary);
}

.muted {
  color: var(--text-color-muted);
}

.small {
  font-size: 14px;
}

.dev-link {
  word-break: break-all;
}

.mb-6 { margin-bottom: 1.5rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

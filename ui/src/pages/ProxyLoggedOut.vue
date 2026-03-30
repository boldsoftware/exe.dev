<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading mb-6">Logged out</h1>
      <p class="subtitle mb-8">You have been logged out successfully.</p>
      <a href="/">Return to {{ page.webHost }}</a>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  webHost: string
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

.mb-6 { margin-bottom: 1.5rem; }
.mb-8 { margin-bottom: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

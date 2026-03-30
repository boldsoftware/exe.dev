<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle mb-4">{{ page.discordUsername }}</p>
      <p class="heading">LINKED</p>
      <p class="subtitle mt-8">Your Discord account has been linked to your exe.dev account.</p>
      <div class="mt-8">
        <a href="/" class="link-subtle">Continue to Dashboard</a>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  discordUsername: string
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

.link-subtle {
  color: var(--text-color-secondary);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.link-subtle:hover {
  color: var(--text-color);
}

.mb-4 { margin-bottom: 1rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
  .heading { font-size: 2.25rem; }
}
</style>

<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle text-lg mb-3">{{ page.userEmail }}</p>
      <h1 class="heading mb-8">CONFIRM LOGIN</h1>
      <div class="code-block mb-6">
        <p class="code-block-label">SITE</p>
        <p class="code-block-value">{{ page.siteDomain }}</p>
      </div>
      <p class="subtitle mb-8">The site will be able to see your email address.</p>
      <div class="actions">
        <a :href="page.cancelUrl" class="btn-outline">Cancel</a>
        <a :href="page.confirmUrl" class="btn-primary">Continue</a>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  webHost: string
  userEmail: string
  siteDomain: string
  cancelUrl: string
  confirmUrl: string
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

.text-lg {
  font-size: 1.125rem;
}

.code-block {
  display: inline-block;
  background: var(--surface-subtle);
  border-radius: 4px;
}

.code-block-label {
  font-size: 12px;
  color: var(--text-color-muted);
  padding: 12px 16px 0;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.code-block-value {
  padding: 8px 16px 16px;
  font-size: 1.25rem;
  font-weight: 600;
}

.actions {
  display: flex;
  gap: 1rem;
}

.btn-primary {
  display: inline-block;
  padding: 12px 24px;
  font-size: 14px;
  font-weight: 600;
  font-family: inherit;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  text-decoration: none;
  background: var(--text-color);
  color: var(--surface-ground);
}
.btn-primary:hover { opacity: 0.85; text-decoration: none; color: var(--surface-ground); }

.btn-outline {
  display: inline-block;
  padding: 12px 24px;
  font-size: 14px;
  font-weight: 600;
  font-family: inherit;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  cursor: pointer;
  text-decoration: none;
  background: transparent;
  color: var(--text-color-secondary);
}
.btn-outline:hover { background: var(--surface-subtle); text-decoration: none; }

.mb-3 { margin-bottom: 0.75rem; }
.mb-6 { margin-bottom: 1.5rem; }
.mb-8 { margin-bottom: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

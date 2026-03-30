<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading mb-6">Check your email</h1>
      <p class="subtitle mb-8">We sent a code to <strong class="email">{{ page.email }}</strong></p>
      <p v-if="page.error" class="error-text mb-4">{{ page.error }}</p>
      <form method="POST" :action="page.formAction">
        <input type="hidden" name="email" :value="page.email">
        <input
          type="text"
          name="code"
          maxlength="8"
          placeholder="a1b2c3d4"
          required
          autofocus
          autocomplete="one-time-code"
          spellcheck="false"
          autocapitalize="none"
          class="code-input"
        >
        <br>
        <button type="submit" class="btn-primary mt-6">Verify</button>
      </form>
      <p v-if="page.devCode" class="dev-code mt-8">(dev mode) code: {{ page.devCode }}</p>
    </main>
  </div>
</template>

<script setup lang="ts">
import { pageData } from './simple'

interface PageData {
  formAction: string
  email: string
  devCode: string
  error: string
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

.email {
  color: var(--text-color);
}

.error-text {
  color: var(--danger-color);
}

.code-input {
  width: 14rem;
  font-size: 1.875rem;
  letter-spacing: 0.2em;
  text-align: center;
  padding: 12px 0;
  border: none;
  border-bottom: 2px solid var(--surface-border);
  border-radius: 0;
  font-family: inherit;
  background: transparent;
  color: var(--text-color);
  outline: none;
}

.code-input:focus {
  border-bottom-color: var(--text-color);
}

.code-input::placeholder {
  color: var(--text-color-muted);
}

.btn-primary {
  display: inline-block;
  padding: 12px 24px;
  font-size: 1.125rem;
  font-family: inherit;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  text-decoration: none;
  background: var(--text-color);
  color: var(--surface-ground);
}
.btn-primary:hover { opacity: 0.85; }

.dev-code {
  color: var(--text-color-muted);
  font-size: 14px;
}

.mb-4 { margin-bottom: 1rem; }
.mb-6 { margin-bottom: 1.5rem; }
.mb-8 { margin-bottom: 2rem; }
.mt-6 { margin-top: 1.5rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

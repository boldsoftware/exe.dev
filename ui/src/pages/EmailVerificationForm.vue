<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle text-lg mb-3">{{ page.email }}</p>
      <h1 class="heading mb-8">CONFIRM LOGIN</h1>
      <form method="POST" :action="page.formAction">
        <input v-if="page.redirect" type="hidden" name="redirect" :value="page.redirect">
        <input v-if="page.return_host" type="hidden" name="return_host" :value="page.return_host">
        <input v-if="page.source" type="hidden" name="source" :value="page.source">
        <input type="hidden" name="token" :value="page.token">
        <button type="submit" class="btn-primary">Confirm</button>
      </form>
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { pageData } from './simple'

interface PageData {
  formAction: string
  token: string
  redirect: string
  return_host: string
  email: string
  source: string
}

const page = pageData<PageData>()

onMounted(() => {
  // Auto-submit immediately via a dynamically created form
  const f = document.createElement('form')
  f.method = 'POST'
  f.action = page.formAction
  f.style.display = 'none'

  const fields: Record<string, string> = { token: page.token }
  if (page.redirect) fields.redirect = page.redirect
  if (page.return_host) fields.return_host = page.return_host
  if (page.source) fields.source = page.source

  for (const [k, v] of Object.entries(fields)) {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = k
    input.value = v
    f.appendChild(input)
  }

  document.documentElement.appendChild(f)
  f.submit()
})
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
.mb-8 { margin-bottom: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

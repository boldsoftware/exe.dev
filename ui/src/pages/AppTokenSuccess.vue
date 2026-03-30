<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle mb-4">{{ page.email }}</p>
      <p class="heading">{{ page.isWelcome ? 'WELCOME' : 'SIGNED IN' }}</p>
      <p ref="statusEl" class="status mt-8"></p>
      <p ref="skipEl" class="mt-4" style="display: none;">
        <a href="#" class="link-subtle small" @click.prevent="finish">Continue to app</a>
      </p>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { pageData } from './simple'

declare const passkey: any

interface PageData {
  email: string
  callbackUrl: string
  isWelcome: boolean
  hasPasskeys: boolean
}

const page = pageData<PageData>()

const statusEl = ref<HTMLElement | null>(null)
const skipEl = ref<HTMLElement | null>(null)
let done = false

function finish() {
  if (done) return
  done = true
  if (statusEl.value) {
    statusEl.value.textContent = 'Returning to app...'
  }
  if (skipEl.value) {
    skipEl.value.style.display = 'none'
  }
  window.location.href = page.callbackUrl
}

onMounted(async () => {
  // Safety net: show skip link after 15s, force redirect after 20s
  setTimeout(() => {
    if (!done && skipEl.value) {
      skipEl.value.style.display = 'block'
    }
  }, 15000)
  setTimeout(() => {
    if (!done) finish()
  }, 20000)

  if (page.hasPasskeys || typeof passkey === 'undefined' || !passkey.isSupported()) {
    finish()
    return
  }

  try {
    const name = passkey.getDefaultName()
    if (statusEl.value) {
      statusEl.value.textContent = 'Setting up passkey...'
    }
    await passkey.register(name)
    if (statusEl.value) {
      statusEl.value.textContent = 'Passkey added!'
      statusEl.value.style.color = 'var(--success-color)'
    }
    setTimeout(finish, 800)
  } catch {
    finish()
  }
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
  font-size: 3.75rem;
  font-weight: 600;
  letter-spacing: 0.1em;
  line-height: 1;
}

.subtitle {
  color: var(--text-color-secondary);
}

.status {
  color: var(--text-color-secondary);
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

.small {
  font-size: 14px;
}

.mb-4 { margin-bottom: 1rem; }
.mt-4 { margin-top: 1rem; }
.mt-8 { margin-top: 2rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
  .heading { font-size: 2.25rem; }
}
</style>

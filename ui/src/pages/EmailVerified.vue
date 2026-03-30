<template>
  <div class="page">
    <main class="page-content">
      <p class="subtitle mb-4">{{ page.email }}</p>
      <p class="heading">{{ page.isWelcome ? 'WELCOME' : 'VERIFIED' }}</p>

      <div v-if="page.needsBilling" class="mt-8">
        <p class="subtitle mb-4">One last step: add a payment method to start launching VMs.</p>
        <a :href="'/billing/checkout/start?token=' + page.billingToken" class="btn-primary">Setup Future Payment Details</a>
      </div>
      <div v-else-if="page.source === 'exemenu'">
        <p class="subtitle mt-8">Return to your terminal to continue.</p>
      </div>
      <div v-else class="mt-8">
        <a href="/" class="btn-blue">Continue to Dashboard</a>
      </div>

      <div v-if="page.isWelcome" class="mt-8">
        <label class="checkbox-label">
          <input type="checkbox" v-model="newsletterChecked" @change="subscribeNewsletter" :disabled="newsletterSubmitting">
          Subscribe to the newsletter
        </label>
        <p v-if="newsletterDone" class="success-text mt-1">Subscribed!</p>
      </div>

      <div v-if="!page.hasPasskeys && !page.needsBilling && passkeyVisible" class="passkey-section mt-12">
        <p class="subtitle small mb-4">Optional: Add a passkey for passwordless sign-in from this browser. You can also do this later on your user profile page.</p>
        <div class="passkey-form">
          <label for="passkey-name" class="subtitle small">Passkey name</label>
          <div class="passkey-row">
            <input type="text" id="passkey-name" v-model="passkeyName" :placeholder="passkeyDefaultName" class="passkey-input">
            <button @click="addPasskey" :disabled="passkeyAdded" class="btn-outline">{{ passkeyAdded ? 'Added' : 'Add Passkey' }}</button>
          </div>
        </div>
        <p v-if="passkeyError" :class="passkeyErrorSoft ? 'subtitle small mt-2' : 'error-text small mt-2'">{{ passkeyError }}</p>
        <p v-if="passkeyAdded" class="success-text small mt-2">Passkey added!</p>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { pageData } from './simple'

interface PageData {
  email: string
  isWelcome: boolean
  source: string
  hasPasskeys: boolean
  needsBilling: boolean
  billingToken: string
}

const page = pageData<PageData>()

// Newsletter
const newsletterChecked = ref(false)
const newsletterSubmitting = ref(false)
const newsletterDone = ref(false)

async function subscribeNewsletter() {
  if (!newsletterChecked.value) return
  newsletterSubmitting.value = true
  try {
    const resp = await fetch('/newsletter-subscribe', { method: 'POST' })
    if (resp.ok) {
      newsletterDone.value = true
    } else {
      newsletterChecked.value = false
    }
  } catch {
    newsletterChecked.value = false
  }
  newsletterSubmitting.value = false
}

// Passkeys
const passkeyVisible = ref(false)
const passkeyName = ref('')
const passkeyDefaultName = ref('e.g. MacBook Pro, Work Laptop')
const passkeyAdded = ref(false)
const passkeyError = ref('')
const passkeyErrorSoft = ref(false)

declare const passkey: any

onMounted(() => {
  if (typeof passkey !== 'undefined' && passkey.isSupported()) {
    passkeyVisible.value = true
    passkeyDefaultName.value = passkey.getDefaultName()
    if (page.isWelcome) {
      addPasskey()
    }
  }
})

async function addPasskey() {
  const name = passkeyName.value.trim() || passkeyDefaultName.value
  passkeyError.value = ''
  passkeyErrorSoft.value = false

  try {
    await passkey.register(name)
    passkeyAdded.value = true
  } catch (err: any) {
    if (err.cancelled) {
      passkeyError.value = err.message
      passkeyErrorSoft.value = true
      return
    }
    passkeyError.value = err.message
  }
}
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

.small {
  font-size: 14px;
}

.mt-1 { margin-top: 0.25rem; }
.mt-2 { margin-top: 0.5rem; }
.mt-4 { margin-top: 1rem; }
.mt-8 { margin-top: 2rem; }
.mt-12 { margin-top: 3rem; }
.mb-4 { margin-bottom: 1rem; }

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

.btn-blue {
  display: inline-block;
  padding: 12px 24px;
  font-size: 14px;
  font-family: inherit;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  text-decoration: none;
  background: #2563eb;
  color: #fff;
}
.btn-blue:hover { background: #1d4ed8; text-decoration: none; color: #fff; }

@media (prefers-color-scheme: dark) {
  .btn-blue { background: #3b82f6; }
  .btn-blue:hover { background: #60a5fa; }
}

.btn-outline {
  display: inline-block;
  padding: 8px 16px;
  font-size: 14px;
  font-family: inherit;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  cursor: pointer;
  background: transparent;
  color: var(--text-color-secondary);
}
.btn-outline:hover { background: var(--surface-subtle); }
.btn-outline:disabled { opacity: 0.6; cursor: default; }

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 14px;
  color: var(--text-color-secondary);
  cursor: pointer;
  user-select: none;
}
.checkbox-label input[type="checkbox"] {
  width: 16px;
  height: 16px;
  accent-color: var(--text-color);
}

.passkey-section {
  border-top: 1px solid var(--surface-border);
  padding-top: 2rem;
}

.passkey-form {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.passkey-row {
  display: flex;
  gap: 0.75rem;
  align-items: center;
}

.passkey-input {
  padding: 8px 12px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 14px;
  background: transparent;
  color: var(--text-color);
  outline: none;
  width: 16rem;
}
.passkey-input:focus { border-color: var(--text-color-secondary); }

.success-text { color: var(--success-color); }
.error-text { color: var(--danger-color); }

@media (max-width: 640px) {
  .page { padding: 24px; }
  .heading { font-size: 2.25rem; }
}
</style>

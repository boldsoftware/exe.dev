<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading mb-6">Verifying...</h1>
      <form ref="formEl" method="POST" :action="page.formAction" style="display: none;">
        <input type="hidden" name="email" :value="page.email">
        <input type="hidden" name="pow_token" :value="page.powToken">
        <input type="hidden" name="pow_nonce" ref="nonceInput" value="">
        <input type="hidden" name="pow_time_ms" ref="timeInput" value="">
        <input v-if="page.redirect" type="hidden" name="redirect" :value="page.redirect">
        <input v-if="page.return_host" type="hidden" name="return_host" :value="page.return_host">
        <input v-if="page.login_with_exe" type="hidden" name="login_with_exe" value="1">
        <input v-if="page.invite" type="hidden" name="invite" :value="page.invite">
        <input v-if="page.hostname" type="hidden" name="hostname" :value="page.hostname">
        <input v-if="page.prompt" type="hidden" name="prompt" :value="page.prompt">
        <input v-if="page.image" type="hidden" name="image" :value="page.image">
        <input v-if="page.team_invite" type="hidden" name="team_invite" :value="page.team_invite">
        <input v-if="page.response_mode" type="hidden" name="response_mode" :value="page.response_mode">
        <input v-if="page.callback_uri" type="hidden" name="callback_uri" :value="page.callback_uri">
      </form>
      <p ref="statusEl" class="status">Please wait...</p>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { pageData } from './simple'

interface PageData {
  formAction: string
  email: string
  powToken: string
  powDifficulty: number
  redirect: string
  return_host: string
  login_with_exe: boolean
  invite: string
  hostname: string
  prompt: string
  image: string
  team_invite: string
  response_mode: string
  callback_uri: string
}

const page = pageData<PageData>()

const formEl = ref<HTMLFormElement | null>(null)
const nonceInput = ref<HTMLInputElement | null>(null)
const timeInput = ref<HTMLInputElement | null>(null)
const statusEl = ref<HTMLElement | null>(null)

function hasLeadingZeros(hash: Uint8Array, n: number): boolean {
  const fullBytes = Math.floor(n / 8)
  const remainingBits = n % 8

  for (let i = 0; i < fullBytes; i++) {
    if (hash[i] !== 0) return false
  }

  if (remainingBits > 0 && fullBytes < hash.length) {
    const mask = 0xFF << (8 - remainingBits)
    if ((hash[fullBytes] & mask) !== 0) return false
  }

  return true
}

async function solvePOW(): Promise<number> {
  const encoder = new TextEncoder()
  const tokenBytes = encoder.encode(page.powToken)

  for (let nonce = 0; ; nonce++) {
    const nonceBytes = new ArrayBuffer(8)
    const view = new DataView(nonceBytes)
    view.setBigUint64(0, BigInt(nonce), true)

    const combined = new Uint8Array(tokenBytes.length + 8)
    combined.set(tokenBytes)
    combined.set(new Uint8Array(nonceBytes), tokenBytes.length)

    const hashBuffer = await crypto.subtle.digest('SHA-256', combined)
    const hashArray = new Uint8Array(hashBuffer)

    if (hasLeadingZeros(hashArray, page.powDifficulty)) {
      return nonce
    }

    if (nonce % 10000 === 0) {
      await new Promise(r => setTimeout(r, 0))
    }
  }
}

onMounted(async () => {
  try {
    const startTime = performance.now()
    const nonce = await solvePOW()
    const elapsed = Math.round(performance.now() - startTime)

    if (nonceInput.value) nonceInput.value.value = nonce.toString()
    if (timeInput.value) timeInput.value.value = elapsed.toString()
    if (formEl.value) formEl.value.submit()
  } catch {
    if (statusEl.value) {
      statusEl.value.textContent = 'Error, please go back and try again'
    }
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
  font-size: 2.25rem;
  font-weight: 600;
  line-height: 1.2;
}

.status {
  color: var(--text-color-muted);
  font-size: 14px;
}

.mb-6 { margin-bottom: 1.5rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

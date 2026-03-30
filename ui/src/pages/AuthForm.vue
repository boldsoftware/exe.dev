<template>
  <div class="page">
    <main class="page-content">
      <h1 class="heading mb-6">Login (or create an account)</h1>

      <p v-if="page.team_invite" class="success-text mb-4">✓ You've been invited to join {{ page.teamInviteName }}</p>
      <template v-else-if="page.inviteValid">
        <p v-if="page.invitePlanType === 'free'" class="success-text mb-4">✓ Invite code accepted: free account</p>
        <p v-else-if="page.invitePlanType === 'trial'" class="success-text mb-4">✓ Invite code accepted: 1 month free trial</p>
        <p v-else class="success-text mb-4">✓ Invite code accepted</p>
      </template>
      <p v-else-if="page.inviteInvalid" class="error-text mb-4">Invalid or already used invite code.</p>

      <form method="POST" :action="page.formAction">
        <input v-if="page.redirect" type="hidden" name="redirect" :value="page.redirect">
        <input v-if="page.return_host" type="hidden" name="return_host" :value="page.return_host">
        <input v-if="page.login_with_exe" type="hidden" name="login_with_exe" value="1">
        <input v-if="page.hostname" type="hidden" name="hostname" :value="page.hostname">
        <input v-if="page.prompt" type="hidden" name="prompt" :value="page.prompt">
        <input v-if="page.image" type="hidden" name="image" :value="page.image">
        <input v-if="page.inviteValid" type="hidden" name="invite" :value="page.invite">
        <input v-if="page.team_invite" type="hidden" name="team_invite" :value="page.team_invite">
        <input v-if="page.response_mode" type="hidden" name="response_mode" :value="page.response_mode">
        <input v-if="page.callback_uri" type="hidden" name="callback_uri" :value="page.callback_uri">
        <input
          type="email"
          name="email"
          placeholder="you@example.com"
          required
          autofocus
          :value="page.teamInviteEmail || ''"
          autocomplete="username webauthn"
          autocorrect="on"
          autocapitalize="none"
          spellcheck="true"
          class="email-input"
        >
        <button type="submit" class="btn-primary mt-6">Continue</button>
      </form>

      <p ref="passkeyErrorEl" class="error-text small mt-4" style="display: none;"></p>

      <div ref="passkeySectionEl" class="mt-6" style="display: none;">
        <button ref="passkeyBtnEl" class="link-subtle small" @click="loginWithPasskey">
          Sign in with passkey
        </button>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { pageData } from './simple'

declare const passkey: any

interface PageData {
  formAction: string
  webHost: string
  redirect: string
  return_host: string
  login_with_exe: boolean
  sshCommand: string
  hostname: string
  prompt: string
  image: string
  invite: string
  inviteValid: boolean
  inviteInvalid: boolean
  invitePlanType: string
  team_invite: string
  teamInviteName: string
  teamInviteEmail: string
  response_mode: string
  callback_uri: string
}

const page = pageData<PageData>()

const passkeyErrorEl = ref<HTMLElement | null>(null)
const passkeySectionEl = ref<HTMLElement | null>(null)
const passkeyBtnEl = ref<HTMLButtonElement | null>(null)

const passkeyExtra: Record<string, string> = {}
if (page.response_mode) passkeyExtra.response_mode = page.response_mode
if (page.callback_uri) passkeyExtra.callback_uri = page.callback_uri

onMounted(async () => {
  if (typeof passkey === 'undefined' || !passkey.isSupported()) return

  if (await passkey.isConditionalUISupported()) {
    // Browser has native passkey autofill UI
    passkey.startConditionalAuth(null, passkeyExtra).catch((err: any) => {
      if (err.name !== 'NotAllowedError') {
        if (err.message && err.message.includes('Resident credentials')) {
          console.log('passkey conditional auth not fully supported:', err.message)
          return
        }
        console.error('passkey conditional auth failed:', err)
        if (passkeyErrorEl.value) {
          passkeyErrorEl.value.textContent = err.message
          passkeyErrorEl.value.style.display = 'block'
        }
      }
    })
  } else {
    // No conditional UI, show manual passkey button
    if (passkeySectionEl.value) {
      passkeySectionEl.value.style.display = 'block'
    }
  }
})

async function loginWithPasskey() {
  if (!passkeyErrorEl.value || !passkeyBtnEl.value) return

  passkeyErrorEl.value.style.display = 'none'
  passkeyBtnEl.value.disabled = true
  passkeyBtnEl.value.textContent = 'Authenticating...'

  try {
    await passkey.authenticate(null, null, passkeyExtra)
  } catch (err: any) {
    passkeyErrorEl.value.textContent = err.message
    passkeyErrorEl.value.style.display = 'block'
    passkeyBtnEl.value.disabled = false
    passkeyBtnEl.value.textContent = 'Sign in with Passkey'
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
  font-size: 2.25rem;
  font-weight: 600;
  line-height: 1.2;
}

.email-input {
  padding: 12px 0;
  border: none;
  border-bottom: 2px solid var(--surface-border);
  border-radius: 0;
  font-family: inherit;
  font-size: 1.25rem;
  background: transparent;
  color: var(--text-color);
  outline: none;
  width: 100%;
}

.email-input:focus {
  border-bottom-color: var(--text-color);
}

.email-input::placeholder {
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

.success-text { color: var(--success-color); }
.error-text { color: var(--danger-color); }

.link-subtle {
  color: var(--text-color-secondary);
  text-decoration: underline;
  text-underline-offset: 2px;
  background: none;
  border: none;
  font-family: inherit;
  cursor: pointer;
  padding: 0;
}
.link-subtle:hover { color: var(--text-color); }
.link-subtle:disabled { opacity: 0.6; cursor: default; }

.small { font-size: 14px; }

.mb-4 { margin-bottom: 1rem; }
.mb-6 { margin-bottom: 1.5rem; }
.mt-4 { margin-top: 1rem; }
.mt-6 { margin-top: 1.5rem; }

@media (max-width: 640px) {
  .page { padding: 24px; }
}
</style>

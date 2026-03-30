<template>
  <div class="profile-page">
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>

    <div v-else-if="loadError" class="error-state">
      <p>Failed to load profile: {{ loadError }}</p>
      <button class="btn btn-secondary" @click="loadProfile">Retry</button>
    </div>

    <template v-else-if="data">
      <!-- What is exe? section for basic users -->
      <section v-if="data.basicUser" class="card">
        <h2 class="card-title">What is exe?</h2>
        <p class="section-help">
          exe.dev is a hosting service. You've logged into a site
          hosted by exe.dev that uses "Login with exe" to manage
          authentication. See our <a href="/docs">docs</a> for
          more information.
        </p>
      </section>

      <!-- User Info -->
      <section class="card">
        <h2 class="card-title">Account</h2>
        <div class="info-grid">
          <div class="info-row">
            <span class="info-label">Email</span>
            <span class="info-value">{{ data.user.email }}</span>
          </div>
          <div class="info-row">
            <span class="info-label">Region</span>
            <span class="info-value">
              {{ data.user.region.toUpperCase() }}<template v-if="data.user.regionDisplay"> ({{ data.user.regionDisplay }})</template>
              &ensp;<button class="btn btn-secondary" @click="showRegionModal = true">Change</button>
            </span>
          </div>
          <div class="info-row">
            <span class="info-label">Newsletter</span>
            <label class="newsletter-label">
              <input type="checkbox" :checked="data.user.newsletterSubscribed" @change="toggleNewsletter" />
              <span>Subscribe to updates</span>
              <span v-if="newsletterStatus" class="newsletter-status">{{ newsletterStatus }}</span>
            </label>
          </div>
          <div v-if="data.inviteCount > 0 || data.canRequestInvites" class="info-row">
            <span class="info-label">Invites</span>
            <span class="info-value">
              {{ data.inviteCount }} invite{{ data.inviteCount !== 1 ? 's' : '' }} available.
              <form v-if="data.inviteCount > 0" method="POST" action="/invite" style="display: inline;">
                <button type="submit" class="btn btn-secondary">Allocate</button>
              </form>
              <a v-else-if="data.canRequestInvites" href="/invite/request" class="btn btn-secondary">Request more</a>
            </span>
          </div>
        </div>
      </section>

      <!-- Billing (Unified) -->
      <section class="card">
        <h2 class="card-title">Billing & Credits</h2>
        <div class="billing-card-inner">
          <!-- Skip Billing Notice -->
          <template v-if="data.credits.skipBilling">
            <div class="info-row">
              <span class="info-label">Status</span>
              <span class="info-value">Not configured (no <code>STRIPE_SECRET_KEY</code> env var exported)</span>
            </div>
          </template>

          <!-- Full Billing UI -->
          <template v-else>
            <!-- Header: Plan Name, Status Badge, Action Buttons -->
            <div class="billing-header">
              <div class="billing-header-left">
                <h3 class="plan-name">{{ data.credits.planName || 'Individual' }}</h3>
                <Tag v-if="data.credits.selfServeBilling" value="ACTIVE" class="active-tag" />
              </div>
              <div class="billing-header-right">
                <a v-if="data.credits.selfServeBilling" href="/billing/update?source=profile" class="btn btn-secondary">Invoices</a>
                <a href="/billing/update?source=profile" class="btn btn-secondary">Manage Plan</a>
              </div>
            </div>

            <!-- Shelley Credits Section -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="credits-grid">
              <!-- Monthly Allowance Card -->
              <div class="credit-card">
                <div class="credit-card-title">MONTHLY ALLOWANCE</div>
                <div class="credit-card-amount">${{ Math.round(data.credits.shelleyCreditsMax - data.credits.monthlyUsedUSD) }}</div>
                <div class="credit-card-subtitle">remaining</div>
                <div class="credit-card-detail">of ${{ Math.round(data.credits.shelleyCreditsMax) }} · </div>
                <div class="credit-card-detail">Resets {{ data.credits.monthlyCreditsResetAt }}</div>
              </div>

              <!-- Extra Credits Card -->
              <div class="credit-card">
                <div class="credit-card-title">EXTRA CREDITS</div>
                <div class="credit-card-amount">${{ Math.round(data.credits.ledgerBalanceUSD) }}</div>
                <div class="credit-card-subtitle">balance</div>
                <div class="credit-card-detail">Purchased · no expiry</div>
              </div>
            </div>

            <!-- Monthly Usage Progress -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="usage-section">
              <div class="usage-header">
                <span class="usage-label">Monthly Usage</span>
                <span class="usage-pct">{{ Math.round(data.credits.monthlyUsedPct) }}% used</span>
              </div>
              <ProgressBar 
                :value="Math.min(data.credits.monthlyUsedPct, 100)" 
                :severity="usageBarSeverity"
                :show-value="false"
              />
              <div class="usage-footer">
                <span>${{ Math.round(data.credits.monthlyUsedUSD) }} used this month</span>
                <span v-if="data.credits.ledgerBalanceUSD > 0">${{ Math.round(data.credits.shelleyCreditsMax - data.credits.monthlyUsedUSD) }} + ${{ Math.round(data.credits.ledgerBalanceUSD) }} extra available</span>
              </div>
            </div>

            <!-- Buy Credits -->
            <div v-if="data.credits.hasShelleyFreeCreditPct" class="buy-section">
              <div class="buy-label">
                Top up extra credits · <a href="/docs/pricing#shelley-tokens" class="learn-more-link">How credits work</a>
              </div>
              <form method="POST" action="/credits/buy" class="buy-form">
                <div class="buy-amounts">
                  <button type="button" @click="selectedAmount = 5" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 5 }]">$5</button>
                  <button type="button" @click="selectedAmount = 10" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 10 }]">$10</button>
                  <button type="button" @click="selectedAmount = 25" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 25 }]">$25</button>
                  <button type="button" @click="selectedAmount = 50" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 50 }]">$50</button>
                  <button type="button" @click="selectedAmount = 100" :class="['amount-btn', { 'amount-btn-selected': selectedAmount === 100 }]">$100</button>
                </div>
                <input type="hidden" name="dollars" :value="selectedAmount" />
                <button type="submit" class="buy-btn">Buy ${{ selectedAmount }}</button>
              </form>
            </div>

            <!-- Transaction History -->
            <div v-if="transactionHistory.length > 0" class="transaction-section">
              <div class="transaction-header">
                <h4 class="subsection-title">Transaction History</h4>
                <a href="#" class="view-all-link">View all →</a>
              </div>
              <DataTable :value="transactionHistory" size="small" class="transaction-table">
                <Column field="type" header="DESCRIPTION" />
                <Column field="amount" header="AMOUNT" />
                <Column field="date" header="DATE">
                  <template #body="slotProps">
                    {{ slotProps.data.date || '—' }}
                  </template>
                </Column>
                <Column field="details" header="">
                  <template #body="slotProps">
                    <a 
                      v-if="slotProps.data.type === 'Purchase' && slotProps.data.details" 
                      :href="slotProps.data.details" 
                      target="_blank" 
                      rel="noopener noreferrer"
                      class="receipt-link"
                    >
                      Receipt ↗
                    </a>
                  </template>
                </Column>
              </DataTable>
            </div>

            <!-- Support -->
            <div class="support-section">
              <span class="support-label">Support</span>
              <a href="mailto:support@exe.dev">support@exe.dev</a>
            </div>
          </template>
        </div>
      </section>

      <!-- Pending Team Invites -->
      <section v-if="data.pendingTeamInvites.length > 0" class="card">
        <h2 class="card-title">Team Invitations</h2>
        <div v-for="invite in data.pendingTeamInvites" :key="invite.token" class="invite-row">
          <div>
            <strong>{{ invite.teamName }}</strong> invited you to join their team
            <span class="text-muted">by {{ invite.invitedBy }}</span>
            <div v-if="invite.vmCount > 0" class="invite-warning">
              · Accepting will make your {{ invite.vmCount }} existing VM{{ invite.vmCount !== 1 ? 's' : '' }} visible to team admins
            </div>
          </div>
          <div class="invite-actions">
            <button class="btn btn-primary" @click="acceptInvite(invite.token)">Accept</button>
            <button class="btn btn-secondary" @click="declineInvite(invite.token)">Decline</button>
          </div>
        </div>
      </section>

      <!-- Create Team (when user has no team and can create one) -->
      <section v-if="!data.teamInfo && data.canEnableTeam" class="card">
        <h2 class="card-title">Teams</h2>
        <p class="section-desc">Teams lets you manage shared billing, invite members, SSH into team members' VMs, and share VMs across your organization.</p>
        <p class="section-desc text-muted">You'll become the billing owner. Your existing VMs will become part of the team.</p>
        <div class="create-team-row">
          <input
            v-model="teamName"
            type="text"
            class="form-input"
            placeholder="Team name"
            @keydown.enter="createTeam"
          />
          <button class="btn btn-primary" :disabled="creatingTeam" @click="createTeam">
            {{ creatingTeam ? 'Creating...' : 'Create Team' }}
          </button>
        </div>
        <div v-if="teamError" class="field-error">{{ teamError }}</div>
      </section>

      <!-- Team Info -->
      <section v-if="data.teamInfo" class="card">
        <h2 class="card-title">{{ data.teamInfo.displayName }}</h2>
        <div class="info-grid">
          <div class="info-row">
            <span class="info-label">Role:</span>
            <span class="info-value">{{ data.teamInfo.role }}</span>
          </div>
          <div class="info-row">
            <span class="info-label">VMs:</span>
            <span class="info-value">{{ data.teamInfo.boxCount }} / {{ data.teamInfo.maxBoxes }}</span>
          </div>
        </div>
        <div v-if="data.teamInfo.members.length > 0" class="member-list">
          <h3 class="subsection-title">Members</h3>
          <div v-for="m in data.teamInfo.members" :key="m.email" class="member-row">
            <span>{{ m.email }}</span>
            <span class="text-muted">{{ m.role }}</span>
            <button
              v-if="data.teamInfo.isAdmin && m.email !== data.user.email"
              class="btn btn-danger"
              @click="removeTeamMember(m.email)"
            >Remove</button>
          </div>
        </div>
        <div v-if="data.teamInfo.isAdmin" class="team-admin-actions">
          <button class="btn btn-secondary" @click="inviteTeamMember">Invite Member</button>
          <button class="btn btn-secondary" @click="transferVM">Transfer VM</button>
        </div>
        <div v-if="data.teamInfo.isBillingOwner && data.teamInfo.onlyMember" class="danger-zone">
          <div class="danger-zone-title">Danger Zone</div>
          <p class="danger-zone-text">
            Disabling your team will remove all team shares, cancel pending invites, and delete team auth/SSO configuration. Your VMs will remain on your personal account.
          </p>
          <button class="btn btn-danger" @click="disableTeam">Disable Team</button>
        </div>
      </section>

      <!-- SSH Keys -->
      <section class="card">
        <h2 class="card-title">SSH Keys</h2>
        <p class="section-help">SSH keys allow you to connect to exe.dev via <code>ssh exe.dev</code></p>
        <div v-if="data.sshKeys.length === 0" class="empty-msg">No SSH keys registered.</div>
        <div v-for="key in data.sshKeys" :key="key.fingerprint" class="ssh-key-row">
          <div class="ssh-key-info">
            <div class="ssh-key-name">{{ key.comment }}</div>
            <div class="ssh-key-fp">{{ key.publicKey }}</div>
          </div>
          <div class="ssh-key-actions">
            <button class="btn btn-secondary" @click="renameSSHKey(key.comment, key.fingerprint)">Rename</button>
            <button class="btn btn-danger" @click="removeSSHKey(key.publicKey)">Remove</button>
          </div>
        </div>
        <div style="margin-top: 12px;">
          <button class="btn btn-secondary" @click="addSSHKey">Add SSH Key</button>
        </div>
      </section>

      <!-- Passkeys -->
      <section class="card">
        <h2 class="card-title">Passkeys</h2>
        <p class="section-help">Passkeys allow you to log into exe.dev on the web without going through e-mail.</p>
        <div v-if="!passkeySupported" class="text-muted">Passkeys are not supported on this device or browser.</div>
        <template v-else>
          <div v-if="data.passkeys.length === 0" class="empty-msg">No passkeys registered yet.</div>
          <div v-for="pk in data.passkeys" :key="pk.id" class="passkey-row">
            <div>
              <div class="passkey-name">{{ pk.name }}</div>
              <div class="text-muted" style="font-size: 11px;">Added {{ pk.createdAt }} · Last used {{ pk.lastUsedAt }}</div>
            </div>
            <div class="passkey-actions">
              <template v-if="!deletingPasskeys.has(pk.id)">
                <button class="btn btn-danger" @click="deletingPasskeys.add(pk.id)">Delete</button>
              </template>
              <template v-else>
                <span class="text-muted" style="font-size: 11px; margin-right: 4px;">Delete this passkey?</span>
                <button class="btn btn-danger" @click="confirmDeletePasskey(pk.id)">Yes</button>
                <button class="btn btn-secondary" @click="deletingPasskeys.delete(pk.id)">Cancel</button>
              </template>
            </div>
          </div>
          <div class="add-passkey-form">
            <input v-model="passkeyName" type="text" class="passkey-name-input" placeholder="Passkey name (e.g., MacBook, iPhone)" />
            <button class="btn btn-secondary" @click="addPasskey">Add Passkey</button>
          </div>
          <div v-if="passkeyError" class="passkey-error">{{ passkeyError }}</div>
        </template>
      </section>

      <!-- Site Sessions -->
      <section v-if="data.siteSessions.length > 0" class="card">
        <h2 class="card-title">Active Sessions</h2>
        <table class="mini-table">
          <thead><tr><th>Site</th><th>Last Used</th></tr></thead>
          <tbody>
            <tr v-for="s in data.siteSessions" :key="s.domain">
              <td><a :href="s.url" target="_blank" rel="noopener noreferrer">{{ s.domain }}</a></td>
              <td>{{ s.lastUsedAt || 'Never' }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- Shared VMs -->
      <section v-if="data.sharedBoxes.length > 0" class="card">
        <h2 class="card-title">Sites Shared With You</h2>
        <table class="mini-table">
          <thead><tr><th>VM</th><th>Owner</th></tr></thead>
          <tbody>
            <tr v-for="b in data.sharedBoxes" :key="b.name">
              <td><a :href="b.proxyURL" target="_blank" rel="noopener noreferrer">{{ b.name }}</a></td>
              <td>{{ b.ownerEmail }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>

    <!-- Region Change Modal -->
    <div v-if="showRegionModal" class="modal-overlay" @click.self="showRegionModal = false">
      <div class="modal-box">
        <div class="modal-title">Change Region</div>
        <p class="modal-text">Email <a href="mailto:support@exe.dev">support@exe.dev</a> to change your region.</p>
        <p class="modal-text"><a href="/docs/regions">View all regions</a></p>
        <div class="modal-actions">
          <button class="btn btn-secondary" @click="showRegionModal = false">Close</button>
        </div>
      </div>
    </div>

    <!-- Command Modal -->
    <CommandModal
      :visible="modal.visible"
      :title="modal.title"
      :description="modal.description"
      :command="modal.command"
      :command-prefix="modal.commandPrefix"
      :input-placeholder="modal.inputPlaceholder"
      :default-value="modal.defaultValue"
      :danger="modal.danger"
      @close="modal.visible = false"
      @success="reload"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { fetchProfile, shellQuote, type ProfileData } from '../api/client'
import CommandModal from '../components/CommandModal.vue'
import Tag from 'primevue/tag'
import ProgressBar from 'primevue/progressbar'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'

const loading = ref(true)
const loadError = ref('')
const data = ref<ProfileData | null>(null)
const passkeyName = ref('')
const deletingPasskeys = ref<Set<number>>(new Set())
const passkeyError = ref('')
const passkeySupported = ref(typeof window !== 'undefined' && window.PublicKeyCredential !== undefined)
const newsletterStatus = ref('')
const showRegionModal = ref(false)

// Team creation
const teamName = ref('')
const teamError = ref('')
const creatingTeam = ref(false)

// Billing state
const selectedAmount = ref(25)

const usageBarSeverity = computed(() => {
  const pct = data.value?.credits.monthlyUsedPct ?? 0
  if (pct >= 90) return 'danger'
  if (pct >= 75) return 'warning'
  if (pct >= 50) return 'info'
  return 'success'
})

const transactionHistory = computed(() => {
  if (!data.value) return []
  
  // Merge purchases and gifts
  const purchases = (data.value.credits.purchases || []).map(p => ({
    type: 'Purchase',
    amount: p.amount,
    date: p.date,
    details: p.receiptURL,
    rawDate: new Date(p.date)
  }))
  
  const gifts = (data.value.credits.gifts || []).map(g => ({
    type: 'Gift',
    amount: g.amount,
    date: '', // Gifts don't have dates in the current structure
    details: g.reason,
    rawDate: new Date(0) // Default to epoch for sorting
  }))
  
  // Combine and sort by date (newest first)
  return [...purchases, ...gifts].sort((a, b) => b.rawDate.getTime() - a.rawDate.getTime())
})

const modal = reactive({
  visible: false,
  title: '',
  description: '',
  command: '',
  commandPrefix: '',
  inputPlaceholder: '',
  defaultValue: '',
  danger: false,
})

async function reload() {
  try {
    data.value = await fetchProfile()
  } catch (err) {
    console.error('Failed to load profile:', err)
  }
}

async function loadProfile() {
  loading.value = true
  loadError.value = ''
  try {
    data.value = await fetchProfile()
  } catch (err: any) {
    console.error('Failed to load profile:', err)
    loadError.value = err.message || 'Failed to load data'
  } finally {
    loading.value = false
  }
}

onMounted(loadProfile)

function openModal(opts: Partial<typeof modal>) {
  Object.assign(modal, {
    visible: true,
    title: '',
    description: '',
    command: '',
    commandPrefix: '',
    inputPlaceholder: '',
    defaultValue: '',
    danger: false,
    ...opts,
  })
}

function addSSHKey() {
  openModal({
    title: 'Add SSH Key',
    description: 'Generate a key:<pre>ssh-keygen -t ed25519 -C "my-key" -f ~/.ssh/id_exe</pre>Then paste the contents of <code>~/.ssh/id_exe.pub</code> below.',
    commandPrefix: 'ssh-key add',
    inputPlaceholder: 'ssh-ed25519 AAAA... comment',
  })
}

function removeSSHKey(pubKey: string) {
  openModal({
    title: 'Remove SSH Key',
    command: `ssh-key remove ${shellQuote(pubKey)}`,
    danger: true,
  })
}

function renameSSHKey(name: string, fingerprint: string) {
  openModal({
    title: 'Rename SSH Key',
    commandPrefix: `ssh-key rename ${shellQuote('SHA256:' + fingerprint)}`,
    inputPlaceholder: 'new name',
    defaultValue: name,
  })
}

function removeTeamMember(email: string) {
  openModal({
    title: 'Remove Team Member',
    command: `team remove ${shellQuote(email)}`,
    danger: true,
  })
}

function inviteTeamMember() {
  openModal({
    title: 'Invite to Team',
    commandPrefix: 'team add',
    inputPlaceholder: 'user@example.com',
  })
}

function transferVM() {
  openModal({
    title: 'Transfer VM',
    commandPrefix: 'team transfer',
    inputPlaceholder: 'vm-name user@example.com',
  })
}

function disableTeam() {
  openModal({
    title: 'Disable Team',
    command: 'team disable',
    danger: true,
  })
}

async function acceptInvite(token: string) {
  try {
    const resp = await fetch('/team/invite/accept', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token }),
      redirect: 'follow',
    })
    if (resp.redirected) {
      window.location.href = resp.url
    } else {
      await reload()
    }
  } catch {
    await reload()
  }
}

async function declineInvite(token: string) {
  try {
    await fetch('/team/invite/decline', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token }),
    })
  } catch { /* ignore */ }
  await reload()
}

// --- Team creation ---

async function createTeam() {
  const name = teamName.value.trim()
  if (!name) {
    teamError.value = 'Team name is required.'
    return
  }
  teamError.value = ''
  creatingTeam.value = true
  try {
    const resp = await fetch('/team/enable', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ name }),
    })
    const result = await resp.json()
    if (result.success) {
      await reload()
      teamName.value = ''
    } else {
      teamError.value = result.error || 'Failed to create team.'
    }
  } catch {
    teamError.value = 'Request failed.'
  } finally {
    creatingTeam.value = false
  }
}

// --- Passkey management ---

function base64URLEncode(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const binary = Array.from(bytes, b => String.fromCharCode(b)).join('')
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '')
}

function base64URLDecode(str: string): ArrayBuffer {
  str = str.replace(/-/g, '+').replace(/_/g, '/')
  while (str.length % 4) str += '='
  const binary = atob(str)
  return Uint8Array.from(binary, c => c.charCodeAt(0)).buffer
}

function getDefaultPasskeyName(): string {
  const ua = navigator.userAgent
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua)) return 'iPad'
  if (/Macintosh/.test(ua)) return 'Mac'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Android/.test(ua)) return 'Android'
  if (/Linux/.test(ua)) return 'Linux'
  return 'Passkey'
}

async function addPasskey() {
  passkeyError.value = ''
  const name = passkeyName.value.trim() || getDefaultPasskeyName()

  try {
    const startResp = await fetch('/passkey/register/start', { method: 'POST', credentials: 'same-origin' })
    if (!startResp.ok) throw new Error(await startResp.text() || 'Failed to start registration')
    const options = await startResp.json()

    options.publicKey.challenge = base64URLDecode(options.publicKey.challenge)
    options.publicKey.user.id = base64URLDecode(options.publicKey.user.id)
    if (options.publicKey.excludeCredentials) {
      options.publicKey.excludeCredentials = options.publicKey.excludeCredentials.map((c: any) => ({ ...c, id: base64URLDecode(c.id) }))
    }

    const credential = await navigator.credentials.create(options) as PublicKeyCredential
    if (!credential) throw new Error('No credential created')
    const response = credential.response as AuthenticatorAttestationResponse

    const body: any = {
      id: credential.id,
      rawId: base64URLEncode(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: base64URLEncode(response.clientDataJSON),
        attestationObject: base64URLEncode(response.attestationObject),
      },
    }
    if (response.getTransports) {
      body.response.transports = response.getTransports()
    }

    const finishResp = await fetch('/passkey/register/finish?name=' + encodeURIComponent(name), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(body),
    })
    if (!finishResp.ok) throw new Error(await finishResp.text() || 'Failed to complete registration')

    passkeyName.value = ''
    await reload()
  } catch (err: any) {
    passkeyError.value = err.message || 'Failed to add passkey'
  }
}

async function confirmDeletePasskey(id: number) {
  try {
    await fetch('/passkey/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ id: String(id) }),
    })
  } catch { /* ignore */ }
  deletingPasskeys.value.delete(id)
  await reload()
}

// --- Newsletter ---

async function toggleNewsletter(event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  newsletterStatus.value = 'Saving...'
  try {
    const resp = await fetch('/newsletter-subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      credentials: 'same-origin',
      body: new URLSearchParams({ subscribed: checked ? '1' : '0' }),
    })
    if (!resp.ok) throw new Error('Failed')
    newsletterStatus.value = checked ? 'Subscribed!' : 'Unsubscribed'
    if (data.value) data.value.user.newsletterSubscribed = checked
    setTimeout(() => { newsletterStatus.value = '' }, 2000)
  } catch {
    newsletterStatus.value = 'Error saving'
    setTimeout(() => { newsletterStatus.value = '' }, 2000)
  }
}
</script>

<style scoped>
.profile-page {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.loading-state {
  text-align: center;
  padding: 48px;
  color: var(--text-color-secondary);
}

.error-state {
  text-align: center;
  padding: 48px;
  color: var(--danger-text);
}

.error-state p {
  margin-bottom: 12px;
}

.card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
}

.card-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}

.card-header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.card-header-row .card-title {
  margin-bottom: 0;
}

.subsection-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-color-secondary);
  margin-bottom: 8px;
  margin-top: 16px;
}

.info-grid {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.info-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.info-label {
  color: var(--text-color-muted);
  min-width: 80px;
}

.info-value {
  color: var(--text-color);
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-box {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
  max-width: 360px;
  width: 90%;
}

.modal-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 12px;
}

.modal-text {
  font-size: 13px;
  color: var(--text-color-secondary);
  margin-bottom: 8px;
}

.modal-actions {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.inline-link {
  font-size: 12px;
}

.text-muted {
  color: var(--text-color-muted);
  font-size: 12px;
}

/* Credit bar */
/* Redesigned Billing & Credits Section */
.billing-card-inner {
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 24px;
}

/* Billing Header */
.billing-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.billing-header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.plan-name {
  font-size: 24px;
  font-weight: 600;
  margin: 0;
}

.billing-header-right {
  display: flex;
  gap: 8px;
}

.active-tag {
  background: var(--text-color) !important;
  color: var(--surface-ground) !important;
}

/* Credit Cards Grid */
.credits-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-bottom: 24px;
}

.credit-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 20px;
}

.credit-card-title {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-muted);
  letter-spacing: 0.5px;
  margin-bottom: 12px;
}

.credit-card-amount {
  font-size: 32px;
  font-weight: 600;
  line-height: 1;
  margin-bottom: 4px;
}

.credit-card-subtitle {
  font-size: 14px;
  color: var(--text-color-muted);
  margin-bottom: 8px;
}

.credit-card-detail {
  font-size: 12px;
  color: var(--text-color-muted);
  line-height: 1.5;
}

/* Usage Section */
.usage-section {
  margin-bottom: 24px;
}

.usage-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
  font-size: 13px;
}

.usage-label {
  font-weight: 500;
}

.usage-pct {
  color: var(--text-color-muted);
}

.usage-footer {
  display: flex;
  justify-content: space-between;
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-color-muted);
}

/* Buy Section */
.buy-section {
  padding: 20px;
  border-top: 1px solid var(--surface-border);
  margin: 0 -24px 24px;
  padding: 20px 24px;
}

.buy-label {
  font-size: 13px;
  margin-bottom: 12px;
}

.buy-form {
  display: flex;
  gap: 12px;
  align-items: center;
}

.buy-amounts {
  display: flex;
  gap: 8px;
  flex: 1;
}

.amount-btn {
  padding: 8px 16px;
  border: 1px solid var(--surface-border);
  background: transparent;
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  color: var(--text-color);
  transition: all 0.15s;
}

.amount-btn:hover {
  background: var(--surface-hover);
}

.amount-btn-selected {
  border-color: var(--text-color);
  background: transparent;
  color: var(--text-color);
}

.buy-btn {
  background: var(--text-color);
  color: var(--surface-ground);
  border: none;
  padding: 10px 24px;
  font-weight: 600;
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
}

.buy-btn:hover {
  filter: brightness(0.9);
}

/* Transaction Section */
.transaction-section {
  margin-bottom: 24px;
}

.transaction-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.transaction-header .subsection-title {
  margin: 0;
}

.view-all-link {
  font-size: 12px;
  color: var(--text-color-muted);
  text-decoration: none;
}

.view-all-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

.transaction-table {
  font-size: 12px;
}

.receipt-link {
  color: var(--text-color-muted);
  text-decoration: none;
  font-size: 12px;
}

.receipt-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

/* Support Section */
.support-section {
  padding-top: 16px;
  border-top: 1px solid var(--surface-border);
  font-size: 12px;
}

.support-label {
  color: var(--text-color-muted);
  margin-right: 8px;
}

.support-section a {
  color: var(--text-color-muted);
  text-decoration: none;
}

.support-section a:hover {
  color: var(--text-color);
  text-decoration: underline;
}

.learn-more-link {
  color: var(--text-color-muted);
  text-decoration: none;
  font-size: 12px;
}

.learn-more-link:hover {
  color: var(--text-color);
  text-decoration: underline;
}

@media (max-width: 768px) {
  .credits-grid {
    grid-template-columns: 1fr;
  }
  
  .billing-header {
    flex-direction: column;
    align-items: flex-start;
    gap: 12px;
  }
  
  .buy-form {
    flex-direction: column;
    align-items: stretch;
  }
  
  .buy-amounts {
    width: 100%;
  }
}

/* ProgressBar severity colors */
:deep(.p-progressbar.p-progressbar-success .p-progressbar-value) {
  background: var(--success-color);
}

:deep(.p-progressbar.p-progressbar-info .p-progressbar-value) {
  background: var(--warning-color);
}

:deep(.p-progressbar.p-progressbar-warning .p-progressbar-value) {
  background: #f97316;
}

:deep(.p-progressbar.p-progressbar-danger .p-progressbar-value) {
  background: var(--danger-color);
}

/* SSH Keys */
.ssh-key-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
  gap: 12px;
}

.ssh-key-row:last-of-type {
  border-bottom: none;
}

.ssh-key-info {
  min-width: 0;
  flex: 1;
}

.ssh-key-name {
  font-weight: 500;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-key-fp {
  font-size: 11px;
  color: var(--text-color-muted);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-key-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

@media (max-width: 600px) {
  .ssh-key-row {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .ssh-key-info {
    width: 100%;
  }

  .ssh-key-actions {
    align-self: flex-end;
  }
}


/* Passkeys */
.passkey-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px solid var(--surface-subtle);
}

.passkey-name {
  font-weight: 500;
  font-size: 13px;
}

.passkey-actions {
  display: flex;
  align-items: center;
  gap: 4px;
}

.section-help {
  font-size: 12px;
  color: var(--text-color-muted);
  margin-bottom: 12px;
}

.add-passkey-form {
  display: flex;
  gap: 8px;
  margin-top: 12px;
  align-items: center;
}

.passkey-name-input {
  padding: 6px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  font-size: 12px;
  font-family: inherit;
  flex: 1;
  max-width: 300px;
  height: 30px;
  box-sizing: border-box;
}

.passkey-error {
  color: var(--danger-color);
  font-size: 12px;
  margin-top: 8px;
}

/* Newsletter */
.newsletter-label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 13px;
}

.newsletter-label input[type="checkbox"] {
  accent-color: var(--text-color);
  width: 16px;
  height: 16px;
}

.newsletter-status {
  font-size: 11px;
  color: var(--text-color-muted);
  margin-left: 4px;
}

/* Invite */
.invite-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px;
  background: var(--warning-bg);
  border: 1px solid var(--warning-color);
  border-radius: 6px;
}

.invite-actions {
  display: flex;
  gap: 8px;
}

/* Members */
.member-list {
  margin-top: 12px;
}

.member-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--surface-subtle);
}

/* Table */
.mini-table {
  width: 100%;
  font-size: 12px;
  border-collapse: collapse;
}

.mini-table th {
  text-align: left;
  font-weight: 500;
  color: var(--text-color-muted);
  padding: 6px 8px;
  border-bottom: 1px solid var(--surface-border);
}

.mini-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--surface-subtle);
}

.empty-msg {
  color: var(--text-color-muted);
  font-size: 13px;
}

.section-desc {
  font-size: 14px;
  line-height: 1.6;
  margin: 0 0 12px;
}

.section-desc.text-muted {
  font-size: 13px;
  color: var(--text-color-muted);
  margin-bottom: 16px;
}

.create-team-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.form-input {
  flex: 1;
  padding: 6px 10px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
}

.field-error {
  color: var(--danger-color);
  font-size: 13px;
  margin-top: 8px;
}

.text-link {
  font-size: 12px;
}

/* Extra credits */


/* Buttons */
.btn {
  padding: 5px 12px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.15s;
  height: 30px;
  box-sizing: border-box;
}

.btn-small {
  padding: 3px 8px;
  font-size: 11px;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover {
  filter: brightness(1.1);
}

.btn-secondary {
  background: var(--btn-bg);
  color: var(--btn-text);
  border-color: var(--btn-border);
}

.btn-secondary:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.btn-danger {
  background: var(--btn-bg);
  color: var(--danger-color);
  border-color: var(--danger-border);
}

.btn-danger:hover {
  background: var(--danger-bg);
}

/* Team admin actions */
.team-admin-actions {
  margin-top: 16px;
  display: flex;
  gap: 8px;
}

/* Danger zone */
.danger-zone {
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--surface-border);
}

.danger-zone-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--danger-color);
  margin-bottom: 8px;
}

.danger-zone-text {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--text-color-muted);
}

/* Invite warning */
.invite-warning {
  font-size: 12px;
  color: var(--text-color-muted);
  margin-top: 4px;
}
</style>

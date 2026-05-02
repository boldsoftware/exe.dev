<template>
  <div class="cd-view">
    <h2 class="page-title">Continuous Deployment</h2>
    <p class="page-subtitle">Automated deploys run every 30 minutes during business hours (9 AM ET – 5 PM PT, weekdays, excluding holidays).</p>

    <div class="cd-services">
      <div class="cd-service-card">
        <div class="service-header">
          <div class="service-info">
            <span class="service-name">exed</span>
            <span class="service-desc">API server</span>
          </div>
          <label class="toggle-switch">
            <input
              type="checkbox"
              :checked="status?.enabled"
              :disabled="actionPending || !status"
              @change="toggleCD"
            />
            <span class="toggle-slider"></span>
          </label>
        </div>

        <div class="service-status" v-if="status">
          <div class="status-row">
            <span class="status-label">Status</span>
            <span class="status-value" :class="statusClass">
              <span class="status-dot"></span>
              {{ statusText }}
            </span>
          </div>

          <div class="status-row" v-if="status.enabled && status.next_deploy_at">
            <span class="status-label">Next deploy</span>
            <span class="status-value">
              {{ nextDeployFormatted }}
              <span class="countdown" v-if="countdown">({{ countdown }})</span>
            </span>
          </div>

          <div class="status-row" v-if="status.last_deploy">
            <span class="status-label">Last deploy</span>
            <span class="status-value">
              <a :href="commitURL(status.last_deploy.sha)" target="_blank" class="sha-link">
                {{ status.last_deploy.sha.slice(0, 12) }}
              </a>
              <span :class="'deploy-state deploy-state-' + status.last_deploy.state">
                {{ status.last_deploy.state }}
              </span>
            </span>
          </div>

          <div class="status-row" v-if="status.deploying">
            <span class="status-label">Currently</span>
            <span class="status-value status-deploying">Deploying…</span>
          </div>

          <div class="status-row" v-if="status.disabled_reason">
            <span class="status-label">Reason</span>
            <span class="status-value status-reason">{{ status.disabled_reason }}</span>
          </div>

          <div class="status-row">
            <span class="status-label">Window</span>
            <span class="status-value">
              {{ status.window_open ? 'Open' : 'Closed' }}
            </span>
          </div>
        </div>

        <div class="service-status" v-else>
          <div class="status-row">
            <span class="status-label">Status</span>
            <span class="status-value status-unavailable">Not configured</span>
          </div>
        </div>
      </div>

      <!-- Future services go here -->
      <div class="cd-service-card cd-service-disabled">
        <div class="service-header">
          <div class="service-info">
            <span class="service-name">exeprox</span>
            <span class="service-desc">Proxy</span>
          </div>
          <label class="toggle-switch">
            <input type="checkbox" disabled />
            <span class="toggle-slider"></span>
          </label>
        </div>
        <div class="service-status">
          <div class="status-row">
            <span class="status-label">Status</span>
            <span class="status-value status-unavailable">Not available yet</span>
          </div>
        </div>
      </div>

      <div class="cd-service-card cd-service-disabled">
        <div class="service-header">
          <div class="service-info">
            <span class="service-name">exeletd</span>
            <span class="service-desc">Container host</span>
          </div>
          <label class="toggle-switch">
            <input type="checkbox" disabled />
            <span class="toggle-slider"></span>
          </label>
        </div>
        <div class="service-status">
          <div class="status-row">
            <span class="status-label">Status</span>
            <span class="status-value status-unavailable">Not available yet</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  fetchCDStatus,
  enableCD,
  disableCD,
  type CDStatus,
} from '../api/client'

const status = ref<CDStatus | null>(null)
const actionPending = ref(false)
const now = ref(Date.now())

let pollTimer: ReturnType<typeof setInterval> | null = null
let tickTimer: ReturnType<typeof setInterval> | null = null

const GITHUB_COMMIT = 'https://github.com/boldsoftware/exe/commit/'

function commitURL(sha: string): string {
  return GITHUB_COMMIT + sha
}

const statusClass = computed(() => {
  if (!status.value) return ''
  if (status.value.deploying) return 'status-active'
  if (status.value.enabled) return 'status-enabled'
  return 'status-disabled'
})

const statusText = computed(() => {
  if (!status.value) return 'Unknown'
  if (status.value.deploying) return 'Deploying'
  if (status.value.enabled) return 'Enabled'
  return 'Disabled'
})

const nextDeployFormatted = computed(() => {
  if (!status.value?.next_deploy_at) return ''
  const d = new Date(status.value.next_deploy_at)
  return d.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
    timeZoneName: 'short',
  })
})

const countdown = computed(() => {
  if (!status.value?.next_deploy_at) return null
  const next = new Date(status.value.next_deploy_at).getTime()
  const diff = next - now.value
  if (diff < 0) return null
  const mins = Math.floor(diff / 60000)
  const secs = Math.floor((diff % 60000) / 1000)
  return `${mins}:${secs.toString().padStart(2, '0')}`
})

async function loadStatus() {
  try {
    status.value = await fetchCDStatus()
  } catch {
    // CD not configured on this environment
  }
}

async function toggleCD() {
  if (!status.value || actionPending.value) return
  actionPending.value = true
  try {
    if (status.value.enabled) {
      status.value = await disableCD()
    } else {
      status.value = await enableCD()
    }
  } catch {
    // reload to get actual state
    await loadStatus()
  } finally {
    actionPending.value = false
  }
}

onMounted(() => {
  loadStatus()
  pollTimer = setInterval(loadStatus, 10000)
  tickTimer = setInterval(() => { now.value = Date.now() }, 1000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (tickTimer) clearInterval(tickTimer)
})
</script>

<style scoped>
.cd-view {
  max-width: 800px;
}

.page-title {
  font-size: 1.4rem;
  font-weight: 600;
  margin-bottom: 0.25rem;
}

.page-subtitle {
  color: var(--text-color-secondary);
  font-size: 0.85rem;
  margin-bottom: 1.5rem;
}

.cd-services {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.cd-service-card {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 1.25rem;
}

.cd-service-disabled {
  opacity: 0.5;
}

.service-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1rem;
  padding-bottom: 0.75rem;
  border-bottom: 1px solid var(--surface-border);
}

.service-info {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
}

.service-name {
  font-size: 1.1rem;
  font-weight: 600;
  font-family: 'JetBrains Mono', monospace;
}

.service-desc {
  font-size: 0.8rem;
  color: var(--text-color-secondary);
}

/* Toggle switch */
.toggle-switch {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
  cursor: pointer;
}

.toggle-switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

.toggle-slider {
  position: absolute;
  inset: 0;
  background: var(--surface-border);
  border-radius: 24px;
  transition: background 0.2s;
}

.toggle-slider::before {
  content: '';
  position: absolute;
  width: 18px;
  height: 18px;
  left: 3px;
  top: 3px;
  background: var(--text-color);
  border-radius: 50%;
  transition: transform 0.2s;
}

.toggle-switch input:checked + .toggle-slider {
  background: var(--green-400);
}

.toggle-switch input:checked + .toggle-slider::before {
  transform: translateX(20px);
}

.toggle-switch input:disabled + .toggle-slider {
  opacity: 0.4;
  cursor: not-allowed;
}

/* Status rows */
.service-status {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.status-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 0.85rem;
}

.status-label {
  color: var(--text-color-secondary);
  font-weight: 500;
}

.status-value {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
}

.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}

.status-enabled .status-dot {
  background: var(--green-400);
}

.status-active .status-dot {
  background: var(--yellow-400);
  animation: pulse 1.5s infinite;
}

.status-disabled .status-dot {
  background: var(--text-color-muted);
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

.status-deploying {
  color: var(--yellow-400);
}

.status-reason {
  color: var(--red-400);
  font-family: inherit;
  font-size: 0.8rem;
}

.status-unavailable {
  color: var(--text-color-muted);
  font-family: inherit;
}

.sha-link {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
}

.deploy-state {
  font-size: 0.7rem;
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
  font-family: inherit;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.deploy-state-success {
  background: var(--green-subtle);
  color: var(--green-400);
}

.deploy-state-failed {
  background: var(--red-subtle);
  color: var(--red-400);
}

.countdown {
  color: var(--text-color-secondary);
  font-size: 0.75rem;
}
</style>

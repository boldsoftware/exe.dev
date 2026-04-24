<template>
  <div v-if="visible" class="modal-overlay" @click.self="close">
    <div class="modal-panel" role="dialog" aria-modal="true" aria-label="Resize Disk">
      <div class="modal-header">
        <h3>Resize Disk<span v-if="boxName"> — {{ boxName }}</span></h3>
        <button class="modal-close" aria-label="Close" @click="close">&times;</button>
      </div>
      <div class="modal-body">
        <div v-if="loading" class="rd-loading">
          <i class="pi pi-spin pi-spinner"></i> Loading current disk info…
        </div>
        <div v-else-if="loadError" class="cmd-result error">{{ loadError }}</div>
        <div v-else-if="!canResize" class="cmd-result error">
          {{ noResizeReason || 'Disk resize is not available on your current plan.' }}
        </div>
        <template v-else>
          <p class="rd-desc">
            Resizes the disk volume. The VM will need to be rebooted.
          </p>

          <div class="rd-current">
            Current size: <strong>{{ fmtGiB(currentBytes) }}</strong>
            <span class="rd-muted">· max on plan: {{ fmtGiB(planMaxBytes) }}</span>
          </div>

          <div class="rd-slider-row">
            <input
              ref="sliderRef"
              v-model.number="targetGiB"
              type="range"
              :min="sliderMinGiB"
              :max="sliderMaxGiB"
              step="1"
              class="rd-slider"
              :disabled="cmd.loading.value || cmd.success.value || sliderMinGiB > sliderMaxGiB"
            />
            <input
              v-model.number="targetGiB"
              type="number"
              :min="sliderMinGiB"
              :max="sliderMaxGiB"
              step="1"
              class="rd-number"
              :disabled="cmd.loading.value || cmd.success.value"
            />
            <span class="rd-unit">GiB</span>
          </div>
          <div class="rd-scale">
            <span>{{ sliderMinGiB }} GiB</span>
            <span v-if="singleOpCapApplies" class="rd-muted">
              (up to +{{ maxGrowthGiB }} GiB per operation)
            </span>
            <span>{{ sliderMaxGiB }} GiB</span>
          </div>

          <div class="cmd-display">
            <code>{{ shownCommand }}</code>
          </div>

          <div
            v-if="result.output || result.error"
            class="cmd-result"
            :class="{ success: result.success, error: !result.success }"
          >{{ result.output || result.error }}</div>
        </template>
      </div>
      <div class="modal-footer">
        <button v-if="!cmd.success.value" class="btn btn-secondary" @click="close">Cancel</button>
        <button
          v-if="cmd.success.value"
          class="btn btn-primary"
          @click="close"
        >Done</button>
        <button
          v-else
          class="btn btn-primary"
          :disabled="cmd.loading.value || !canRun"
          @click="run"
        >{{ cmd.loading.value ? 'Resizing…' : 'Resize' }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import { fetchVMLiveMetrics, fetchProfile, shellQuote } from '../api/client'
import { useCommand } from '../composables/useCommand'

const GIB = 1024 * 1024 * 1024
// Matches server-side maxDiskGrowth in exelet/services/compute/grow_disk.go
const MAX_GROWTH_GIB = 250

const props = defineProps<{
  visible: boolean
  boxName: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'success'): void
}>()

const cmd = useCommand()
const loading = ref(false)
const loadError = ref('')
const currentBytes = ref(0)
const planMaxBytes = ref(0)
const canResize = ref(false)
const noResizeReason = ref('')
const targetGiB = ref(0)
const result = ref({ output: '', error: '', success: false })
const sliderRef = ref<HTMLInputElement | null>(null)

const currentGiB = computed(() => Math.ceil(currentBytes.value / GIB))
const planMaxGiB = computed(() => Math.floor(planMaxBytes.value / GIB))
const maxGrowthGiB = computed(() => MAX_GROWTH_GIB)
const singleOpCapApplies = computed(
  () => planMaxGiB.value - currentGiB.value > MAX_GROWTH_GIB,
)

const sliderMinGiB = computed(() => currentGiB.value + 1)
const sliderMaxGiB = computed(() => {
  const byPlan = planMaxGiB.value
  const byOp = currentGiB.value + MAX_GROWTH_GIB
  return Math.max(sliderMinGiB.value, Math.min(byPlan, byOp))
})

// NOTE: the SSH command this modal runs (`resize <vm> --disk=<N>GiB`) must be
// present in the web allowlist (`allowedCommands` in execore/web-cmd.go) or the
// /cmd endpoint will reject it. If you change the command verb/flags here, keep
// that allowlist in sync.
const shownCommand = computed(() => {
  if (!props.boxName || targetGiB.value <= 0) return ''
  return `resize ${shellQuote(props.boxName)} --disk=${targetGiB.value}GiB`
})

const canRun = computed(() =>
  canResize.value
  && targetGiB.value >= sliderMinGiB.value
  && targetGiB.value <= sliderMaxGiB.value,
)

function fmtGiB(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 GiB'
  const gib = bytes / GIB
  if (gib >= 100 || Number.isInteger(gib)) return `${Math.round(gib)} GiB`
  return `${gib.toFixed(1)} GiB`
}

async function load() {
  loading.value = true
  loadError.value = ''
  canResize.value = false
  noResizeReason.value = ''
  try {
    const [metrics, profile] = await Promise.all([
      fetchVMLiveMetrics(props.boxName),
      fetchProfile(),
    ])
    currentBytes.value = metrics.disk_capacity_bytes || 0
    const maxGB = profile?.planCapacity?.maxDiskGB || 0
    planMaxBytes.value = maxGB * GIB
    if (!planMaxBytes.value) {
      noResizeReason.value = 'Disk resize is not available on your current plan. Upgrade to enable disk resizing.'
    } else if (currentBytes.value <= 0) {
      noResizeReason.value = "Couldn't determine the current disk size. Try again in a moment."
    } else if (currentBytes.value >= planMaxBytes.value) {
      noResizeReason.value = `This VM is already at your plan's maximum disk size (${fmtGiB(planMaxBytes.value)}).`
    } else {
      canResize.value = true
      const def = Math.min(sliderMaxGiB.value, Math.max(sliderMinGiB.value, currentGiB.value + 10))
      targetGiB.value = def
    }
  } catch (e: any) {
    loadError.value = e?.message || 'Failed to load VM info'
  } finally {
    loading.value = false
  }
}

async function run() {
  if (!canRun.value || cmd.loading.value || cmd.success.value) return
  result.value = { output: '', error: '', success: false }
  const res = await cmd.execute(shownCommand.value)
  result.value = {
    output: res.output || '',
    error: res.error || '',
    success: !!res.success,
  }
  if (res.success) emit('success')
}

function close() {
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  emit('close')
}

function onEscape(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

watch(() => props.visible, (v) => {
  if (v) {
    cmd.reset()
    result.value = { output: '', error: '', success: false }
    load()
    document.addEventListener('keydown', onEscape)
    nextTick(() => sliderRef.value?.focus())
  } else {
    document.removeEventListener('keydown', onEscape)
  }
}, { immediate: true })
</script>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--surface-overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-panel {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  width: 520px;
  max-width: 92vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  border-bottom: 1px solid var(--surface-border);
}

.modal-header h3 {
  font-size: 14px;
  font-weight: 600;
  margin: 0;
}

.modal-close {
  background: none;
  border: none;
  font-size: 20px;
  cursor: pointer;
  color: var(--text-color-muted);
  padding: 0 4px;
}

.modal-body {
  padding: 14px 18px;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 18px;
  border-top: 1px solid var(--surface-border);
}

.btn {
  padding: 6px 16px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.15s;
}

.btn:disabled { opacity: 0.6; cursor: not-allowed; }

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}
.btn-primary:hover:not(:disabled) { filter: brightness(1.1); }

.btn-secondary {
  background: var(--btn-bg);
  color: var(--btn-text);
  border-color: var(--btn-border);
}
.btn-secondary:hover:not(:disabled) {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.rd-desc {
  font-size: 13px;
  color: var(--text-color-secondary);
  line-height: 1.5;
  margin: 0 0 10px 0;
}



.rd-current {
  font-size: 13px;
  margin-bottom: 10px;
}

.rd-muted {
  color: var(--text-color-muted);
  margin-left: 6px;
}

.rd-slider-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 6px 0 2px 0;
}

.rd-slider {
  flex: 1;
  accent-color: var(--primary-color, #6366f1);
}

.rd-number {
  width: 80px;
  padding: 6px 8px;
  border: 1px solid var(--input-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  outline: none;
  text-align: right;
}
.rd-number:focus { border-color: var(--input-focus-border); }

.rd-unit {
  font-size: 12px;
  color: var(--text-color-muted);
}

.rd-scale {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: var(--text-color-muted);
  margin-bottom: 10px;
}

.cmd-display {
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 8px 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
  word-break: break-all;
}

.rd-loading {
  font-size: 13px;
  color: var(--text-color-secondary);
  padding: 8px 0;
}

.cmd-result {
  margin-top: 10px;
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  white-space: pre-wrap;
}

.cmd-result.success {
  background: var(--success-bg);
  color: var(--success-text);
  border: 1px solid var(--success-border);
}

.cmd-result.error {
  background: var(--danger-bg);
  color: var(--danger-text);
  border: 1px solid var(--danger-border);
}
</style>

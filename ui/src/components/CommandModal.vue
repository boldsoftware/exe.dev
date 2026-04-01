<template>
  <div v-if="visible" class="modal-overlay" @click.self="close">
    <div class="modal-panel" role="dialog" aria-modal="true" :aria-label="title">
      <div class="modal-header">
        <h3>{{ title }}</h3>
        <button class="modal-close" aria-label="Close" @click="close">&times;</button>
      </div>
      <div class="modal-body" role="document">
        <!-- eslint-disable-next-line vue/no-v-html -- descriptions are built from trusted code, not user data -->
        <div v-if="description" ref="descRef" class="modal-description" v-html="description"></div>
        <div class="cmd-display">
          <code>{{ shownCommand }}</code>
        </div>
        <input
          v-if="needsInput"
          ref="inputRef"
          v-model="inputValue"
          class="cmd-input"
          :placeholder="inputPlaceholder"
          autocomplete="off"
          @keydown.enter="run"
        />
        <div v-if="(result.output || result.error) && !formattedResult" class="cmd-result" :class="{ success: result.success, error: !result.success }">
          {{ result.output || result.error }}
        </div>
        <!-- Formatted success result (e.g. share-link with copy button) -->
        <!-- eslint-disable-next-line vue/no-v-html -- built from trusted JSON data -->
        <div v-if="formattedResult" class="cmd-result success formatted" v-html="formattedResult"></div>
      </div>
      <div class="modal-footer">
        <button v-if="!cmd.success.value" class="btn btn-secondary" @click="close">Cancel</button>
        <button
          v-if="cmd.success.value"
          class="btn btn-primary"
          @click="close"
        >
          Done
        </button>
        <button
          v-else
          class="btn" :class="danger ? 'btn-danger' : 'btn-primary'"
          :disabled="cmd.loading.value"
          @click="run"
        >
          {{ cmd.loading.value ? 'Running...' : 'Run' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import { useCommand } from '../composables/useCommand'
import { shellQuote } from '../api/client'

const props = defineProps<{
  visible: boolean
  title: string
  description?: string
  command?: string
  displayCommand?: string
  commandPrefix?: string
  inputPlaceholder?: string
  defaultValue?: string
  danger?: boolean
  successFormat?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'success', output: string): void
}>()

const cmd = useCommand()
const inputValue = ref('')
const inputRef = ref<HTMLInputElement | null>(null)
const descRef = ref<HTMLElement | null>(null)
const result = ref({ output: '', error: '', success: false })
const formattedResult = ref('')

function injectPreCopyButtons() {
  if (!descRef.value) return
  descRef.value.querySelectorAll('pre').forEach(pre => {
    if (pre.querySelector('.pre-copy-btn')) return
    pre.style.position = 'relative'
    const btn = document.createElement('button')
    btn.className = 'pre-copy-btn'
    btn.title = 'Copy'
    btn.innerHTML = '<i class="pi pi-copy" style="font-size:11px;"></i>'
    btn.addEventListener('click', (e) => {
      e.stopPropagation()
      navigator.clipboard.writeText(pre.textContent?.replace(/\n$/, '') || '').then(() => {
        btn.innerHTML = '<i class="pi pi-check" style="font-size:11px;"></i>'
        btn.classList.add('copied')
        setTimeout(() => {
          btn.innerHTML = '<i class="pi pi-copy" style="font-size:11px;"></i>'
          btn.classList.remove('copied')
        }, 1500)
      })
    })
    pre.appendChild(btn)
  })
}

const needsInput = computed(() => !!props.commandPrefix && !props.command)

const displayCommand = computed(() => {
  if (props.command) return props.command
  if (props.commandPrefix && inputValue.value.trim()) {
    return `${props.commandPrefix} ${shellQuote(inputValue.value.trim())}`
  }
  if (props.commandPrefix && props.inputPlaceholder) {
    return `${props.commandPrefix} <${props.inputPlaceholder}>`
  }
  return props.commandPrefix || ''
})

/** Command text shown in the UI (may hide internal flags like --json). */
const shownCommand = computed(() => {
  if (props.displayCommand) return props.displayCommand
  return displayCommand.value
})

const fullCommand = computed(() => {
  if (props.command) return props.command
  if (props.commandPrefix && inputValue.value.trim()) {
    return `${props.commandPrefix} ${shellQuote(inputValue.value.trim())}`
  }
  return ''
})

function escapeHtml(text: string): string {
  const div = document.createElement('div')
  div.textContent = text
  return div.innerHTML
}

function formatSuccessResult(output: string, format: string): string {
  if (format === 'share-link') {
    try {
      const data = JSON.parse(output)
      const h = escapeHtml
      return `<div class="share-link-result">
        <div class="share-link-check">✓ Share link created</div>
        <div class="share-link-field">
          <div class="share-link-label">Share URL</div>
          <div class="share-link-copy-row">
            <code class="share-link-url">${h(data.url)}</code>
            <button class="share-link-copy-btn" data-copy-url title="Copy URL"><i class="pi pi-copy" style="font-size:11px;"></i></button>
          </div>
        </div>
        <div class="share-link-revoke">To revoke: <code>share remove-link ${h(data.vm_name)} ${h(data.token)}</code></div>
      </div>`
    } catch {
      return ''
    }
  }
  return ''
}

function attachCopyHandlers() {
  // Attach click handler for the copy button rendered via v-html
  const btn = document.querySelector('[data-copy-url]') as HTMLButtonElement | null
  if (!btn) return
  const url = btn.closest('.share-link-result')?.querySelector('.share-link-url')?.textContent || ''
  btn.removeAttribute('data-copy-url')
  const copyIcon = '<i class="pi pi-copy" style="font-size:11px;"></i>'
  const checkIcon = '<i class="pi pi-check" style="font-size:11px;"></i>'
  btn.addEventListener('click', () => {
    navigator.clipboard.writeText(url).then(() => {
      btn.innerHTML = checkIcon
      btn.classList.add('copied')
      setTimeout(() => { btn.innerHTML = copyIcon; btn.classList.remove('copied') }, 1500)
    }).catch(() => {
      // Fallback
      const ta = document.createElement('textarea')
      ta.value = url
      ta.style.cssText = 'position:fixed;opacity:0'
      document.body.appendChild(ta)
      ta.select()
      try { document.execCommand('copy') } catch { /* noop */ }
      document.body.removeChild(ta)
      btn.innerHTML = checkIcon
      btn.classList.add('copied')
      setTimeout(() => { btn.innerHTML = copyIcon; btn.classList.remove('copied') }, 1500)
    })
  })
}

function onEscapeKey(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

watch(() => props.visible, (v) => {
  if (v) {
    inputValue.value = props.defaultValue || ''
    cmd.reset()
    result.value = { output: '', error: '', success: false }
    formattedResult.value = ''
    nextTick(() => {
      injectPreCopyButtons()
      inputRef.value?.focus()
    })
    document.addEventListener('keydown', onEscapeKey)
  } else {
    document.removeEventListener('keydown', onEscapeKey)
  }
})

async function run() {
  if (cmd.loading.value || cmd.success.value) return
  const command = fullCommand.value
  if (!command) {
    inputRef.value?.focus()
    return
  }

  const res = await cmd.execute(command)
  result.value = {
    output: res.output || '',
    error: res.error || '',
    success: !!res.success,
  }
  if (res.success) {
    if (props.successFormat) {
      formattedResult.value = formatSuccessResult(res.output || '', props.successFormat)
    }
    if (formattedResult.value) {
      nextTick(attachCopyHandlers)
    }
    emit('success', res.output || '')
  }
}

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onEscapeKey)
})

function close() {
  // Blur focused element before hiding to prevent browser scroll jump
  // when v-if destroys the focused input.
  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur()
  }
  emit('close')
}
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
  width: 480px;
  max-width: 90vw;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.2);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
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
  padding: 16px 20px;
}

.modal-description {
  font-size: 13px;
  color: var(--text-color-secondary);
  margin-bottom: 12px;
  line-height: 1.5;
}

.modal-description :deep(code) {
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
  font-size: 12px;
  background: var(--code-bg);
  padding: 2px 5px;
  border-radius: 3px;
}

.modal-description :deep(pre) {
  background: var(--surface-subtle);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 8px 36px 8px 12px;
  font-family: var(--font-mono, 'JetBrains Mono', ui-monospace, monospace);
  font-size: 12px;
  margin: 6px 0;
  white-space: pre-wrap;
  word-break: break-all;
  position: relative;
}

.modal-description :deep(.pre-copy-btn) {
  position: absolute;
  top: 50%;
  right: 6px;
  transform: translateY(-50%);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 3px 5px;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 3px;
  cursor: pointer;
  color: var(--btn-text);
  transition: all 0.15s;
  opacity: 0.6;
}

.modal-description :deep(pre:hover .pre-copy-btn) {
  opacity: 1;
}

.modal-description :deep(.pre-copy-btn:hover) {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.modal-description :deep(.pre-copy-btn.copied) {
  color: var(--success-color);
  border-color: var(--success-color);
  opacity: 1;
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

.cmd-input {
  width: 100%;
  margin-top: 8px;
  padding: 8px 12px;
  border: 1px solid var(--input-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  outline: none;
}

.cmd-input:focus {
  border-color: var(--input-focus-border);
}

.cmd-result {
  margin-top: 8px;
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

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 20px;
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

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--text-color);
  color: var(--surface-ground);
}

.btn-primary:hover:not(:disabled) {
  filter: brightness(1.1);
}

.btn-secondary {
  background: var(--btn-bg);
  color: var(--btn-text);
  border-color: var(--btn-border);
}

.btn-secondary:hover:not(:disabled) {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.btn-danger {
  background: var(--danger-color);
  color: #fff;
}

.btn-danger:hover:not(:disabled) {
  background: var(--danger-hover);
}

/* Formatted results override pre-wrap from plain .cmd-result */
.cmd-result.formatted {
  white-space: normal;
  font-family: inherit;
}

.cmd-result :deep(.share-link-check) {
  font-weight: 600;
  margin-bottom: 10px;
}

.cmd-result :deep(.share-link-label) {
  font-size: 11px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  opacity: 0.7;
  margin-bottom: 4px;
}

.cmd-result :deep(.share-link-copy-row) {
  display: flex;
  align-items: center;
  gap: 8px;
}

.cmd-result :deep(.share-link-url) {
  flex: 1;
  min-width: 0;
  padding: 6px 8px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 12px;
  word-break: break-all;
  user-select: all;
}

.cmd-result :deep(.share-link-copy-btn) {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  background: var(--btn-bg);
  color: var(--btn-text);
  cursor: pointer;
  transition: all 0.15s;
}

.cmd-result :deep(.share-link-copy-btn:hover) {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
}

.cmd-result :deep(.share-link-copy-btn.copied) {
  color: var(--success-color);
  border-color: var(--success-color);
}

.cmd-result :deep(.share-link-revoke) {
  margin-top: 10px;
  font-size: 11px;
  opacity: 0.7;
}

.cmd-result :deep(.share-link-revoke code) {
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 11px;
  background: var(--surface-card);
  padding: 1px 4px;
  border-radius: 3px;
  word-break: break-all;
}
</style>

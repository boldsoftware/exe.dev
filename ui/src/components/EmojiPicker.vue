<template>
  <Teleport to="body">
    <div v-if="open" class="emoji-backdrop" @click="close"></div>
    <div
      v-if="open"
      ref="popoverRef"
      class="emoji-popover"
      :style="popoverStyle"
      role="dialog"
      aria-label="Pick an emoji"
      @click.stop
      @keydown.escape.stop="close"
    >
      <div v-if="saving" class="emoji-status">Saving…</div>
      <div v-else-if="errorMsg" class="emoji-status error">{{ errorMsg }}</div>

      <component
        :is="'emoji-picker'"
        ref="pickerRef"
        class="emoji-picker-el"
        :class="{ dark: isDark }"
      />
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onBeforeUnmount, onMounted } from 'vue'
import 'emoji-picker-element'

const props = defineProps<{
  open: boolean
  anchorEl: HTMLElement | null
  current: string
  recents?: string[]
  saving?: boolean
  errorMsg?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'pick', emoji: string): void
}>()

const popoverRef = ref<HTMLElement | null>(null)
const pickerRef = ref<HTMLElement | null>(null)
const popoverStyle = ref<Record<string, string>>({})
const isDark = ref(false)

function detectDark() {
  isDark.value = document.documentElement.classList.contains('exe-dark') ||
    document.documentElement.getAttribute('data-theme') === 'dark' ||
    window.matchMedia?.('(prefers-color-scheme: dark)').matches === true
}

function pick(c: string) {
  emit('pick', c)
}

function close() {
  emit('close')
}

function position() {
  if (!props.anchorEl) return
  const rect = props.anchorEl.getBoundingClientRect()
  const popW = 352
  const popH = 440
  let left = rect.left
  if (left + popW > window.innerWidth - 8) {
    left = Math.max(8, window.innerWidth - popW - 8)
  }
  let top = rect.bottom + 6
  if (top + popH > window.innerHeight - 8) {
    top = Math.max(8, rect.top - popH - 6)
  }
  popoverStyle.value = {
    top: `${top}px`,
    left: `${Math.max(8, left)}px`,
  }
}

function onScrollOrResize() {
  if (props.open) position()
}

// emoji-picker-element emits 'emoji-click' with { detail: { unicode, emoji, ... } }
function onEmojiClick(ev: Event) {
  const detail = (ev as CustomEvent).detail
  const unicode = detail?.unicode || detail?.emoji?.unicode
  if (unicode) pick(unicode)
}

let attached = false
function attachListener() {
  const el = pickerRef.value
  if (!el || attached) return
  el.addEventListener('emoji-click', onEmojiClick)
  attached = true
}
function detachListener() {
  const el = pickerRef.value
  if (el && attached) {
    el.removeEventListener('emoji-click', onEmojiClick)
  }
  attached = false
}

watch(() => props.open, (v) => {
  if (v) {
    detectDark()
    nextTick(() => {
      position()
      attachListener()
      // Focus the picker's internal search input if available.
      const picker = pickerRef.value as any
      const input = picker?.shadowRoot?.querySelector('input[type="search"]')
      input?.focus()
    })
    window.addEventListener('resize', onScrollOrResize)
    window.addEventListener('scroll', onScrollOrResize, true)
  } else {
    detachListener()
    window.removeEventListener('resize', onScrollOrResize)
    window.removeEventListener('scroll', onScrollOrResize, true)
  }
})

onMounted(detectDark)

onBeforeUnmount(() => {
  detachListener()
  window.removeEventListener('resize', onScrollOrResize)
  window.removeEventListener('scroll', onScrollOrResize, true)
})
</script>

<style scoped>
.emoji-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1999;
}

.emoji-popover {
  position: fixed;
  z-index: 2000;
  width: 352px;
  display: flex;
  flex-direction: column;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 10px;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.18);
  overflow: hidden;
}

@media (prefers-color-scheme: dark) {
  .emoji-popover { box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5); }
}

.emoji-picker-el {
  width: 100%;
  height: 400px;
  --background: var(--surface-card);
  --border-color: var(--surface-border);
  --input-border-color: var(--surface-border);
  --input-background-color: var(--surface-inset);
  --button-hover-background: var(--surface-inset);
  --button-active-background: var(--surface-inset);
  --indicator-color: var(--primary-color, var(--text-color));
  --category-font-color: var(--text-color-muted);
  --input-font-color: var(--text-color);
  --input-placeholder-color: var(--text-color-muted);
  --num-columns: 8;
}

.emoji-picker-el.dark {
  --background: #2a2a2a;
  --border-color: #444;
  --input-border-color: #555;
  --input-background-color: #1e1e1e;
  --input-font-color: #eee;
  --button-hover-background: #333;
  --button-active-background: #444;
  --category-font-color: #aaa;
}

.emoji-status {
  padding: 6px 10px;
  font-size: 11px;
  color: var(--text-color-muted);
  border-bottom: 1px solid var(--surface-border);
}

.emoji-status.error { color: var(--danger-color); }

</style>

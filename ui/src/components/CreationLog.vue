<template>
  <div class="creation-log">
    <div class="creation-log-header">
      <span class="creation-log-title">{{ streaming ? 'Creating...' : 'Creation Log' }}</span>
    </div>
    <div ref="termEl" class="creation-log-terminal"></div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { Terminal } from '@xterm/xterm'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'

const props = defineProps<{
  hostname: string
  streaming: boolean
}>()

const emit = defineEmits<{
  (e: 'done'): void
  (e: 'fail', message: string): void
}>()

const termEl = ref<HTMLElement | null>(null)
let terminal: Terminal | null = null
let abortController: AbortController | null = null

onMounted(async () => {
  if (!termEl.value) return

  const isDark = window.matchMedia('(prefers-color-scheme: dark)').matches
  terminal = new Terminal({
    fontSize: 13,
    fontFamily: "'JetBrains Mono', Consolas, 'Liberation Mono', Menlo, Courier, monospace",
    convertEol: true,
    disableStdin: true,
    cursorBlink: false,
    scrollback: 5000,
    theme: isDark
      ? { background: '#1e1e1e', foreground: '#d4d4d4', cursor: '#d4d4d4' }
      : { background: '#ffffff', foreground: '#1e1e1e', cursor: '#1e1e1e' },
  })
  terminal.loadAddon(new WebLinksAddon())
  terminal.open(termEl.value)

  // Resize to fill container
  const cols = Math.max(40, Math.floor(termEl.value.clientWidth / 8))
  terminal.resize(cols, 16)

  if (props.streaming) {
    await streamCreation()
  } else {
    await loadStoredLog()
  }
})

async function streamCreation() {
  if (!terminal) return
  abortController = new AbortController()
  try {
    const resp = await fetch('/creating/stream?hostname=' + encodeURIComponent(props.hostname), {
      signal: abortController.signal,
    })
    if (!resp.ok || !resp.body) {
      terminal.write('Error connecting to creation stream\r\n')
      return
    }
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    let curEvent = ''
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      const lines = buf.split('\n')
      buf = lines.pop() || ''
      for (const line of lines) {
        if (line.startsWith('event:')) {
          curEvent = line.slice(6).trim()
          continue
        }
        if (line.startsWith('data:')) {
          const data = line.slice(5).trim()
          if (curEvent === 'done') {
            emit('done')
            curEvent = ''
            continue
          }
          if (curEvent === 'fail') {
            emit('fail', data)
            curEvent = ''
            continue
          }
          if (data) {
            try {
              terminal.write(base64ToBytes(data))
            } catch { /* skip bad base64 */ }
          }
        } else if (line === '') {
          curEvent = ''
        }
      }
    }
  } catch (err: any) {
    if (err.name !== 'AbortError') {
      terminal.write('\r\nStream ended\r\n')
    }
  }
}

async function loadStoredLog() {
  if (!terminal) return
  try {
    const resp = await fetch('/box/creation-log?hostname=' + encodeURIComponent(props.hostname))
    if (!resp.ok) {
      terminal.write('Failed to load creation log\r\n')
      return
    }
    // The endpoint returns raw terminal output (application/octet-stream)
    const text = await resp.text()
    if (text) {
      terminal.write(text)
    }
  } catch {
    terminal.write('Failed to load creation log\r\n')
  }
}

function base64ToBytes(b64: string): Uint8Array {
  // Uint8Array.fromBase64 is new; fall back to atob + charCodeAt.
  const u8 = Uint8Array as unknown as { fromBase64?: (s: string) => Uint8Array }
  if (u8.fromBase64) return u8.fromBase64(b64)
  const bin = atob(b64)
  return Uint8Array.from(bin, c => c.charCodeAt(0))
}

onBeforeUnmount(() => {
  abortController?.abort()
  terminal?.dispose()
})
</script>

<style scoped>
.creation-log {
  margin-top: 8px;
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  overflow: hidden;
}

.creation-log-header {
  padding: 4px 8px;
  background: var(--surface-subtle);
  font-size: 11px;
  color: var(--text-color-muted);
}

.creation-log-title {
  font-weight: 500;
}

.creation-log-terminal {
  height: 280px;
  overflow: hidden;
}

.creation-log-terminal :deep(.xterm) {
  height: 100%;
  padding: 4px;
  background: var(--surface-ground, #ffffff);
}

.creation-log-terminal :deep(.xterm-viewport) {
  background-color: transparent !important;
}

@media (prefers-color-scheme: dark) {
  .creation-log-terminal :deep(.xterm) {
    background: #1e1e1e;
  }
}
</style>

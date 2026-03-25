<template>
  <div class="shell-container">
    <div class="shell-header">
      <span class="shell-title">{{ shellHost }} Lobby</span>
    </div>

    <!-- Mobile control bar -->
    <div class="control-bar">
      <button
        class="ctrl-btn mod"
        :class="{ active: ctrlActive }"
        @click.prevent="toggleCtrl"
      >Ctrl</button>
      <button
        class="ctrl-btn mod"
        :class="{ active: altActive }"
        @click.prevent="toggleAlt"
      >Alt</button>
      <span class="sep"></span>
      <button class="ctrl-btn" @click.prevent="sendKey('Escape')">Esc</button>
      <button class="ctrl-btn" @click.prevent="sendKey('Tab')">Tab</button>
      <span class="sep"></span>
      <button class="ctrl-btn" @click.prevent="sendCtrl('c')">^C</button>
      <button class="ctrl-btn" @click.prevent="sendCtrl('d')">^D</button>
      <button class="ctrl-btn" @click.prevent="sendCtrl('z')">^Z</button>
      <span class="sep"></span>
      <span class="arrow-group">
        <button class="ctrl-btn sm" @click.prevent="sendKey('ArrowUp')">↑</button>
        <button class="ctrl-btn sm" @click.prevent="sendKey('ArrowDown')">↓</button>
        <button class="ctrl-btn sm" @click.prevent="sendKey('ArrowLeft')">←</button>
        <button class="ctrl-btn sm" @click.prevent="sendKey('ArrowRight')">→</button>
      </span>
      <span class="sep"></span>
      <button class="ctrl-btn sm" @click.prevent="sendKey('Home')">Home</button>
      <button class="ctrl-btn sm" @click.prevent="sendKey('End')">End</button>
      <button class="ctrl-btn sm" @click.prevent="sendKey('Delete')">Del</button>
      <span class="sep"></span>
      <button class="ctrl-btn" @click.prevent="pasteClipboard">📋</button>
    </div>

    <div ref="terminalEl" class="terminal-wrap">
      <div class="status-overlay" :class="statusType" v-show="statusVisible">
        <span class="status-dot"></span>
        <span>{{ statusText }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { Terminal } from '@xterm/xterm'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { FitAddon } from '@xterm/addon-fit'
import { fetchDashboard } from '../api/client'
import '@xterm/xterm/css/xterm.css'

const terminalEl = ref<HTMLElement | null>(null)
let destroyed = false
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
let reconnectDelay = 2000
let themeMediaQuery: MediaQueryList | null = null
let themeChangeHandler: (() => void) | null = null

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let connected = false

const shellHost = ref('Lobby')
const statusType = ref('connecting')
const statusText = ref('Connecting...')
const statusVisible = ref(true)

const ctrlActive = ref(false)
const altActive = ref(false)

// Theme (follows system preference)

const themes = {
  light: {
    background: '#ffffff',
    foreground: '#24292f',
    cursor: '#0d9488',
    selectionBackground: '#0d948840',
    selectionForeground: '#24292f',
    black: '#24292f', red: '#cf222e', green: '#116329', yellow: '#4d2d00',
    blue: '#0969da', magenta: '#8250df', cyan: '#1b7c83', white: '#6e7681',
    brightBlack: '#656d76', brightRed: '#a40e26', brightGreen: '#1a7f37',
    brightYellow: '#633c01', brightBlue: '#218bff', brightMagenta: '#a475f9',
    brightCyan: '#3192aa', brightWhite: '#8c959f',
  },
  dark: {
    background: '#0d1117',
    foreground: '#e6edf3',
    cursor: '#5eead4',
    selectionBackground: '#5eead44d',
    black: '#484f58', red: '#ff7b72', green: '#3fb950', yellow: '#d29922',
    blue: '#58a6ff', magenta: '#bc8cff', cyan: '#39c5cf', white: '#b1bac4',
    brightBlack: '#6e7681', brightRed: '#ffa198', brightGreen: '#56d364',
    brightYellow: '#e3b341', brightBlue: '#79c0ff', brightMagenta: '#d2a8ff',
    brightCyan: '#56d4dd', brightWhite: '#f0f6fc',
  },
}

function effectiveTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function applyTermTheme() {
  if (!terminal || !terminalEl.value) return
  const t = effectiveTheme()
  terminal.options.theme = themes[t]
  terminalEl.value.style.setProperty('--xterm-bg', themes[t].background)
}

// Modifier helpers
function toggleCtrl() {
  ctrlActive.value = !ctrlActive.value
  terminal?.focus()
}
function toggleAlt() {
  altActive.value = !altActive.value
  terminal?.focus()
}
function resetModifiers() {
  ctrlActive.value = false
  altActive.value = false
}

// Key sequences
const keySeqs: Record<string, string> = {
  Escape: '\x1b', Tab: '\t',
  ArrowUp: '\x1b[A', ArrowDown: '\x1b[B',
  ArrowRight: '\x1b[C', ArrowLeft: '\x1b[D',
  Home: '\x1b[H', End: '\x1b[F',
  PageUp: '\x1b[5~', PageDown: '\x1b[6~',
  Delete: '\x1b[3~',
}

function sendKey(key: string) {
  let seq = keySeqs[key] || ''
  if (seq && (ctrlActive.value || altActive.value)) {
    const mod = 1 + (altActive.value ? 2 : 0) + (ctrlActive.value ? 4 : 0)
    const codes: Record<string, string> = { ArrowUp: 'A', ArrowDown: 'B', ArrowRight: 'C', ArrowLeft: 'D' }
    if (codes[key]) seq = `\x1b[1;${mod}${codes[key]}`
  }
  wsSend(seq)
  resetModifiers()
  terminal?.focus()
}

function sendCtrl(ch: string) {
  wsSend(String.fromCharCode(ch.toLowerCase().charCodeAt(0) - 96))
  resetModifiers()
  terminal?.focus()
}

async function pasteClipboard() {
  try {
    const text = await navigator.clipboard.readText()
    if (text) wsSend(text)
  } catch { /* ignore */ }
  terminal?.focus()
}

function wsSend(data: string) {
  if (connected && ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'input', data }))
  }
}

function sendResize() {
  if (terminal && connected && ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }))
  }
}

function connect() {
  statusType.value = 'connecting'
  statusText.value = 'Connecting to shell...'
  statusVisible.value = true

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  ws = new WebSocket(`${proto}//${location.host}/shell/ws`)

  ws.onopen = () => {
    connected = true
    reconnectDelay = 2000
    statusType.value = 'connected'
    statusText.value = 'Connected'
    setTimeout(() => { statusVisible.value = false }, 2000)
    ws!.send(JSON.stringify({ type: 'init', cols: terminal!.cols, rows: terminal!.rows }))
  }

  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data)
      if (msg.type === 'output' && msg.data) terminal?.write(msg.data)
    } catch { /* ignore */ }
  }

  ws.onerror = () => {
    statusType.value = 'error'
    statusText.value = 'Connection error'
    statusVisible.value = true
  }

  ws.onclose = () => {
    connected = false
    if (destroyed) return
    statusType.value = 'error'
    statusText.value = 'Connection closed. Reconnecting...'
    statusVisible.value = true
    reconnectTimer = setTimeout(connect, reconnectDelay)
    reconnectDelay = Math.min(reconnectDelay * 1.5, 30000)
  }
}

let resizeObserver: ResizeObserver | null = null

onMounted(async () => {
  await nextTick()
  if (!terminalEl.value) return

  terminal = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', Consolas, 'Liberation Mono', Menlo, Courier, monospace",
    theme: themes[effectiveTheme()],
  })

  fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)
  terminal.loadAddon(new WebLinksAddon())

  // Intercept keys when modifiers active
  terminal.attachCustomKeyEventHandler((event) => {
    if (event.type !== 'keydown') return true
    if (!ctrlActive.value && !altActive.value) return true
    if (event.key.length === 1 && /[a-z]/i.test(event.key)) {
      event.preventDefault()
      event.stopPropagation()
      let data: string
      if (ctrlActive.value) {
        data = String.fromCharCode(event.key.toLowerCase().charCodeAt(0) - 96)
      } else {
        data = '\x1b' + event.key
      }
      wsSend(data)
      resetModifiers()
      return false
    }
    return true
  })

  terminal.onData((data) => wsSend(data))

  terminal.open(terminalEl.value)
  applyTermTheme()
  fitAddon.fit()
  connect()
  terminal.focus()

  // Fetch host name
  fetchDashboard().then(data => {
    if (data.replHost) shellHost.value = data.replHost
  }).catch(() => {})

  // System theme change listener
  themeMediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
  themeChangeHandler = () => applyTermTheme()
  themeMediaQuery.addEventListener('change', themeChangeHandler)

  // Use ResizeObserver for responsive fitting
  resizeObserver = new ResizeObserver(() => {
    fitAddon?.fit()
    sendResize()
  })
  resizeObserver.observe(terminalEl.value)
})

onBeforeUnmount(() => {
  destroyed = true
  if (reconnectTimer) clearTimeout(reconnectTimer)
  if (themeMediaQuery && themeChangeHandler) {
    themeMediaQuery.removeEventListener('change', themeChangeHandler)
  }
  resizeObserver?.disconnect()
  ws?.close()
  terminal?.dispose()
})
</script>

<style scoped>
.shell-container {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 48px - 48px); /* viewport minus topbar minus content padding */
}

.shell-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.shell-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-color);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

/* Mobile control bar */
.control-bar {
  display: none;
  padding: 6px 0;
  gap: 4px;
  flex-wrap: wrap;
  align-items: center;
}

.ctrl-btn {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 6px 10px;
  font-size: 11px;
  font-family: inherit;
  color: var(--text-color);
  cursor: pointer;
  user-select: none;
  -webkit-tap-highlight-color: transparent;
  min-width: 36px;
  text-align: center;
}

.ctrl-btn:active {
  background: var(--surface-hover);
}

.ctrl-btn.mod.active {
  background: var(--text-color);
  color: var(--surface-card);
}

.ctrl-btn.sm {
  padding: 6px 6px;
  min-width: 28px;
}

.sep {
  width: 1px;
  height: 20px;
  background: var(--surface-border);
  margin: 0 2px;
}

.arrow-group {
  display: flex;
  gap: 3px;
}

/* Status overlay - positioned inside terminal so it doesn't cause reflow */
.status-overlay {
  position: absolute;
  top: 8px;
  right: 8px;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 500;
  box-shadow: 0 1px 4px rgba(0,0,0,0.1);
}

.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}

.status-overlay.connecting {
  background: var(--warning-bg);
  color: var(--warning-text);
}
.status-overlay.connecting .status-dot {
  background: var(--warning-color);
  animation: pulse 1.5s ease-in-out infinite alternate;
}

.status-overlay.connected {
  background: var(--success-bg);
  color: var(--success-text);
}
.status-overlay.connected .status-dot {
  background: var(--success-color);
}

.status-overlay.error {
  background: var(--danger-bg);
  color: var(--danger-text);
}
.status-overlay.error .status-dot {
  background: var(--danger-color);
  animation: pulse 1s ease-in-out infinite alternate;
}

@keyframes pulse {
  from { opacity: 0.4; }
  to { opacity: 1; }
}

.terminal-wrap {
  flex: 1;
  position: relative;
  border-radius: 6px;
  overflow: hidden;
  min-height: 0;
}

/* Override xterm.js to fill container */
.terminal-wrap :deep(.xterm) {
  height: 100%;
  padding: 8px;
}
.terminal-wrap :deep(.xterm-viewport) {
  overflow-y: auto !important;
}
.terminal-wrap :deep(.xterm-screen) {
  height: 100% !important;
}

/* Force xterm viewport background to match theme — xterm 6 doesn't always apply it */
.terminal-wrap :deep(.xterm-viewport) {
  background-color: var(--xterm-bg, #fafafa) !important;
}

@media (max-width: 768px) {
  .shell-container {
    height: calc(100vh - 48px - 24px);
  }
  .shell-header {
    display: none;
  }
  .control-bar {
    display: flex;
  }
}
</style>

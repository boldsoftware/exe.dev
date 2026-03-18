<template>
  <div class="agent-view" :style="{ left: sidebarOffset + 'px' }">
    <div class="agent-layout">
      <!-- Conversation sidebar -->
      <aside class="conv-panel" :class="{ open: convPanelOpen }">
        <div class="conv-header">
          <span class="conv-title">Conversations</span>
          <button class="conv-new-btn" @click="startNewConversation" title="New conversation">
            <i class="pi pi-plus"></i>
          </button>
        </div>
        <div class="conv-list">
          <div
            v-for="conv in conversations"
            :key="conv.id"
            class="conv-item"
            :class="{ active: conv.id === activeConversationId }"
            @click="selectConversation(conv.id)"
          >
            <input
              v-if="editingConversationId === conv.id"
              class="conv-rename-input"
              v-model="editingTitle"
              @keydown.enter="finishRename(conv.id)"
              @keydown.escape="cancelRename"
              @blur="finishRename(conv.id)"
              @click.stop
              ref="renameInput"
            />
            <span v-else class="conv-item-title" @dblclick.stop="startRename(conv.id, conv.title)">{{ conv.title || 'New conversation' }}</span>
            <button
              class="conv-delete-btn"
              :class="{ confirming: confirmingDeleteId === conv.id }"
              @click.stop="confirmDelete(conv.id)"
              :title="confirmingDeleteId === conv.id ? 'Click to confirm delete' : 'Delete conversation'"
            >
              <i :class="confirmingDeleteId === conv.id ? 'pi pi-check' : 'pi pi-trash'"></i>
            </button>
          </div>
          <div v-if="conversations.length === 0" class="conv-empty">
            No conversations yet
          </div>
        </div>
        <div class="conv-footer" v-if="chatConfig?.provider">
          <div class="conv-config-pill">
            <span class="conv-provider">{{ chatConfig.provider }}</span>
            <span class="conv-model">{{ chatConfig.model }}</span>
          </div>
        </div>
      </aside>

      <!-- Mobile overlay -->
      <div class="conv-mask" :class="{ active: convPanelOpen }" @click="convPanelOpen = false"></div>

      <!-- Chat area -->
      <div class="chat-area">
        <div class="chat-topbar">
          <button class="conv-toggle-btn" @click="convPanelOpen = !convPanelOpen">
            <i class="pi pi-list"></i>
          </button>
          <span class="chat-topbar-title">AI Agent</span>
        </div>

        <!-- Not configured message -->
        <div v-if="notConfigured" class="chat-not-configured">
          <div class="not-configured-card">
            <i class="pi pi-microchip-ai not-configured-icon"></i>
            <h3>AI Agent not configured</h3>
            <p>To enable the AI agent, start the server with an AI provider:</p>
            <pre><code>exe-ops-server \
  --ai-provider=ollama \
  --ai-model=llama3 \
  --ai-base-url=http://localhost:11434</code></pre>
            <p class="not-configured-hint">Supported providers: <strong>anthropic</strong>, <strong>openai</strong>, <strong>openai-compat</strong>, <strong>ollama</strong></p>
            <p class="not-configured-hint">You can also use environment variables: <code>EXE_OPS_AI_PROVIDER</code>, <code>EXE_OPS_AI_API_KEY</code>, <code>EXE_OPS_AI_MODEL</code>, <code>EXE_OPS_AI_BASE_URL</code></p>
          </div>
        </div>

        <!-- Messages -->
        <div v-else class="chat-messages" ref="messagesContainer">
          <div v-if="messages.length === 0 && !streaming" class="chat-welcome">
            <i class="pi pi-microchip-ai welcome-icon"></i>
            <p>Ask me anything about your fleet.</p>
          </div>
          <div v-for="msg in messages" :key="msg.id" class="chat-msg" :class="'msg-' + msg.role">
            <div class="msg-header">
              <span class="msg-label">{{ msg.role === 'user' ? 'You' : 'Agent' }}</span>
              <span class="msg-time">{{ formatTime(msg.created_at) }}</span>
              <button class="msg-copy-btn" @click="copyMessage(msg.content)" title="Copy message">
                <i class="pi pi-copy"></i>
              </button>
            </div>
            <div v-if="msg.role === 'assistant'" class="msg-content" v-html="renderMarkdown(msg.content)"></div>
            <div v-else class="msg-content">{{ msg.content }}</div>
          </div>
          <!-- Streaming response -->
          <div v-if="streaming" class="chat-msg msg-assistant">
            <div class="msg-label">Agent</div>
            <div class="msg-content" v-html="renderMarkdown(streamBuffer)"></div>
            <div v-if="streamBuffer === ''" class="msg-thinking">
              <span class="dot"></span><span class="dot"></span><span class="dot"></span>
            </div>
          </div>
        </div>

        <!-- Input -->
        <div v-if="!notConfigured" class="chat-input-area">
          <textarea
            ref="inputEl"
            v-model="inputText"
            @keydown.enter.exact.prevent="sendMessage"
            placeholder="Ask about your fleet..."
            rows="1"
            :disabled="streaming"
            class="chat-input"
          ></textarea>
          <button class="chat-send-btn" @click="sendMessage" :disabled="streaming || !inputText.trim()" title="Send">
            <i class="pi pi-send"></i>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { marked } from 'marked'
import {
  fetchConversations,
  fetchChatMessages,
  fetchChatConfig,
  deleteConversation,
  renameConversation,
  sendChatMessage,
  type Conversation,
  type ChatMessage,
  type ChatConfig,
} from '../api/client'

// Track sidebar width so the fixed agent view doesn't overlap it.
const sidebarOffset = ref(0)
let resizeObserver: ResizeObserver | null = null

function updateSidebarOffset() {
  const sidebar = document.querySelector('.layout-sidebar') as HTMLElement | null
  if (sidebar) {
    sidebarOffset.value = sidebar.offsetWidth
  }
}

const conversations = ref<Conversation[]>([])
const activeConversationId = ref<string | null>(null)
const confirmingDeleteId = ref<string | null>(null)
let confirmingDeleteTimer: ReturnType<typeof setTimeout> | null = null
const editingConversationId = ref<string | null>(null)
const editingTitle = ref('')
const renameInput = ref<HTMLInputElement | null>(null)
const messages = ref<ChatMessage[]>([])
const inputText = ref('')
const streaming = ref(false)
const streamBuffer = ref('')
const notConfigured = ref(false)
const chatConfig = ref<ChatConfig | null>(null)
const convPanelOpen = ref(false)
const messagesContainer = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)
let abortController: AbortController | null = null

// Configure marked for safe rendering
marked.setOptions({
  breaks: true,
  gfm: true,
})

const renderer = new marked.Renderer()

// Custom code block renderer with language label and copy button.
renderer.code = function ({ text, lang }: { text: string; lang?: string }) {
  const langLabel = lang ? `<span class="code-lang">${lang}</span>` : ''
  const escaped = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return `<div class="code-block">\
<div class="code-header">${langLabel}<button class="code-copy-btn" onclick="navigator.clipboard.writeText(this.closest('.code-block').querySelector('code').textContent).then(()=>{this.textContent='Copied!';setTimeout(()=>this.textContent='Copy',1500)})">Copy</button></div>\
<pre><code class="language-${lang || ''}">${escaped}</code></pre></div>`
}

function renderMarkdown(text: string): string {
  if (!text) return ''
  return marked.parse(text, { renderer }) as string
}

async function loadConversations() {
  try {
    conversations.value = await fetchConversations()
  } catch {
    // silently fail
  }
}

async function selectConversation(id: string) {
  activeConversationId.value = id
  convPanelOpen.value = false
  try {
    messages.value = await fetchChatMessages(id)
    await nextTick()
    scrollToBottom()
  } catch {
    messages.value = []
  }
}

function startNewConversation() {
  activeConversationId.value = null
  messages.value = []
  convPanelOpen.value = false
  nextTick(() => inputEl.value?.focus())
}

function confirmDelete(id: string) {
  if (confirmingDeleteId.value === id) {
    // Second click — actually delete.
    confirmingDeleteId.value = null
    if (confirmingDeleteTimer) clearTimeout(confirmingDeleteTimer)
    deleteConversation(id).then(() => {
      conversations.value = conversations.value.filter(c => c.id !== id)
      if (activeConversationId.value === id) {
        activeConversationId.value = null
        messages.value = []
      }
    }).catch(() => {})
    return
  }
  // First click — enter confirm state, auto-reset after 3s.
  confirmingDeleteId.value = id
  if (confirmingDeleteTimer) clearTimeout(confirmingDeleteTimer)
  confirmingDeleteTimer = setTimeout(() => { confirmingDeleteId.value = null }, 3000)
}

function startRename(id: string, title: string) {
  editingConversationId.value = id
  editingTitle.value = title || ''
  nextTick(() => {
    const input = document.querySelector('.conv-rename-input') as HTMLInputElement | null
    input?.focus()
    input?.select()
  })
}

function cancelRename() {
  editingConversationId.value = null
  editingTitle.value = ''
}

async function finishRename(id: string) {
  const title = editingTitle.value.trim()
  editingConversationId.value = null
  if (!title) return
  try {
    await renameConversation(id, title)
    const conv = conversations.value.find(c => c.id === id)
    if (conv) conv.title = title
  } catch {
    // silently fail
  }
}

function formatTime(dateStr: string): string {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  const now = new Date()
  const isToday = d.toDateString() === now.toDateString()
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  if (isToday) return time
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) + ' ' + time
}

function copyMessage(content: string) {
  navigator.clipboard.writeText(content).catch(() => {})
}

function scrollToBottom() {
  if (messagesContainer.value) {
    messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight
  }
}

async function sendMessage() {
  const text = inputText.value.trim()
  if (!text || streaming.value) return

  inputText.value = ''
  streaming.value = true
  streamBuffer.value = ''
  notConfigured.value = false

  // Add user message to local display immediately
  const tempId = Date.now()
  messages.value.push({
    id: tempId,
    conversation_id: activeConversationId.value || '',
    role: 'user',
    content: text,
    created_at: new Date().toISOString(),
  })
  await nextTick()
  scrollToBottom()

  abortController = sendChatMessage(text, activeConversationId.value, {
    onConversation(id: string) {
      activeConversationId.value = id
      // Update conversation in list
      loadConversations()
    },
    onDelta(delta: string) {
      streamBuffer.value += delta
      nextTick(() => scrollToBottom())
    },
    onTitle(title: string) {
      const conv = conversations.value.find(c => c.id === activeConversationId.value)
      if (conv) conv.title = title
      // Always reload to ensure the list is in sync.
      loadConversations()
    },
    onDone() {
      if (streamBuffer.value) {
        messages.value.push({
          id: Date.now(),
          conversation_id: activeConversationId.value || '',
          role: 'assistant',
          content: streamBuffer.value,
          created_at: new Date().toISOString(),
        })
      }
      streamBuffer.value = ''
      streaming.value = false
      nextTick(() => {
        scrollToBottom()
        inputEl.value?.focus()
      })
    },
    onError(error: string) {
      streaming.value = false
      streamBuffer.value = ''
      if (error.includes('not configured') || error.includes('501')) {
        notConfigured.value = true
      } else {
        messages.value.push({
          id: Date.now(),
          conversation_id: activeConversationId.value || '',
          role: 'assistant',
          content: `Error: ${error}`,
          created_at: new Date().toISOString(),
        })
      }
      nextTick(() => scrollToBottom())
    },
  })
}

// Auto-resize textarea
watch(inputText, () => {
  nextTick(() => {
    if (inputEl.value) {
      inputEl.value.style.height = 'auto'
      inputEl.value.style.height = Math.min(inputEl.value.scrollHeight, 160) + 'px'
    }
  })
})

onMounted(async () => {
  updateSidebarOffset()
  const sidebar = document.querySelector('.layout-sidebar') as HTMLElement | null
  if (sidebar) {
    resizeObserver = new ResizeObserver(() => updateSidebarOffset())
    resizeObserver.observe(sidebar)
  }
  await loadConversations()
  fetchChatConfig().then(c => { chatConfig.value = c }).catch(() => {})
  nextTick(() => inputEl.value?.focus())
})

onUnmounted(() => {
  resizeObserver?.disconnect()
})
</script>

<style scoped>
.agent-view {
  position: fixed;
  top: 0;
  right: 0;
  /* left is set dynamically via :style to match sidebar width */
  height: 100vh;
  height: 100dvh;
  display: flex;
  flex-direction: column;
  background: var(--surface-section);
  z-index: 10;
}

.agent-layout {
  flex: 1;
  display: flex;
  min-height: 0;
}

/* Conversation panel */
.conv-panel {
  width: 260px;
  flex-shrink: 0;
  background: var(--surface-card);
  border-right: 1px solid var(--surface-border);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.conv-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  padding: 0 1rem;
  border-bottom: 1px solid var(--surface-border);
  flex-shrink: 0;
}

.conv-title {
  font-size: 0.8rem;
  font-weight: 600;
  color: var(--text-color-secondary);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.conv-new-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 4px;
  border: 1px solid var(--surface-border);
  background: transparent;
  color: var(--text-color-secondary);
  cursor: pointer;
  transition: all 0.15s;
}

.conv-new-btn:hover {
  color: var(--primary-color);
  border-color: var(--primary-color);
}

.conv-list {
  flex: 1;
  overflow-y: auto;
  padding: 0.5rem;
}

.conv-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
  cursor: pointer;
  transition: background 0.15s;
  margin-bottom: 2px;
}

.conv-item:hover {
  background: var(--surface-hover);
}

.conv-item.active {
  background: var(--surface-overlay);
  border: 1px solid var(--surface-border);
}

.conv-item-title {
  flex: 1;
  font-size: 0.8rem;
  color: var(--text-color);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.conv-delete-btn {
  display: none;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 4px;
  background: none;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 0.7rem;
  flex-shrink: 0;
}

.conv-item:hover .conv-delete-btn {
  display: flex;
}

.conv-delete-btn:hover {
  color: var(--red-400);
}

.conv-delete-btn.confirming {
  display: flex;
  color: var(--red-400);
}

.conv-empty {
  padding: 1rem;
  text-align: center;
  color: var(--text-color-muted);
  font-size: 0.8rem;
}

.conv-footer {
  /* Match .sidebar-footer */
  padding: 0.5rem;
  border-top: 1px solid var(--surface-border);
  flex-shrink: 0;
}

.conv-config-pill {
  /* Match .theme-toggle container */
  display: flex;
  align-items: center;
  gap: 0.4rem;
  background: var(--surface-ground);
  border-radius: 6px;
  /* Match .theme-toggle padding (3px) + .theme-btn padding (0.35rem) */
  padding: calc(3px + 0.35rem) 0.5rem;
  /* Match .theme-btn font-size so line-height matches */
  font-size: 0.8rem;
  overflow: hidden;
}

.conv-provider {
  font-size: 0.65rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  color: var(--text-color-muted);
  flex-shrink: 0;
}

.conv-model {
  font-size: 0.65rem;
  font-family: 'JetBrains Mono', monospace;
  color: var(--text-color-muted);
  opacity: 0.7;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.conv-rename-input {
  flex: 1;
  min-width: 0;
  font-family: inherit;
  font-size: 0.8rem;
  color: var(--text-color);
  background: var(--surface-ground);
  border: 1px solid var(--primary-color);
  border-radius: 3px;
  padding: 0.15rem 0.4rem;
  outline: none;
}

.conv-mask {
  display: none;
}

/* Chat area */
.chat-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.chat-topbar {
  display: none;
}

.chat-messages {
  flex: 1;
  overflow-y: auto;
  padding: 1.5rem;
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.chat-welcome {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  color: var(--text-color-muted);
}

.welcome-icon {
  font-size: 2.5rem;
  color: var(--primary-color);
  opacity: 0.5;
}

.chat-welcome p {
  font-size: 0.9rem;
}

/* Messages */
.chat-msg {
  width: 100%;
  margin: 0 auto;
}

.msg-header {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.25rem;
}

.msg-label {
  font-size: 0.7rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-color-muted);
}

.msg-user .msg-label {
  color: var(--primary-color);
}

.msg-time {
  font-size: 0.65rem;
  color: var(--text-color-muted);
  opacity: 0.7;
}

.msg-copy-btn {
  display: none;
  align-items: center;
  justify-content: center;
  margin-left: auto;
  border: none;
  background: none;
  color: var(--text-color-muted);
  font-size: 0.7rem;
  cursor: pointer;
  padding: 0.1rem 0.3rem;
  border-radius: 3px;
  transition: color 0.15s;
}

.chat-msg:hover .msg-copy-btn {
  display: flex;
}

.msg-copy-btn:hover {
  color: var(--primary-color);
}

.msg-content {
  font-size: 0.85rem;
  line-height: 1.7;
  color: var(--text-color);
}

.msg-user .msg-content {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 0.75rem 1rem;
}

/* Markdown styling */
.msg-assistant .msg-content :deep(p) {
  margin-bottom: 0.75rem;
}

.msg-assistant .msg-content :deep(p:last-child) {
  margin-bottom: 0;
}

/* Code blocks with header */
.msg-assistant .msg-content :deep(.code-block) {
  margin: 0.75rem 0;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  overflow: hidden;
}

.msg-assistant .msg-content :deep(.code-header) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.35rem 0.75rem;
  background: var(--surface-overlay);
  border-bottom: 1px solid var(--surface-border);
  font-size: 0.7rem;
}

.msg-assistant .msg-content :deep(.code-lang) {
  color: var(--text-color-secondary);
  font-family: 'JetBrains Mono', monospace;
  text-transform: lowercase;
}

.msg-assistant .msg-content :deep(.code-copy-btn) {
  border: none;
  background: none;
  color: var(--text-color-muted);
  font-size: 0.7rem;
  font-family: inherit;
  cursor: pointer;
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
  transition: color 0.15s, background 0.15s;
}

.msg-assistant .msg-content :deep(.code-copy-btn:hover) {
  color: var(--text-color);
  background: var(--surface-card);
}

.msg-assistant .msg-content :deep(.code-block pre) {
  background: var(--surface-card);
  padding: 0.75rem 1rem;
  overflow-x: auto;
  margin: 0;
  border: none;
  border-radius: 0;
  font-size: 0.8rem;
  line-height: 1.5;
}

/* Standalone pre (fallback if no .code-block wrapper) */
.msg-assistant .msg-content :deep(pre:not(.code-block pre)) {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 0.75rem 1rem;
  overflow-x: auto;
  margin: 0.75rem 0;
  font-size: 0.8rem;
  line-height: 1.5;
}

.msg-assistant .msg-content :deep(code) {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
}

.msg-assistant .msg-content :deep(:not(pre) > code) {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 3px;
  padding: 0.15rem 0.35rem;
  font-size: 0.78em;
}

.msg-assistant .msg-content :deep(ul),
.msg-assistant .msg-content :deep(ol) {
  padding-left: 1.5rem;
  margin-bottom: 0.75rem;
}

.msg-assistant .msg-content :deep(li) {
  margin-bottom: 0.25rem;
}

/* Tables */
.msg-assistant .msg-content :deep(table) {
  border-collapse: collapse;
  width: 100%;
  margin: 0.75rem 0;
  font-size: 0.8rem;
  font-family: 'JetBrains Mono', monospace;
  display: block;
  overflow-x: auto;
}

.msg-assistant .msg-content :deep(th),
.msg-assistant .msg-content :deep(td) {
  border: 1px solid var(--surface-border);
  padding: 0.4rem 0.65rem;
  text-align: left;
  white-space: nowrap;
}

.msg-assistant .msg-content :deep(th) {
  background: var(--surface-overlay);
  font-weight: 600;
  color: var(--text-color);
}

.msg-assistant .msg-content :deep(tr:nth-child(even)) {
  background: var(--surface-card);
}

.msg-assistant .msg-content :deep(tr:hover) {
  background: var(--surface-hover);
}

/* Blockquotes */
.msg-assistant .msg-content :deep(blockquote) {
  border-left: 3px solid var(--primary-color);
  padding: 0.5rem 1rem;
  margin: 0.75rem 0;
  color: var(--text-color-secondary);
  background: var(--primary-50);
  border-radius: 0 4px 4px 0;
}

.msg-assistant .msg-content :deep(blockquote p) {
  margin-bottom: 0;
}

/* Horizontal rules */
.msg-assistant .msg-content :deep(hr) {
  border: none;
  border-top: 1px solid var(--surface-border);
  margin: 1rem 0;
}

.msg-assistant .msg-content :deep(strong) {
  font-weight: 600;
  color: var(--text-color);
}

.msg-assistant .msg-content :deep(h1),
.msg-assistant .msg-content :deep(h2),
.msg-assistant .msg-content :deep(h3) {
  margin: 1rem 0 0.5rem;
  font-weight: 600;
}

.msg-assistant .msg-content :deep(h1) { font-size: 1.1rem; }
.msg-assistant .msg-content :deep(h2) { font-size: 1rem; }
.msg-assistant .msg-content :deep(h3) { font-size: 0.9rem; }

/* Thinking dots */
.msg-thinking {
  display: flex;
  gap: 4px;
  padding: 0.25rem 0;
}

.msg-thinking .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--text-color-muted);
  animation: thinking 1.2s infinite;
}

.msg-thinking .dot:nth-child(2) { animation-delay: 0.2s; }
.msg-thinking .dot:nth-child(3) { animation-delay: 0.4s; }

@keyframes thinking {
  0%, 80%, 100% { opacity: 0.3; }
  40% { opacity: 1; }
}

/* Not configured */
.chat-not-configured {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2rem;
}

.not-configured-card {
  max-width: 560px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  padding: 2rem;
  text-align: center;
}

.not-configured-icon {
  font-size: 2.5rem;
  color: var(--text-color-muted);
  margin-bottom: 1rem;
}

.not-configured-card h3 {
  font-size: 1.1rem;
  margin-bottom: 0.75rem;
  color: var(--text-color);
}

.not-configured-card p {
  font-size: 0.85rem;
  color: var(--text-color-secondary);
  margin-bottom: 0.75rem;
}

.not-configured-card pre {
  background: var(--surface-ground);
  border: 1px solid var(--surface-border);
  border-radius: 4px;
  padding: 0.75rem 1rem;
  text-align: left;
  font-size: 0.8rem;
  overflow-x: auto;
  margin-bottom: 1rem;
}

.not-configured-card code {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
}

.not-configured-hint {
  font-size: 0.75rem;
  color: var(--text-color-muted);
}

.not-configured-hint code {
  background: var(--surface-ground);
  padding: 0.1rem 0.3rem;
  border-radius: 3px;
}

/* Input area */
.chat-input-area {
  padding: 0.75rem 1.5rem 1rem;
  display: flex;
  gap: 0.5rem;
  align-items: flex-end;
  max-width: none;
  width: 100%;
  margin: 0 auto;
}

.chat-input {
  flex: 1;
  resize: none;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 0.6rem 0.75rem;
  font-family: inherit;
  font-size: 0.85rem;
  line-height: 1.5;
  color: var(--text-color);
  background: var(--surface-card);
  outline: none;
  transition: border-color 0.15s;
  min-height: 38px;
  max-height: 160px;
}

.chat-input:focus {
  border-color: var(--primary-color);
}

.chat-input::placeholder {
  color: var(--text-color-muted);
}

.chat-send-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 38px;
  height: 38px;
  border-radius: 6px;
  border: 1px solid var(--surface-border);
  background: var(--primary-color);
  color: var(--primary-color-text);
  cursor: pointer;
  transition: opacity 0.15s;
  flex-shrink: 0;
}

.chat-send-btn:hover:not(:disabled) {
  opacity: 0.9;
}

.chat-send-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

/* Responsive */
@media (max-width: 991px) {
  .agent-view {
    left: 0 !important;
  }

  .conv-panel {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    z-index: 101;
    transform: translateX(-100%);
    transition: transform 0.2s;
  }

  .conv-panel.open {
    transform: translateX(0);
  }

  .conv-mask.active {
    display: block;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 100;
  }

  .chat-topbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.5rem 1rem;
    border-bottom: 1px solid var(--surface-border);
    flex-shrink: 0;
  }

  .conv-toggle-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: none;
    border-radius: 4px;
    background: none;
    color: var(--text-color-secondary);
    cursor: pointer;
  }

  .chat-topbar-title {
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--text-color);
  }

  .chat-messages {
    padding: 1rem;
  }

  .chat-input-area {
    padding: 0.5rem 1rem 0.75rem;
  }
}
</style>

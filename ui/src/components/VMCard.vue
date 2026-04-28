<template>
  <div class="box-row" role="link" tabindex="0" @click="onRowClick" @keydown.enter="onRowClick">
    <div class="box-main">
      <span ref="emojiAnchor" class="emoji-anchor">
        <StatusDot
          :status="box.status"
          :emoji="box.emoji"
          clickable
          @edit="openEmojiPicker"
        />
      </span>
      <EmojiPicker
        :open="emojiOpen"
        :anchor-el="emojiAnchor"
        :current="box.emoji"
        :recents="recentEmojis"
        :saving="emojiSaving"
        :error-msg="emojiError"
        @close="closeEmojiPicker"
        @pick="onEmojiPick"
      />
      <router-link :to="`/vm/${box.name}`" class="box-name" @click.stop>{{ box.name }}</router-link>
      <span v-if="box.totalShareCount > 0" class="share-badge" :title="`Shared with ${box.sharedUserCount} user(s) and ${box.shareLinkCount} link(s)`">
        👥 {{ box.totalShareCount }}
      </span>
      <span v-if="box.isTeamShared" class="team-badge" title="Shared with your team">TEAM</span>
      <span v-if="box.proxyShare === 'public'" class="public-badge">PUBLIC</span>
      <span class="box-tags">
        <span v-for="tag in box.displayTags" :key="tag" class="tag">#{{ tag }}</span>
      </span>
      <div class="box-actions">
        <template v-if="box.status !== 'pending'">
          <a :href="box.proxyURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="HTTPS" @click.stop>
            <i class="pi pi-globe" style="font-size: 12px;"></i>
          </a>
          <a :href="box.terminalURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="Terminal" @click.stop>
            <i class="pi pi-chevron-right" style="font-size: 12px;"></i>
          </a>
          <a v-if="box.shelleyURL" :href="box.shelleyURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="Shelley" @click.stop>
            <CoolS :name="box.name" :size="14" />
          </a>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import type { BoxInfo } from '../api/client'
import StatusDot from './StatusDot.vue'
import CoolS from './CoolS.vue'
import EmojiPicker from './EmojiPicker.vue'
import { useCommand } from '../composables/useCommand'
import { shellQuote } from '../api/client'

const router = useRouter()

const emojiAnchor = ref<HTMLElement | null>(null)
const emojiOpen = ref(false)
const emojiSaving = ref(false)
const emojiError = ref('')
const emojiCmd = useCommand()

const props = defineProps<{
  box: BoxInfo
  recentEmojis?: string[]
}>()

function openEmojiPicker() {
  emojiError.value = ''
  emojiOpen.value = true
}

function closeEmojiPicker() {
  emojiOpen.value = false
}

async function onEmojiPick(emoji: string) {
  if (!emoji || emoji === props.box.emoji) {
    closeEmojiPicker()
    return
  }
  emojiSaving.value = true
  emojiError.value = ''
  const cmd = `emoji ${shellQuote(props.box.name)} ${shellQuote(emoji)}`
  const result = await emojiCmd.execute(cmd)
  emojiSaving.value = false
  if (result.success) {
    closeEmojiPicker()
    emit('action', { type: 'emoji-changed', boxName: props.box.name, extra: emoji })
  } else {
    emojiError.value = result.output || result.error || 'Failed to update emoji'
  }
}

const emit = defineEmits<{
  (e: 'action', action: { type: string; boxName: string; extra?: any }): void
}>()

function onRowClick(event: Event) {
  if ((event.target as HTMLElement).closest('button, a')) return
  router.push(`/vm/${props.box.name}`)
}
</script>

<style scoped>
.box-row {
  background: var(--surface-card);
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.1s;
}

.box-row:hover {
  background: var(--surface-inset);
}

.box-main {
  display: flex;
  align-items: center;
  gap: 12px;
}

.emoji-anchor {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}

.box-name {
  font-weight: 500;
  font-size: 13px;
  color: var(--text-color);
  text-decoration: none;
}

.box-name:hover {
  text-decoration: underline;
  color: var(--primary-color, var(--text-color));
}

.share-badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 2px 6px;
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
  border-radius: 3px;
  font-size: 11px;
}

.team-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 6px;
  background: var(--badge-share-bg);
  color: var(--badge-share-text);
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
}

.public-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 6px;
  background: var(--badge-public-bg);
  color: var(--badge-public-text);
  border-radius: 3px;
  font-size: 11px;
  font-weight: 600;
}

.box-tags {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.tag {
  font-size: 11px;
  color: var(--text-color-muted);
  background: var(--tag-bg);
  padding: 1px 6px;
  border-radius: 3px;
}

.box-actions {
  margin-left: auto;
  display: flex;
  gap: 4px;
  align-items: center;
}

.action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  cursor: pointer;
  color: var(--btn-text);
  text-decoration: none;
  transition: all 0.15s;
  box-sizing: border-box;
}

.action-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
  text-decoration: none;
}

@media (max-width: 768px) {
  .box-actions {
    gap: 2px;
  }
  .box-row {
    padding: 10px 8px;
  }
  .box-main {
    gap: 8px;
    flex-wrap: wrap;
  }
  /* Push tags onto a second row on mobile. Actions stay on the first row via margin-left: auto. */
  .box-tags {
    order: 10;
    flex-basis: 100%;
    margin-left: 36px; /* align under name, past emoji */
  }
}
</style>

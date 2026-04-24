<template>
  <div class="box-details" @click.stop>
    <!-- SSH -->
    <div v-if="box.status !== 'pending' && box.sshCommand" class="detail-row">
      <span class="detail-label">SSH:</span>
      <code class="ssh-cmd">{{ box.sshCommand }}</code>
      <CopyButton :text="box.sshCommand" title="Copy SSH command" />
    </div>

    <!-- Info grid -->
    <div class="detail-grid">
      <div class="detail-item">
        <span class="detail-label">Status:</span>
        <span>{{ box.status }}</span>
      </div>
      <div class="detail-item">
        <span class="detail-label">Region:</span>
        <span>{{ box.region }}</span>
      </div>
      <div class="detail-item">
        <span class="detail-label">Image:</span>
        <span>{{ box.image }}</span>
      </div>
      <div v-if="box.routeKnown" class="detail-item">
        <span class="detail-label">Proxy:</span>
        <span class="proxy-info">
          Port {{ box.proxyPort }}
          <button class="detail-btn" @click="$emit('action', { type: 'set-port', boxName: box.name, extra: box.proxyURL })">Change</button>
          · {{ box.proxyShare }}
          <button v-if="box.proxyShare === 'public'" class="detail-btn" @click="$emit('action', { type: 'set-private', boxName: box.name })">Make Private</button>
          <button v-else class="detail-btn" @click="$emit('action', { type: 'set-public', boxName: box.name })">Make Public</button>
        </span>
      </div>
      <div v-if="box.totalShareCount > 0" class="detail-item">
        <span class="detail-label">Sharing:</span>
        <span>
          <template v-if="box.sharedUserCount > 0">{{ box.sharedUserCount }} user{{ box.sharedUserCount !== 1 ? 's' : '' }}</template>
          <template v-if="box.sharedUserCount > 0 && box.shareLinkCount > 0"> · </template>
          <template v-if="box.shareLinkCount > 0">{{ box.shareLinkCount }} link{{ box.shareLinkCount !== 1 ? 's' : '' }}</template>
        </span>
      </div>
      <div v-if="box.createdAt" class="detail-item">
        <span class="detail-label">Created:</span>
        <span>{{ box.createdAt }}</span>
      </div>
    </div>

    <!-- Tags -->
    <div class="detail-row">
      <span class="detail-label">Tags:</span>
      <span class="tags-row">
        <span v-for="tag in box.displayTags" :key="tag" class="tag tag-removable">
          #{{ tag }}
          <button class="tag-remove" @click="$emit('action', { type: 'remove-tag', boxName: box.name, extra: tag })">&times;</button>
        </span>
        <button class="detail-btn" @click="$emit('action', { type: 'add-tag', boxName: box.name })">Add Tag</button>
      </span>
    </div>

    <!-- Shared emails -->
    <div v-if="box.sharedEmails && box.sharedEmails.length > 0" class="detail-row">
      <span class="detail-label">Shared with:</span>
      <div class="shared-list">
        <div v-for="email in box.sharedEmails" :key="email" class="shared-item">
          <span>{{ email }}</span>
          <button class="remove-btn" @click="$emit('action', { type: 'remove-share', boxName: box.name, extra: email })">&times;</button>
        </div>
      </div>
    </div>

    <!-- Share links -->
    <div v-if="box.shareLinks && box.shareLinks.length > 0" class="detail-row">
      <span class="detail-label">Share links:</span>
      <div class="shared-list">
        <div v-for="link in box.shareLinks" :key="link.token" class="shared-item">
          <code class="share-link-url">{{ link.url }}</code>
          <CopyButton :text="link.url" title="Copy link" />
          <button class="remove-btn" @click="$emit('action', { type: 'remove-share-link', boxName: box.name, extra: link.token })">&times;</button>
        </div>
      </div>
    </div>

    <!-- Action buttons -->
    <div class="detail-actions">
      <a :href="box.proxyURL" class="action-btn-expanded" target="_blank" rel="noopener noreferrer">
        <i class="pi pi-globe"></i> HTTPS
      </a>
      <a :href="box.terminalURL" class="action-btn-expanded" target="_blank" rel="noopener noreferrer">
        <i class="pi pi-chevron-right"></i> Terminal
      </a>
      <button v-if="box.vscodeURL" class="action-btn-expanded" @click="$emit('action', { type: 'open-editor', boxName: box.name, extra: box.vscodeURL })">
        <i class="pi pi-code"></i> Editor
      </button>
      <a v-if="box.shelleyURL" :href="box.shelleyURL" class="action-btn-expanded" target="_blank" rel="noopener noreferrer">
        <CoolS :name="box.name" :size="14" /> Shelley
      </a>
      <button class="action-btn-expanded" @click="$emit('action', { type: 'share', boxName: box.name })">
        <i class="pi pi-share-alt"></i> Share
      </button>
      <button v-if="hasTeam" class="action-btn-expanded" @click="$emit('action', { type: 'share-team', boxName: box.name })">
        <i class="pi pi-users"></i> {{ box.isTeamShared ? 'Unshare Team' : 'Share with Team' }}
      </button>
      <button class="action-btn-expanded" @click="$emit('action', { type: 'share-link', boxName: box.name })">
        <i class="pi pi-link"></i> Share Link
      </button>
      <button class="action-btn-expanded" @click="$emit('action', { type: 'copy', boxName: box.name, extra: box.displayTags })">
        <i class="pi pi-clone"></i> Copy
      </button>
      <button class="action-btn-expanded" @click="$emit('action', { type: 'rename', boxName: box.name })">
        <i class="pi pi-pencil"></i> Rename
      </button>
      <button class="action-btn-expanded" @click="$emit('action', { type: 'restart', boxName: box.name })">
        <i class="pi pi-refresh"></i> Restart
      </button>
      <button class="action-btn-expanded danger" @click="$emit('action', { type: 'delete', boxName: box.name })">
        <i class="pi pi-trash"></i> Delete
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { BoxInfo } from '../api/client'
import CopyButton from './CopyButton.vue'
import CoolS from './CoolS.vue'

defineProps<{
  box: BoxInfo
  hasTeam: boolean
}>()

defineEmits<{
  (e: 'action', action: { type: string; boxName: string; extra?: any }): void
}>()

</script>

<style scoped>
.box-details {
  display: flex;
  flex-direction: column;
  gap: 10px;
  cursor: default;
}

.detail-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12px;
}

.detail-label {
  color: var(--text-color-muted);
  min-width: 70px;
  flex-shrink: 0;
  font-size: 12px;
}

.ssh-cmd {
  font-size: 12px;
  color: var(--text-color);
  background: var(--code-bg);
  padding: 2px 8px;
  border-radius: 3px;
  word-break: break-all;
}

.detail-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 16px;
  font-size: 12px;
}

.detail-item {
  display: flex;
  gap: 8px;
}

.detail-btn {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  color: var(--btn-text);
  transition: all 0.15s;
}

.detail-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
}

.proxy-info {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.tags-row {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  align-items: center;
}

.tag {
  font-size: 11px;
  color: var(--tag-text, var(--text-color-muted));
  background: var(--tag-bg);
  padding: 1px 6px;
  border-radius: 3px;
}

.tag-removable {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.tag-remove {
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  font-size: 14px;
  padding: 0 2px;
  line-height: 1;
}

.tag-remove:hover {
  color: var(--danger-color);
}

.shared-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.shared-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}

.share-link-url {
  font-size: 11px;
  color: var(--text-color);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 300px;
}

.remove-btn {
  background: none;
  border: none;
  color: var(--text-color-muted);
  cursor: pointer;
  padding: 2px 6px;
  font-size: 16px;
}

.remove-btn:hover {
  color: var(--danger-color);
}

.detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 4px;
}

.action-btn-expanded {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 5px 10px;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  color: var(--btn-text);
  text-decoration: none;
  transition: all 0.15s;
}

.action-btn-expanded:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
  text-decoration: none;
}

.action-btn-expanded.danger {
  color: var(--danger-color);
}

.action-btn-expanded.danger:hover {
  background: var(--danger-bg);
  border-color: var(--danger-border);
}

.action-btn-expanded i {
  font-size: 11px;
}

@media (max-width: 768px) {
  .detail-grid {
    grid-template-columns: 1fr;
  }
}
</style>

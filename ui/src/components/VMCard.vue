<template>
  <div class="box-row" :class="{ expanded }" @click="onRowClick">
    <!-- Collapsed row -->
    <div class="box-main">
      <StatusDot :status="box.status" />
      <span class="box-name">{{ box.name }}</span>
      <span v-if="box.totalShareCount > 0" class="share-badge" :title="`Shared with ${box.sharedUserCount} user(s) and ${box.shareLinkCount} link(s)`">
        👥 {{ box.totalShareCount }}
      </span>
      <span v-if="box.proxyShare === 'public'" class="public-badge">PUBLIC</span>
      <span class="box-tags">
        <span v-for="tag in box.displayTags" :key="tag" class="tag">#{{ tag }}</span>
      </span>
      <div class="box-actions">
        <template v-if="box.status !== 'pending'">
          <CopyButton :text="box.sshCommand" title="Copy SSH command" />
          <a :href="box.proxyURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="HTTPS" @click.stop>
            <i class="pi pi-globe" style="font-size: 12px;"></i>
          </a>
          <a :href="box.terminalURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="Terminal" @click.stop>
            <i class="pi pi-chevron-right" style="font-size: 12px;"></i>
          </a>
          <a v-if="box.shelleyURL" :href="box.shelleyURL" class="action-btn" target="_blank" rel="noopener noreferrer" title="Shelley" @click.stop>
            <i class="pi pi-sparkles" style="font-size: 12px;"></i>
          </a>
        </template>
        <button class="action-btn expand-btn" @click.stop="$emit('toggle')" :title="expanded ? 'Collapse' : 'Expand'">
          <i :class="expanded ? 'pi pi-chevron-up' : 'pi pi-chevron-down'" style="font-size: 10px;"></i>
        </button>
      </div>
    </div>

    <!-- Expanded details -->
    <div v-if="expanded" class="box-details" @click.stop>
      <!-- SSH -->
      <div v-if="box.status !== 'pending'" class="detail-row">
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
        <div class="detail-item">
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
          <button class="inline-btn" @click="$emit('action', { type: 'add-tag', boxName: box.name })">+ tag</button>
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

      <!-- Creation log -->
      <CreationLog
        v-if="box.status === 'creating'"
        :hostname="box.name"
        :streaming="true"
      />
      <div v-else-if="box.hasCreationLog && showCreationLog" class="creation-log-wrap">
        <CreationLog :hostname="box.name" :streaming="false" />
      </div>
      <div v-else-if="box.hasCreationLog && !showCreationLog" class="detail-item">
        <span class="detail-label">Creation Log:</span>
        <button class="detail-btn" @click="showCreationLog = true">View</button>
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
          <i class="pi pi-sparkles"></i> Shelley
        </a>
        <button class="action-btn-expanded" @click="$emit('action', { type: 'share', boxName: box.name })">
          <i class="pi pi-share-alt"></i> Share
        </button>
        <button class="action-btn-expanded" @click="$emit('action', { type: 'share-link', boxName: box.name })">
          <i class="pi pi-link"></i> Share Link
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
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import type { BoxInfo } from '../api/client'
import StatusDot from './StatusDot.vue'
import CopyButton from './CopyButton.vue'
import { defineAsyncComponent } from 'vue'
const CreationLog = defineAsyncComponent(() => import('./CreationLog.vue'))

const showCreationLog = ref(false)

defineProps<{
  box: BoxInfo
  expanded: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle'): void
  (e: 'action', action: { type: string; boxName: string; extra?: any }): void
}>()

function onRowClick(event: MouseEvent) {
  if ((event.target as HTMLElement).closest('button, a')) return
  emit('toggle')
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

.box-name {
  font-weight: 500;
  font-size: 13px;
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
  padding: 4px 6px;
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 4px;
  cursor: pointer;
  color: var(--btn-text);
  text-decoration: none;
  transition: all 0.15s;
}

.action-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
  text-decoration: none;
}

.expand-btn {
  width: 24px;
  height: 24px;
}

/* Expanded details */
.box-details {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--surface-border);
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

.inline-btn {
  background: none;
  border: none;
  color: var(--primary-color);
  cursor: pointer;
  font-size: 11px;
  font-family: inherit;
  padding: 0 2px;
  text-decoration: underline;
}

.inline-btn:hover {
  color: var(--primary-hover);
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
}

.tags-row {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  align-items: center;
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
  .box-actions {
    gap: 2px;
  }
  .box-row {
    padding: 10px 8px;
  }
  .box-main {
    gap: 8px;
  }
}
</style>

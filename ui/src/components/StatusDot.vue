<template>
  <button
    v-if="emoji"
    type="button"
    class="status-emoji"
    :class="[status, { clickable: !!clickable }]"
    :title="clickable ? `${status} — click to change emoji` : status"
    :aria-label="`${status}${clickable ? ' (click to change emoji)' : ''}`"
    :disabled="!clickable"
    @click.stop="onClick"
  >{{ emoji }}</button>
  <button
    v-else-if="clickable"
    type="button"
    class="status-dot clickable"
    :class="status"
    :title="`${status} — click to set an emoji`"
    :aria-label="`${status} (click to set an emoji)`"
    @click.stop="onClick"
  ></button>
  <span
    v-else
    class="status-dot"
    :class="status"
    :title="status"
    role="status"
    :aria-label="status"
  ></span>
</template>

<script setup lang="ts">
const props = defineProps<{
  status: string
  emoji?: string
  clickable?: boolean
}>()

const emit = defineEmits<{ (e: 'edit'): void }>()

function onClick() {
  if (props.clickable) emit('edit')
}
</script>

<style scoped>
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

button.status-dot {
  padding: 0;
  border: 2px solid transparent;
  background-clip: padding-box;
  cursor: pointer;
  width: 14px;
  height: 14px;
  box-sizing: border-box;
  transition: transform 0.12s;
}

button.status-dot:hover { transform: scale(1.15); border-color: var(--surface-border); }

.status-dot.running { background: var(--success-color); box-shadow: 0 0 4px rgba(34, 197, 94, 0.4); }
.status-dot.stopped { background: var(--text-color-muted); }
.status-dot.creating { background: var(--warning-color); animation: pulse 1.5s ease-in-out infinite; }
.status-dot.error { background: var(--danger-color); }

.status-emoji {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  font-size: 16px;
  line-height: 1;
  flex-shrink: 0;
  cursor: default;
  transition: transform 0.12s, border-color 0.12s, background 0.12s;
  font-family: "Apple Color Emoji", "Segoe UI Emoji", "Noto Color Emoji", sans-serif;
}

.status-emoji.clickable {
  cursor: pointer;
}

.status-emoji.clickable:hover {
  background: var(--surface-inset);
  border-color: var(--surface-border);
  transform: scale(1.08);
}

.status-emoji.stopped { filter: grayscale(1); opacity: 0.55; }
.status-emoji.creating { animation: pulse 1.5s ease-in-out infinite; }
.status-emoji.error { filter: hue-rotate(-30deg) saturate(1.3); }
.status-emoji.pending { opacity: 0.55; }

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}
</style>

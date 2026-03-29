<template>
  <div class="view-popover-anchor" ref="anchorRef">
    <button class="view-btn" @click="toggle" :class="{ active: open }">
      <i class="pi pi-sliders-h" style="font-size: 11px;"></i>
      <span class="view-btn-text">View</span>
      <i class="pi pi-chevron-down view-btn-chevron" style="font-size: 9px;"></i>
    </button>
    <Teleport to="body">
      <div v-if="open" class="view-backdrop" @click="open = false"></div>
      <div v-if="open" class="view-popover" ref="popoverRef" :style="popoverStyle">
        <div class="view-section">
          <div class="view-section-label">Sort by</div>
          <label class="view-radio" v-for="opt in sortOptions" :key="opt.value">
            <input type="radio" :value="opt.value" v-model="localSort" @change="emitChange" />
            <span>{{ opt.label }}</span>
          </label>
        </div>
        <div class="view-divider"></div>
        <div class="view-section">
          <div class="view-section-label">Group by</div>
          <label class="view-radio" v-for="opt in groupOptions" :key="opt.value">
            <input type="radio" :value="opt.value" v-model="localGroup" @change="emitChange" />
            <span>{{ opt.label }}</span>
          </label>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'

export type SortField = 'updatedAt' | 'createdAt' | 'name'
export type GroupField = 'none' | 'tag'

export interface ViewOptions {
  sort: SortField
  group: GroupField
}

const props = defineProps<{
  modelValue: ViewOptions
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: ViewOptions): void
}>()

const open = ref(false)
const anchorRef = ref<HTMLElement | null>(null)
const popoverRef = ref<HTMLElement | null>(null)
const popoverStyle = ref<Record<string, string>>({})

const localSort = ref<SortField>(props.modelValue.sort)
const localGroup = ref<GroupField>(props.modelValue.group)

watch(() => props.modelValue, (v) => {
  localSort.value = v.sort
  localGroup.value = v.group
})

const sortOptions: { value: SortField; label: string }[] = [
  { value: 'updatedAt', label: 'Last used' },
  { value: 'createdAt', label: 'Created' },
  { value: 'name', label: 'Name' },
]

const groupOptions: { value: GroupField; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'tag', label: 'Tag' },
]

function toggle() {
  open.value = !open.value
  if (open.value) {
    nextTick(positionPopover)
  }
}

function positionPopover() {
  if (!anchorRef.value) return
  const rect = anchorRef.value.getBoundingClientRect()
  const popWidth = 200
  let left = rect.left
  // If popover would overflow right edge, align to right edge of button
  if (left + popWidth > window.innerWidth - 8) {
    left = rect.right - popWidth
  }
  popoverStyle.value = {
    top: `${rect.bottom + 6}px`,
    left: `${Math.max(8, left)}px`,
  }
}

function emitChange() {
  emit('update:modelValue', {
    sort: localSort.value,
    group: localGroup.value,
  })
}
</script>

<style scoped>
.view-popover-anchor {
  position: relative;
}

.view-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  height: 30px;
  box-sizing: border-box;
  background: var(--btn-bg);
  color: var(--btn-text);
  border: 1px solid var(--btn-border);
  border-radius: 6px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: all 0.15s;
  white-space: nowrap;
}

.view-btn:hover,
.view-btn.active {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
}

.view-backdrop {
  position: fixed;
  inset: 0;
  z-index: 999;
}

.view-popover {
  position: fixed;
  z-index: 1000;
  width: 200px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  padding: 8px 0;
  font-size: 13px;
}

@media (prefers-color-scheme: dark) {
  .view-popover {
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  }
}

.view-section {
  padding: 4px 12px;
}

.view-section-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--text-color-muted);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 4px;
}

.view-radio {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  cursor: pointer;
  color: var(--text-color);
  font-size: 13px;
}

.view-radio input[type="radio"] {
  accent-color: var(--primary-color);
  margin: 0;
  flex-shrink: 0;
}

.view-divider {
  height: 1px;
  background: var(--surface-border);
  margin: 4px 0;
}

@media (max-width: 768px) {
  .view-btn-text,
  .view-btn-chevron {
    display: none;
  }
  .view-btn {
    padding: 4px 8px;
  }
}
</style>

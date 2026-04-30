<template>
  <div class="multi-tag-picker">
    <div class="selected-tags" :class="{ empty: modelValue.length === 0 }">
      <button
        v-for="tag in modelValue"
        :key="tag"
        type="button"
        class="selected-tag"
        :title="`Remove ${tag}`"
        @click="removeTag(tag)"
      >
        #{{ tag }} <span aria-hidden="true">×</span>
      </button>
    </div>
    <div class="tag-input-wrap">
      <input
        ref="inputRef"
        v-model="tagSearch"
        type="text"
        class="tag-input"
        :placeholder="placeholder"
        autocomplete="off"
        autocapitalize="none"
        autocorrect="off"
        spellcheck="false"
        @input="onTagInput"
        @keydown.enter.prevent="addHighlightedTag"
        @keydown.tab="addHighlightedTag"
        @keydown.backspace="onTagBackspace"
      />
    </div>
    <div class="tag-options" :class="{ empty: pickerOptions.length === 0 }" aria-label="Available tags">
      <button
        v-for="opt in pickerOptions"
        :key="opt.tag"
        type="button"
        class="tag-option"
        @click="addTag(opt.tag)"
      >
        <span class="tag-option-name">{{ opt.create ? 'Create #' : '#' }}{{ opt.tag }}</span>
        <span v-if="opt.integrations.length > 0" class="tag-option-integrations">
          {{ opt.integrations.join(', ') }}<span v-if="opt.more > 0"> +{{ opt.more }}</span>
        </span>
        <span v-else class="tag-option-integrations muted">{{ opt.create ? 'No integrations yet' : 'No integrations' }}</span>
      </button>
    </div>
    <div v-if="tagError" class="tag-hint error">{{ tagError }}</div>
    <div v-else-if="hint" class="tag-hint">{{ hint }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'

export interface TagSummary {
  tag: string
  integrations: string[]
  more: number
}

const props = withDefaults(defineProps<{
  modelValue: string[]
  tagSummaries?: TagSummary[]
  excludedTags?: string[]
  placeholder?: string
  hint?: string
  maxOptions?: number
}>(), {
  tagSummaries: () => [],
  excludedTags: () => [],
  placeholder: 'Search or add a tag...',
  hint: '',
  maxOptions: 6,
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: string[]): void
}>()

const tagNameRe = /^[a-z][a-z0-9_-]*$/
const tagSearch = ref('')
const inputRef = ref<HTMLInputElement | null>(null)

const selectedSet = computed(() => new Set(props.modelValue))
const excludedSet = computed(() => new Set(props.excludedTags))
const tagSummaryMap = computed(() => new Map(props.tagSummaries.map(s => [s.tag, s])))

const tagError = computed(() => {
  const q = tagSearch.value.trim()
  if (!q) return ''
  if (!tagNameRe.test(q)) return 'Tag names must match [a-z][a-z0-9_-]*'
  if (selectedSet.value.has(q) || excludedSet.value.has(q)) return `#${q} is already on this VM`
  return ''
})

const creatableTag = computed(() => {
  const q = tagSearch.value.trim()
  if (!q || !tagNameRe.test(q) || selectedSet.value.has(q) || excludedSet.value.has(q) || tagSummaryMap.value.has(q)) return ''
  return q
})

const tagOptions = computed(() => {
  const q = tagSearch.value.trim().toLowerCase()
  return props.tagSummaries
    .filter(s => !selectedSet.value.has(s.tag) && !excludedSet.value.has(s.tag))
    .filter(s => !q || s.tag.toLowerCase().includes(q) || s.integrations.some(name => name.toLowerCase().includes(q)))
    .slice(0, props.maxOptions)
})

const pickerOptions = computed(() => {
  if (tagOptions.value.length > 0) return tagOptions.value.map(opt => ({ ...opt, create: false }))
  if (creatableTag.value) return [{ tag: creatableTag.value, integrations: [], more: 0, create: true }]
  return []
})

function addTag(tag: string) {
  tag = tag.trim()
  if (!tagNameRe.test(tag) || selectedSet.value.has(tag) || excludedSet.value.has(tag)) return
  emit('update:modelValue', [...props.modelValue, tag].sort())
  tagSearch.value = ''
  inputRef.value?.focus()
}

function removeTag(tag: string) {
  emit('update:modelValue', props.modelValue.filter(t => t !== tag))
}

function addHighlightedTag(e?: KeyboardEvent) {
  if (tagOptions.value.length > 0) {
    e?.preventDefault()
    addTag(tagOptions.value[0].tag)
    return
  }
  if (creatableTag.value) {
    e?.preventDefault()
    addTag(creatableTag.value)
  }
}

function onTagInput() {
  const raw = tagSearch.value
  const parts = raw.split(/[\s,]+/)
  if (parts.length < 2) return

  const complete = parts.slice(0, -1)
  const remaining = parts[parts.length - 1]
  const next = [...props.modelValue]
  const nextSet = new Set(next)
  for (const part of complete) {
    const tag = part.trim()
    if (!tagNameRe.test(tag) || nextSet.has(tag) || excludedSet.value.has(tag)) continue
    next.push(tag)
    nextSet.add(tag)
  }
  if (next.length !== props.modelValue.length) {
    emit('update:modelValue', next.sort())
  }
  tagSearch.value = remaining
}

function onTagBackspace() {
  if (!tagSearch.value && props.modelValue.length > 0) {
    emit('update:modelValue', props.modelValue.slice(0, -1))
  }
}

defineExpose({ focus: () => inputRef.value?.focus() })
</script>

<style scoped>
.selected-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 24px;
  margin-bottom: 6px;
}

.selected-tags.empty {
  visibility: hidden;
}

.selected-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border: 1px solid var(--surface-border);
  border-radius: 999px;
  background: var(--tag-bg);
  color: var(--tag-text);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}

.selected-tag:hover {
  border-color: var(--danger-color);
  color: var(--danger-color);
}

.tag-input-wrap {
  margin-bottom: 6px;
}

.tag-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid var(--input-border);
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  background: var(--input-bg);
  color: var(--input-text);
  outline: none;
}

.tag-input:focus {
  border-color: var(--input-focus-border);
}

.tag-options {
  display: grid;
  align-content: start;
  gap: 4px;
  height: 178px;
  overflow-y: auto;
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  padding: 4px;
  background: var(--surface-card);
}

.tag-options.empty {
  visibility: hidden;
}

.tag-option {
  display: grid;
  grid-template-columns: minmax(80px, auto) 1fr;
  gap: 8px;
  align-items: center;
  width: 100%;
  padding: 5px 7px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-color);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.tag-option:hover {
  background: var(--surface-hover);
}

.tag-option-name {
  font-weight: 500;
  white-space: nowrap;
}

.tag-option-integrations {
  color: var(--text-color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}

.tag-option-integrations.muted {
  opacity: 0.75;
}

.tag-hint {
  min-height: 16px;
  margin-top: 4px;
  font-size: 11px;
  color: var(--text-color-muted);
}

.tag-hint.error {
  color: var(--danger-color);
}
</style>

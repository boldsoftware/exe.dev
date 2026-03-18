<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="dialog-overlay" @click.self="handleCancel">
        <div class="dialog-panel">
          <div class="dialog-header">
            <i class="pi" :class="iconClass"></i>
            <span class="dialog-title">{{ title }}</span>
          </div>
          <div class="dialog-body">
            <slot>{{ message }}</slot>
          </div>
          <div class="dialog-footer">
            <button class="dialog-btn btn-cancel" @click="handleCancel" :disabled="loading">
              Cancel
            </button>
            <button
              class="dialog-btn" :class="confirmClass"
              @click="handleConfirm"
              :disabled="loading"
            >
              <i v-if="loading" class="pi pi-spin pi-spinner"></i>
              {{ confirmLabel }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  visible: boolean
  title?: string
  message?: string
  confirmLabel?: string
  variant?: 'danger' | 'primary'
  loading?: boolean
}>(), {
  title: 'Confirm',
  message: '',
  confirmLabel: 'Confirm',
  variant: 'primary',
  loading: false,
})

const emit = defineEmits<{
  (e: 'confirm'): void
  (e: 'cancel'): void
}>()

const iconClass = computed(() =>
  props.variant === 'danger' ? 'pi-exclamation-triangle icon-danger' : 'pi-question-circle icon-primary'
)

const confirmClass = computed(() =>
  props.variant === 'danger' ? 'btn-danger' : 'btn-primary'
)

function handleConfirm() {
  if (!props.loading) emit('confirm')
}

function handleCancel() {
  if (!props.loading) emit('cancel')
}
</script>

<style scoped>
.dialog-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.6);
  backdrop-filter: blur(2px);
}

.dialog-panel {
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 6px;
  width: 100%;
  max-width: 420px;
  margin: 1rem;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
}

.dialog-header {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 1rem 1.25rem 0.5rem;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--text-color);
}

.dialog-header .icon-danger {
  color: var(--red-400);
  font-size: 1.1rem;
}

.dialog-header .icon-primary {
  color: var(--primary-color);
  font-size: 1.1rem;
}

.dialog-body {
  padding: 0.5rem 1.25rem 1rem;
  font-size: 0.8rem;
  color: var(--text-color-secondary);
  line-height: 1.5;
}

.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 0.5rem;
  padding: 0.75rem 1.25rem;
  border-top: 1px solid var(--surface-border);
}

.dialog-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.4rem 0.85rem;
  font-size: 0.78rem;
  font-family: inherit;
  font-weight: 500;
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.15s;
  border: 1px solid;
}

.dialog-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-cancel {
  background: var(--surface-overlay);
  border-color: var(--surface-border);
  color: var(--text-color-secondary);
}

.btn-cancel:hover:not(:disabled) {
  border-color: var(--surface-border-bright);
  color: var(--text-color);
}

.btn-danger {
  background: var(--red-subtle);
  border-color: var(--red-400);
  color: var(--red-400);
}

.btn-danger:hover:not(:disabled) {
  background: var(--red-400);
  color: #fff;
}

.btn-primary {
  background: var(--primary-50);
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.btn-primary:hover:not(:disabled) {
  background: var(--primary-color);
  color: var(--primary-color-text);
}

/* Transitions */
.dialog-enter-active,
.dialog-leave-active {
  transition: opacity 0.15s ease;
}

.dialog-enter-active .dialog-panel,
.dialog-leave-active .dialog-panel {
  transition: transform 0.15s ease;
}

.dialog-enter-from,
.dialog-leave-to {
  opacity: 0;
}

.dialog-enter-from .dialog-panel {
  transform: scale(0.95);
}

.dialog-leave-to .dialog-panel {
  transform: scale(0.95);
}
</style>

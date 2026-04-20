<template>
  <div class="trial-banner" :class="{ expired: expired }">
    <div class="trial-content">
      <span class="trial-icon">{{ expired ? '⚠️' : '⏳' }}</span>
      <span class="trial-text" v-if="expired">
        Your <strong>trial has expired</strong>.
        <a href="/user" class="trial-link">Upgrade</a> to keep using your VMs.
      </span>
      <span class="trial-text" v-else>
        <strong>Trial</strong> — {{ daysText }} remaining.
        <a href="/user" class="trial-link">Upgrade</a> to keep your VMs.
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  daysLeft: number
  expired: boolean
}>()

const daysText = computed(() => {
  if (props.daysLeft <= 0) return 'less than a day'
  if (props.daysLeft === 1) return '1 day'
  return `${props.daysLeft} days`
})
</script>

<style scoped>
.trial-banner {
  background: var(--warning-bg);
  border: 1px solid var(--warning-color);
  border-radius: 6px;
  padding: 10px 16px;
}

.trial-banner.expired {
  background: var(--danger-bg);
  border-color: var(--danger-color);
}

.trial-content {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--warning-text);
}

.expired .trial-content {
  color: var(--danger-text);
}

.trial-icon {
  font-size: 16px;
  flex-shrink: 0;
}

.trial-text {
  line-height: 1.4;
}

.trial-link {
  color: var(--warning-text);
  text-decoration: underline;
  font-weight: 600;
}

.expired .trial-link {
  color: var(--danger-text);
}

.trial-link:hover {
  opacity: 0.8;
}
</style>

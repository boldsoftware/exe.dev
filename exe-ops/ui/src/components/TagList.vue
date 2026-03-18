<template>
  <div class="tag-list" v-if="tags && tags.length > 0">
    <span class="tag" v-for="tag in tags" :key="tag">{{ tag }}</span>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  tags: string[]
}>()
</script>

<style scoped>
.tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
}

.tag {
  background: var(--primary-50);
  color: var(--primary-color);
  padding: 0.15rem 0.5rem;
  border-radius: 6px;
  font-size: 0.7rem;
  font-weight: 500;
  letter-spacing: 0.01em;
}
</style>

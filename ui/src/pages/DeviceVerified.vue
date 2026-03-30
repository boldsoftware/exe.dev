<template>
  <div class="page">
    <main class="page-content">
      <p class="heading">KEY AUTHORIZED</p>
      <p class="subtitle">(return to your terminal)</p>
    </main>
  </div>
</template>

<script setup lang="ts">
</script>

<style scoped>
.page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 48px;
}

.page-content {
  width: 100%;
  max-width: 42rem;
}

.heading {
  font-size: 3.75rem;
  font-weight: 600;
  letter-spacing: 0.1em;
  line-height: 1;
  margin-bottom: 1.5rem;
}

.subtitle {
  color: var(--text-color-secondary);
}

@media (max-width: 640px) {
  .page {
    padding: 24px;
  }
  .heading {
    font-size: 2.25rem;
  }
}
</style>

<template>
  <div class="docs-list-page">
    <div v-if="loading" class="loading-state">
      <i class="pi pi-spin pi-spinner"></i> Loading...
    </div>
    <div v-else-if="loadError" class="error-state">
      <p>{{ loadError }}</p>
    </div>
    <main v-else class="docs-list-main">
      <h1 class="page-title">exe Documentation</h1>
      <p class="page-subtitle">Learn how exe hosts persistent development containers you can reach over SSH or the browser.</p>
      <p class="page-links">
        <router-link to="/docs/all">View all docs in one page</router-link> &middot;
        <a href="/docs/all.md">Download as Markdown</a> &middot;
        <a href="/llms.txt">llms.txt</a>
      </p>

      <div class="docs-container">
        <section v-for="group in groups" :key="group.slug" class="docs-section">
          <h2 class="section-title">{{ group.heading }}</h2>
          <div v-for="doc in group.docs" :key="doc.slug" class="doc-item">
            <h3 class="doc-title">
              <router-link :to="'/docs/' + doc.slug">{{ doc.title }}</router-link>
            </h3>
            <p v-if="doc.description" class="doc-description">{{ doc.description }}</p>
          </div>
        </section>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { fetchDocsList, type DocsGroup } from '../api/client'

const loading = ref(true)
const loadError = ref('')
const groups = ref<DocsGroup[]>([])

onMounted(async () => {
  try {
    const data = await fetchDocsList()
    groups.value = data.groups
  } catch (e: any) {
    loadError.value = e.message || 'Failed to load'
  } finally {
    loading.value = false
  }
})
</script>

<style scoped>
.docs-list-page {
  margin: -24px -20px;
  padding: 0;
}

.loading-state, .error-state {
  padding: 48px 24px;
  text-align: center;
  color: var(--text-color-secondary);
}

.docs-list-main {
  max-width: 1200px;
  margin: 64px auto 120px;
  padding: 96px 72px;
  background: var(--surface-card);
  border: 1px solid var(--surface-border);
  border-radius: 28px;
  box-shadow: 0 16px 40px rgba(0, 0, 0, 0.06);
}

@media (prefers-color-scheme: dark) {
  .docs-list-main {
    box-shadow: 0 16px 40px rgba(0, 0, 0, 0.3);
  }
}

.page-title {
  font-size: 48px;
  font-weight: 400;
  color: var(--text-color);
  margin-bottom: 24px;
  line-height: 1.1;
  letter-spacing: -0.02em;
}

.page-subtitle {
  font-size: 20px;
  color: var(--text-color-secondary);
  margin-bottom: 80px;
  max-width: 600px;
  line-height: 1.4;
  font-weight: 300;
}

.page-links {
  margin-bottom: 48px;
  font-size: 12px;
  color: var(--text-color-secondary);
}

.page-links a {
  color: var(--primary-color);
  text-decoration: none;
}

.page-links a:hover {
  text-decoration: underline;
}

.docs-container {
  max-width: 900px;
}

.docs-section {
  margin-bottom: 64px;
}

.section-title {
  font-size: 28px;
  font-weight: 400;
  color: var(--text-color);
  margin-bottom: 32px;
  line-height: 1.2;
}

.doc-item {
  margin-bottom: 32px;
  padding-bottom: 32px;
  border-bottom: 1px dotted var(--surface-border);
}

.doc-item:last-child {
  border-bottom: none;
}

.doc-title {
  font-size: 20px;
  font-weight: 600;
  color: var(--text-color);
  margin-bottom: 8px;
  line-height: 1.3;
}

.doc-title a {
  color: inherit;
  text-decoration: none;
}

.doc-title a:hover {
  text-decoration: underline;
}

.doc-description {
  font-size: 14px;
  color: var(--text-color-secondary);
  line-height: 1.5;
}

@media (max-width: 768px) {
  .docs-list-main {
    margin: 32px auto 80px;
    padding: 60px 28px;
    border-radius: 20px;
  }

  .page-title {
    font-size: 36px;
  }

  .page-subtitle {
    font-size: 18px;
    margin-bottom: 60px;
  }

  .section-title {
    font-size: 24px;
  }

  .doc-title {
    font-size: 18px;
  }
}
</style>

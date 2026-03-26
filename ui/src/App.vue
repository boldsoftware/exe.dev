<template>
  <div class="layout">
    <header class="topbar">
      <div class="nav-container">
        <div class="nav-left">
          <router-link to="/" class="nav-logo">
            <img src="/exy.png" alt="exe.dev" class="logo-img" />
            <span class="logo-text">exe.dev</span>
          </router-link>
          <nav class="nav-links">
            <router-link to="/docs" class="docs-link">docs</router-link>
          </nav>
        </div>
        <div class="nav-right">
          <template v-if="isLoggedIn">
            <router-link to="/" class="nav-btn" :class="{ active: route.name === 'vms' }">
              <i class="pi pi-box"></i>
              <span class="nav-btn-text">VMs</span>
            </router-link>
            <router-link to="/integrations" class="nav-btn" :class="{ active: route.name === 'integrations' }">
              <i class="pi pi-arrows-h"></i>
              <span class="nav-btn-text">Integrations</span>
            </router-link>
            <router-link to="/shell" class="nav-btn" :class="{ active: route.name === 'shell' }">
              <i class="pi pi-chevron-right"></i>
              <span class="nav-btn-text">Lobby</span>
            </router-link>
            <router-link to="/user" class="nav-btn" :class="{ active: route.name === 'profile' }">
              <i class="pi pi-user"></i>
              <span class="nav-btn-text">Profile</span>
            </router-link>
            <a href="/logout" class="nav-btn">
              <i class="pi pi-sign-out"></i>
              <span class="nav-btn-text">Sign out</span>
            </a>
          </template>
          <a v-else href="/auth" class="nav-btn">
            <i class="pi pi-sign-in"></i>
            <span class="nav-btn-text">Sign in</span>
          </a>
        </div>
      </div>
    </header>
    <main class="content">
      <router-view />
    </main>
    <Toast position="bottom-center" />
  </div>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router'
import Toast from 'primevue/toast'
import { isAuthenticated } from './api/client'

const route = useRoute()
// Auth state is derived from API responses — no separate auth-check request needed.
const isLoggedIn = isAuthenticated
</script>

<style>
/* Reset */
*, *::before, *::after {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

:root {
  /* Surface colors */
  --surface-ground: #fafafa;
  --surface-card: #ffffff;
  --surface-border: #e0e0e0;
  --surface-hover: #f5f5f5;
  --surface-subtle: #f3f4f6;
  --surface-inset: #f8f8f8;
  --surface-overlay: rgba(0, 0, 0, 0.4);

  /* Text colors */
  --text-color: #1a1a1a;
  --text-color-secondary: #555;
  --text-color-muted: #717171;

  /* Accent / brand */
  --primary-color: #0d9488;
  --primary-hover: #0a7c72;
  --link-color: #0d9488;

  /* Buttons (secondary / ghost style) */
  --btn-bg: #ffffff;
  --btn-border: #e0e0e0;
  --btn-text: #555;
  --btn-hover-bg: #f5f5f5;
  --btn-hover-border: #d0d0d0;
  --btn-hover-text: #1a1a1a;
  --btn-active-bg: #fafafa;
  --btn-active-border: #ccc;

  /* Status / semantic */
  --danger-color: #dc2626;
  --danger-bg: #fef2f2;
  --danger-border: #fecaca;
  --danger-text: #dc2626;
  --danger-hover: #b91c1c;
  --success-color: #22c55e;
  --success-bg: #f0fdf4;
  --success-border: #dcfce7;
  --success-text: #166534;
  --warning-color: #d97706;
  --warning-bg: #fefce8;
  --warning-text: #d97706;

  /* Badges */
  --badge-share-bg: #dbeafe;
  --badge-share-text: #1e40af;
  --badge-public-bg: #fef3c7;
  --badge-public-text: #92400e;

  /* Tags */
  --tag-bg: #f3f4f6;
  --tag-text: #999;

  /* Inputs */
  --input-bg: #ffffff;
  --input-border: #e0e0e0;
  --input-focus-border: #555;
  --input-text: #1a1a1a;
  --input-placeholder: #999;

  /* Code blocks */
  --code-bg: #f3f4f6;
  --code-text: #1a1a1a;

  /* Font */
  --font-mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
}

@media (prefers-color-scheme: dark) {
  :root {
    /* Surface colors */
    --surface-ground: #111;
    --surface-card: #1f1f1f;
    --surface-border: #333;
    --surface-hover: #333;
    --surface-subtle: #2a2a2a;
    --surface-inset: #2a2a2a;
    --surface-overlay: rgba(0, 0, 0, 0.7);

    /* Text colors */
    --text-color: #f3f4f6;
    --text-color-secondary: #b0b8c4;
    --text-color-muted: #8b95a3;

    /* Accent / brand */
    --primary-color: #5eead4;
    --primary-hover: #99f6e4;
    --link-color: #5eead4;

    /* Buttons */
    --btn-bg: #2a2a2a;
    --btn-border: #333;
    --btn-text: #b0b8c4;
    --btn-hover-bg: #333;
    --btn-hover-border: #444;
    --btn-hover-text: #f3f4f6;
    --btn-active-bg: #333;
    --btn-active-border: #555;

    /* Status / semantic */
    --danger-color: #f87171;
    --danger-bg: #450a0a;
    --danger-border: #991b1b;
    --danger-text: #fca5a5;
    --danger-hover: #ef4444;
    --success-color: #4ade80;
    --success-bg: #052e16;
    --success-border: #166534;
    --success-text: #86efac;
    --warning-color: #fbbf24;
    --warning-bg: #422006;
    --warning-text: #fbbf24;

    /* Badges */
    --badge-share-bg: #1e3a5f;
    --badge-share-text: #93c5fd;
    --badge-public-bg: #422006;
    --badge-public-text: #fde68a;

    /* Tags */
    --tag-bg: #2a2a2a;
    --tag-text: #6b7280;

    /* Inputs */
    --input-bg: #1f1f1f;
    --input-border: #333;
    --input-focus-border: #555;
    --input-text: #f3f4f6;
    --input-placeholder: #6b7280;

    /* Code blocks */
    --code-bg: #2a2a2a;
    --code-text: #f3f4f6;

  }
}

html, body {
  height: 100%;
}

body {
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.6;
  background: var(--surface-ground);
  color: var(--text-color);
  -webkit-font-smoothing: antialiased;
}

a {
  color: var(--link-color);
  text-decoration: none;
}

a:hover {
  text-decoration: underline;
}

/* Shared input styling */
input[type="text"],
input[type="number"],
input[type="email"],
textarea,
select {
  background: var(--input-bg);
  border: 1px solid var(--input-border);
  color: var(--input-text);
  font-family: var(--font-mono);
}

input::placeholder,
textarea::placeholder {
  color: var(--input-placeholder);
}

input:focus,
textarea:focus,
select:focus {
  border-color: var(--input-focus-border);
  outline: none;
}
</style>

<style scoped>
.layout {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.topbar {
  background: var(--surface-card);
  border-bottom: 1px solid var(--surface-border);
  position: sticky;
  top: 0;
  z-index: 100;
}

.nav-container {
  max-width: 1000px;
  margin: 0 auto;
  padding: 0 20px;
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.nav-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.nav-logo {
  display: flex;
  align-items: center;
  gap: 8px;
  text-decoration: none;
  color: var(--text-color);
  font-weight: 600;
}

.logo-img {
  width: 24px;
  height: 24px;
}

.logo-text {
  font-size: 14px;
  letter-spacing: -0.02em;
}

.nav-links a {
  font-size: 13px;
  color: var(--text-color-secondary);
  text-decoration: none;
}

.nav-links a:hover {
  color: var(--text-color);
  text-decoration: none;
}

.nav-right {
  display: flex;
  align-items: center;
  gap: 4px;
}

.nav-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--btn-text);
  background: var(--btn-bg);
  border: 1px solid var(--btn-border);
  border-radius: 6px;
  text-decoration: none;
  transition: all 0.15s;
  font-family: inherit;
}

.nav-btn:hover {
  background: var(--btn-hover-bg);
  border-color: var(--btn-hover-border);
  color: var(--btn-hover-text);
  text-decoration: none;
}

.nav-btn.active {
  background: var(--btn-active-bg);
  border-color: var(--btn-active-border);
  color: var(--text-color);
  font-weight: 600;
}

.nav-btn i {
  font-size: 12px;
}

.content {
  flex: 1;
  max-width: 1000px;
  margin: 0 auto;
  padding: 24px 20px;
  width: 100%;
}

@media (max-width: 768px) {
  .nav-btn-text {
    display: none;
  }
  .nav-btn {
    padding: 6px 8px;
  }
  .nav-right {
    gap: 2px;
  }
  .content {
    padding: 12px 8px;
  }
}
</style>

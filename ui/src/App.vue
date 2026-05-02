<template>
  <div class="layout">
    <header class="topbar">
      <div class="nav-container">
        <div class="nav-left">
          <a v-if="isLoggedIn" href="/" class="nav-logo" @click.prevent="$router.push('/')">
            <img src="/exy.png" alt="exe.dev" class="logo-img" />
          </a>
          <a v-else href="/" class="nav-logo">
            <img src="/exy.png" alt="exe.dev" class="logo-img" />
          </a>
          <a v-if="isLoggedIn" href="/" class="logo-text" @click.prevent="$router.push('/')">exe.dev</a>
          <a v-else href="/" class="logo-text">exe.dev</a>
          <router-link to="/docs" class="docs-link">docs</router-link>
          <a href="https://blog.exe.dev" class="docs-link">blog</a>
          <a v-if="!isLoggedIn" href="/pricing" class="docs-link">pricing</a>
        </div>
        <div class="nav-right">
          <template v-if="isLoggedIn">
            <router-link to="/" class="nav-btn" :class="{ active: route.name === 'vms' || route.name === 'vms-usage' }">
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
          <template v-else>
            <a href="/auth" class="nav-login-btn"><span class="nav-login-full">Login / Register</span><span class="nav-login-short">Login</span></a>
          </template>
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


html, body {
  height: 100%;
}

body {
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.6;
  background: var(--surface-ground);
  background-image: var(--pinstripe-image);
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
  max-width: 1200px;
  margin: 0 auto;
  padding: 1rem 2rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.nav-left {
  display: flex;
  align-items: baseline;
  gap: 1rem;
}

.nav-logo {
  display: flex;
  align-self: center;
}

.logo-img {
  width: 24px;
  height: 24px;
}

.logo-text {
  font-size: 0.9rem;
  font-weight: 600;
  text-decoration: none;
  color: var(--text-color);
}

.docs-link {
  font-size: 0.85rem;
  color: var(--text-color-secondary);
  text-decoration: none;
}

.docs-link:hover {
  color: var(--text-color);
  text-decoration: none;
}

.nav-right {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 0.85rem;
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

.nav-login-btn {
  padding: 0.4rem 1rem;
  border: 1px solid #d1d5db;
  border-radius: 0.375rem;
  font-size: 0.85rem;
  color: #6b7280;
  text-decoration: none;
  transition: all 0.15s;
}

.nav-login-btn:hover {
  border-color: var(--text-color);
  color: var(--text-color);
  text-decoration: none;
}

.nav-login-short {
  display: none;
}

.content {
  flex: 1;
  max-width: 1000px;
  margin: 0 auto;
  padding: 24px 20px;
  width: 100%;
}

@media (prefers-color-scheme: dark) {
  .nav-login-btn {
    border-color: #4b5563;
    color: #9ca3af;
  }
  .nav-login-btn:hover {
    border-color: #f3f4f6;
    color: #f3f4f6;
  }
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
    font-size: 0.75rem;
  }
  .docs-link {
    font-size: 0.75rem;
  }
  .logo-text {
    font-size: 0.8rem;
    margin-right: 0.5rem;
  }
  .nav-container {
    padding: 0.75rem 1rem;
  }
  .nav-left {
    gap: 0.5rem;
  }
  .nav-login-btn {
    padding: 0.3rem 0.6rem;
    font-size: 0.75rem;
  }
  .nav-login-full {
    display: none;
  }
  .nav-login-short {
    display: inline;
  }
  .content {
    padding: 12px 8px;
  }
}
</style>

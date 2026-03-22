<template>
  <div class="layout-wrapper" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
    <aside class="layout-sidebar" :class="{ collapsed: sidebarCollapsed, open: sidebarOpen }">
      <div class="sidebar-header">
        <button v-if="!sidebarCollapsed" class="sidebar-logo logo-collapse" @click="sidebarCollapsed = true" title="Collapse sidebar">
          <img src="/exy.png" alt="" class="logo-icon" />
          <span class="logo-text">exe-ops</span>
        </button>
        <button v-else class="sidebar-logo logo-expand" @click="sidebarCollapsed = false" title="Expand sidebar">
          <img src="/exy.png" alt="exe-ops" class="logo-icon" />
        </button>
        <button v-if="!sidebarCollapsed" class="collapse-btn" @click="sidebarCollapsed = !sidebarCollapsed" title="Collapse sidebar">
          <i class="pi pi-angle-left"></i>
        </button>
      </div>
      <nav class="sidebar-nav">
        <div class="nav-section-label" v-if="!sidebarCollapsed">
          <span class="section-prefix">//</span> ops
        </div>
        <ul>
          <li>
            <router-link to="/" class="nav-item" :class="{ active: $route.name === 'dashboard' }" :title="sidebarCollapsed ? 'Dashboard' : undefined">
              <i class="pi pi-objects-column nav-icon icon-cyan"></i>
              <span class="sidebar-label">Dashboard</span>
            </router-link>
          </li>
          <li>
            <router-link to="/deploy" class="nav-item" :class="{ active: $route.name === 'deploy' }" :title="sidebarCollapsed ? 'Deploy' : undefined">
              <i class="pi pi-upload nav-icon icon-cyan"></i>
              <span class="sidebar-label">Deploy</span>
            </router-link>
          </li>
        </ul>
      </nav>
      <div class="sidebar-footer">
        <div class="theme-toggle" :title="sidebarCollapsed ? 'Theme: ' + themePreference : undefined">
          <button
            class="theme-btn"
            :class="{ active: themePreference === 'system' }"
            @click="themePreference = 'system'"
            title="System theme"
          >
            <i class="pi pi-desktop"></i>
          </button>
          <button
            class="theme-btn"
            :class="{ active: themePreference === 'light' }"
            @click="themePreference = 'light'"
            title="Light theme"
          >
            <i class="pi pi-sun"></i>
          </button>
          <button
            class="theme-btn"
            :class="{ active: themePreference === 'dark' }"
            @click="themePreference = 'dark'"
            title="Dark theme"
          >
            <i class="pi pi-moon"></i>
          </button>
        </div>
      </div>
    </aside>

    <div class="layout-content-wrapper">
      <header class="mobile-topbar">
        <button class="mobile-menu-btn" @click="sidebarOpen = true" aria-label="Open navigation">
          <i class="pi pi-bars"></i>
        </button>
        <span class="mobile-title"><img src="/exy.png" alt="" class="mobile-logo-icon" /> exe-ops</span>
      </header>
      <main class="layout-content">
        <router-view />
      </main>
      <footer class="version-footer" v-if="serverVersion">
        <span class="version-text">exe-ops {{ serverVersion.version }}</span>
        <span class="version-detail" v-if="serverVersion.commit !== 'unknown'">{{ serverVersion.commit.slice(0, 7) }}</span>
        <span class="version-detail" v-if="serverVersion.date !== 'unknown'">{{ serverVersion.date }}</span>
      </footer>
    </div>

    <div class="layout-mask" :class="{ active: sidebarOpen }" @click="sidebarOpen = false"></div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { fetchServerVersion, type ServerVersion } from './api/client'

const route = useRoute()
const sidebarOpen = ref(false)
const serverVersion = ref<ServerVersion | null>(null)

// Close mobile sidebar on navigation
watch(() => route.path, () => {
  sidebarOpen.value = false
})
const sidebarCollapsed = ref(localStorage.getItem('sidebar-collapsed') === 'true')

watch(sidebarCollapsed, (v) => {
  localStorage.setItem('sidebar-collapsed', String(v))
})

// Theme management
type ThemePreference = 'system' | 'light' | 'dark'
const themePreference = ref<ThemePreference>(
  (localStorage.getItem('theme-preference') as ThemePreference) || 'system'
)

const systemPrefersDark = ref(window.matchMedia('(prefers-color-scheme: dark)').matches)
const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')

function onSystemThemeChange(e: MediaQueryListEvent) {
  systemPrefersDark.value = e.matches
}

onMounted(() => {
  mediaQuery.addEventListener('change', onSystemThemeChange)
  fetchServerVersion().then(v => { serverVersion.value = v }).catch(() => {})
})

onUnmounted(() => {
  mediaQuery.removeEventListener('change', onSystemThemeChange)
})

const effectiveTheme = computed(() => {
  if (themePreference.value === 'system') {
    return systemPrefersDark.value ? 'dark' : 'light'
  }
  return themePreference.value
})

watch(themePreference, (v) => {
  localStorage.setItem('theme-preference', v)
})

watch(effectiveTheme, (theme) => {
  document.documentElement.classList.toggle('light-mode', theme === 'light')
}, { immediate: true })
</script>

<style>
/* ── Reset & Base ── */
*,
*::before,
*::after {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

:root {
  --sidebar-width: 224px;
  --sidebar-collapsed-width: 56px;

  /* Background palette */
  --surface-ground: #080c10;
  --surface-section: #0d1117;
  --surface-card: #161b22;
  --surface-overlay: #1c2128;
  --surface-border: #30363d;
  --surface-border-bright: #484f58;
  --surface-hover: #1c2128;

  /* Text */
  --text-color: #e6edf3;
  --text-color-secondary: #8b949e;
  --text-color-muted: #6e7681;

  /* Primary (cyan) */
  --primary-color: #48d1cc;
  --primary-color-text: #000000;
  --primary-50: rgba(72, 209, 204, 0.08);
  --primary-100: rgba(72, 209, 204, 0.16);
  --primary-hover: #7ee8e4;

  /* Semantic colors */
  --green-400: #3fb950;
  --green-500: #3fb950;
  --green-subtle: rgba(63, 185, 80, 0.12);
  --red-400: #f85149;
  --red-500: #f85149;
  --red-subtle: rgba(248, 81, 73, 0.12);
  --yellow-400: #d29922;
  --yellow-500: #d29922;
  --yellow-subtle: rgba(210, 153, 34, 0.12);
  --blue-400: #58a6ff;
  --blue-500: #58a6ff;

  /* Sidebar */
  --sidebar-bg: #161b22;
  --sidebar-item-hover: rgba(255, 255, 255, 0.04);
  --sidebar-item-active-bg: #1c2128;
  --sidebar-item-active-text: var(--text-color);
}

/* ── Light mode overrides ── */
:root.light-mode {
  --surface-ground: #eef1f5;
  --surface-section: #f0f3f6;
  --surface-card: #ffffff;
  --surface-overlay: #e8ebef;
  --surface-border: #c5cdd6;
  --surface-border-bright: #a0aab5;
  --surface-hover: #e8ebef;

  --text-color: #1f2328;
  --text-color-secondary: #4d5562;
  --text-color-muted: #6e7781;

  --primary-color: #0f9690;
  --primary-color-text: #ffffff;
  --primary-50: rgba(15, 150, 144, 0.08);
  --primary-100: rgba(15, 150, 144, 0.16);
  --primary-hover: #0c7a75;

  --green-400: #1a7f37;
  --green-500: #1a7f37;
  --green-subtle: rgba(26, 127, 55, 0.10);
  --red-400: #cf222e;
  --red-500: #cf222e;
  --red-subtle: rgba(207, 34, 46, 0.10);
  --yellow-400: #9a6700;
  --yellow-500: #9a6700;
  --yellow-subtle: rgba(154, 103, 0, 0.10);
  --blue-400: #0969da;
  --blue-500: #0969da;

  --sidebar-bg: #f0f2f5;
  --sidebar-item-hover: rgba(0, 0, 0, 0.04);
  --sidebar-item-active-bg: #e2e5e9;
  --sidebar-item-active-text: var(--text-color);
}

html, body {
  height: 100%;
}

body {
  font-family: 'Monda', 'JetBrains Mono', sans-serif;
  font-size: 13px;
  line-height: 1.6;
  background: var(--surface-ground);
  color: var(--text-color);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  /* Subtle grid pattern */
  background-image:
    linear-gradient(rgba(72, 209, 204, 0.03) 1px, transparent 1px),
    linear-gradient(90deg, rgba(72, 209, 204, 0.03) 1px, transparent 1px);
  background-size: 24px 24px;
}

.light-mode body {
  background-image:
    linear-gradient(rgba(15, 150, 144, 0.04) 1px, transparent 1px),
    linear-gradient(90deg, rgba(15, 150, 144, 0.04) 1px, transparent 1px);
}

.light-mode ::selection {
  background: rgba(15, 150, 144, 0.25);
}

h1, h2, h3, h4, h5, h6 {
  font-weight: 500;
  letter-spacing: -0.02em;
}

a {
  color: var(--primary-color);
  text-decoration: none;
  transition: color 0.2s;
}

a:hover {
  color: var(--primary-hover);
}

/* Scrollbar styling */
::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

::-webkit-scrollbar-track {
  background: var(--surface-section);
}

::-webkit-scrollbar-thumb {
  background: var(--surface-border);
  border-radius: 4px;
}

::-webkit-scrollbar-thumb:hover {
  background: var(--surface-border-bright);
}

/* Selection styling */
::selection {
  background: rgba(72, 209, 204, 0.3);
  color: var(--text-color);
}

/* ── Layout ── */
.layout-wrapper {
  display: flex;
  min-height: 100vh;
}

/* ── Sidebar ── */
.layout-sidebar {
  width: var(--sidebar-width);
  height: 100vh;
  height: 100dvh;
  background: var(--sidebar-bg);
  border-right: 1px solid var(--surface-border);
  display: flex;
  flex-direction: column;
  position: fixed;
  top: 0;
  left: 0;
  bottom: 0;
  z-index: 100;
  overflow: hidden;
  transition: width 0.2s, transform 0.2s;
}

.layout-sidebar.collapsed {
  width: var(--sidebar-collapsed-width);
}

.layout-sidebar.collapsed .sidebar-label {
  display: none;
}

.layout-sidebar.collapsed .sidebar-header {
  justify-content: center;
}

.sidebar-header {
  height: 56px;
  padding: 0 0.75rem;
  border-bottom: 1px solid var(--surface-border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  flex-shrink: 0;
}

.sidebar-logo {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  color: var(--text-color);
  text-decoration: none;
  font-weight: 600;
  letter-spacing: 0.05em;
  overflow: hidden;
  transition: opacity 0.2s;
  min-width: 0;
}

.logo-expand,
.logo-collapse {
  border: none;
  background: none;
  cursor: pointer;
  padding: 0;
}

.sidebar-logo:hover {
  opacity: 0.8;
  color: var(--text-color);
}

.logo-caret {
  color: var(--primary-color);
  flex-shrink: 0;
  font-size: 1rem;
}

.logo-text {
  font-size: 1rem;
  text-transform: uppercase;
  white-space: nowrap;
}

.logo-icon {
  width: 24px;
  height: 24px;
  object-fit: contain;

}

.sidebar-nav {
  padding: 0.75rem 0.5rem;
  flex: 1;
  overflow-y: auto;
  min-height: 0;
}

.sidebar-nav ul {
  list-style: none;
  padding: 0;
}

.nav-section-label {
  font-size: 0.65rem;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.15em;
  color: var(--text-color-muted);
  padding: 0 0.5rem;
  margin-bottom: 0.5rem;
  display: flex;
  align-items: center;
  gap: 0.375rem;
}

.section-prefix {
  color: var(--primary-color);
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem;
  border-radius: 4px;
  border: 1px solid transparent;
  color: var(--text-color-secondary);
  text-decoration: none;
  font-size: 0.8rem;
  font-weight: 400;
  transition: all 0.15s;
  margin-bottom: 2px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.nav-icon {
  font-size: 0.9rem;
  width: 16px;
  text-align: center;
  flex-shrink: 0;
}

.icon-cyan { color: var(--primary-color); }
.icon-green { color: var(--green-400); }
.icon-red { color: var(--red-400); }
.icon-yellow { color: var(--yellow-400); }

.nav-item:hover {
  background: rgba(255, 255, 255, 0.03);
  border-color: var(--surface-border);
  color: var(--text-color);
}

.nav-item.active {
  background: var(--surface-overlay);
  border-color: var(--surface-border);
  color: var(--text-color);
}

/* ── Collapse button ── */
.collapse-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 4px;
  border: none;
  background: none;
  color: var(--text-color-secondary);
  font-size: 0.85rem;
  cursor: pointer;
  flex-shrink: 0;
  transition: color 0.15s;
}

.collapse-btn:hover {
  color: var(--text-color);
}

/* ── Content ── */
.layout-content-wrapper {
  margin-left: var(--sidebar-width);
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  min-width: 0;
  overflow-x: hidden;
  background: var(--surface-section);
  transition: margin-left 0.2s;
}

.sidebar-collapsed .layout-content-wrapper {
  margin-left: var(--sidebar-collapsed-width);
}

/* ── Main Content ── */
.layout-content {
  flex: 1;
  padding: 1.5rem 2rem;
  max-width: 1400px;
  margin: 0 auto;
  width: 100%;
  min-width: 0;
}

/* ── Version Footer ── */
.version-footer {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  padding: 0.6rem 1rem;
  font-size: 0.7rem;
  color: var(--text-color-muted);
  border-top: 1px solid var(--surface-border);
  flex-shrink: 0;
}

.version-text {
  font-weight: 600;
}

.version-detail {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.65rem;
  opacity: 0.7;
}

/* ── Overlay for mobile sidebar ── */
.layout-mask {
  display: none;
}

/* ── Sidebar footer / theme toggle ── */
.sidebar-footer {
  padding: 0.5rem;
  border-top: 1px solid var(--surface-border);
  flex-shrink: 0;
}

.theme-toggle {
  display: flex;
  gap: 2px;
  background: var(--surface-ground);
  border-radius: 6px;
  padding: 3px;
}

.layout-sidebar.collapsed .theme-toggle {
  flex-direction: column;
}

.theme-btn {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0.35rem;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-color-muted);
  font-size: 0.8rem;
  cursor: pointer;
  transition: all 0.15s;
}

.theme-btn:hover {
  color: var(--text-color-secondary);
  background: var(--sidebar-item-hover);
}

.theme-btn.active {
  color: var(--primary-color);
  background: var(--surface-card);
}

/* ── Light mode nav-item adjustments ── */
.light-mode .nav-item:hover {
  background: rgba(0, 0, 0, 0.04);
}

.light-mode .nav-item.active {
  background: var(--sidebar-item-active-bg);
}

.light-mode .layout-mask.active {
  background: rgba(0, 0, 0, 0.3);
}

/* ── Mobile top bar ── */
.mobile-topbar {
  display: none;
}

/* ── Responsive ── */
@media (max-width: 991px) {
  .mobile-topbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    height: 48px;
    padding: 0 1rem;
    background: var(--sidebar-bg);
    border-bottom: 1px solid var(--surface-border);
    flex-shrink: 0;
  }

  .mobile-menu-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: none;
    border-radius: 4px;
    background: none;
    color: var(--text-color-secondary);
    font-size: 1.1rem;
    cursor: pointer;
    transition: color 0.15s;
  }

  .mobile-menu-btn:hover {
    color: var(--text-color);
  }

  .mobile-title {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: 600;
    font-size: 0.9rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-color);
  }

  .mobile-logo-icon {
    width: 22px;
    height: 22px;
    object-fit: contain;
  }

  .layout-sidebar {
    transform: translateX(-100%);
    width: var(--sidebar-width);
  }

  .layout-sidebar.open {
    transform: translateX(0);
  }

  .layout-sidebar.collapsed {
    width: var(--sidebar-width);
  }

  .layout-sidebar.collapsed .sidebar-label {
    display: inline;
  }

  .layout-sidebar.collapsed .sidebar-header {
    justify-content: space-between;
  }

  .layout-content-wrapper {
    margin-left: 0;
  }

  .sidebar-collapsed .layout-content-wrapper {
    margin-left: 0;
  }

  .layout-mask.active {
    display: block;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 99;
  }

  .layout-content {
    padding: 1rem;
  }
}
</style>

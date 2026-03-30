// Lightweight entry for simple server-rendered pages.
// Reads page data from window.__PAGE__ and mounts a Vue app.
import { createApp, type Component } from 'vue'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/600.css'
import PrimeVue from 'primevue/config'
import Aura from '@primevue/themes/aura'
import { definePreset } from '@primevue/themes'

const ExePreset = definePreset(Aura, {
  semantic: {
    primary: {
      50: '{teal.50}',
      100: '{teal.100}',
      200: '{teal.200}',
      300: '{teal.300}',
      400: '{teal.400}',
      500: '{teal.500}',
      600: '{teal.600}',
      700: '{teal.700}',
      800: '{teal.800}',
      900: '{teal.900}',
      950: '{teal.950}',
    },
  },
})

// Inject base styles shared by all simple pages
const style = document.createElement('style')
style.textContent = `
  :root {
    --font-mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
    --text-color: #1a1a1a;
    --text-color-secondary: #555;
    --text-color-muted: #717171;
    --surface-ground: #fafafa;
    --surface-card: #fff;
    --surface-subtle: #f3f4f6;
    --surface-border: #e0e0e0;
    --danger-color: #dc2626;
    --success-color: #22c55e;
    --link-color: #0d9488;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --text-color: #f3f4f6;
      --text-color-secondary: #b0b8c4;
      --text-color-muted: #8b95a3;
      --surface-ground: #111;
      --surface-card: #1f1f1f;
      --surface-subtle: #2a2a2a;
      --surface-border: #333;
      --danger-color: #f87171;
      --success-color: #4ade80;
      --link-color: #5eead4;
    }
  }
  *, *::before, *::after { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    font-family: var(--font-mono);
    font-size: 16px;
    line-height: 1.6;
    background: var(--surface-ground);
    color: var(--text-color);
    -webkit-font-smoothing: antialiased;
  }
  a { color: var(--link-color); text-decoration: none; }
  a:hover { text-decoration: underline; }
`
document.head.appendChild(style)

export function mountPage(component: Component) {
  const app = createApp(component)
  app.use(PrimeVue, {
    theme: {
      preset: ExePreset,
      options: {
        darkModeSelector: 'system',
      },
    },
  })
  app.mount('#app')
}

export function pageData<T>(): T {
  return (window as any).__PAGE__ as T
}

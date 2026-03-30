import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'
import { writeFileSync, mkdirSync } from 'fs'

// Page manifest: each simple page has a title, entry script, and optional extras.
const pages: Record<string, { title: string; extras?: string }> = {
  'app-token-code-entry': { title: 'Enter Code' },
  'app-token-success': { title: 'Signed In', extras: '<script src="/static/passkey.js"></script>' },
  'auth-error': { title: 'Error' },
  'auth-form': { title: 'Sign In', extras: '<script src="/static/passkey.js"></script>' },
  'auth-pow': { title: 'Verifying' },
  'billing-success': { title: 'Payment Info Added' },
  'device-verification': { title: 'Confirm Device' },
  'device-verified': { title: 'Device Verified' },
  'discord-linked': { title: 'Discord Linked' },
  'email-sent': { title: 'Check Your Email' },
  'email-verification-form': { title: 'Confirm Email' },
  'email-verified': { title: 'Email Verified', extras: '<script src="/static/passkey.js"></script>' },
  'github-connected': { title: 'GitHub Connected' },
  'login-confirmation': { title: 'Confirm Login' },
  'oauth-ssh-success': { title: 'Verified' },
  'proxy-logged-out': { title: 'Logged Out' },
}

function pageHTML(name: string, title: string, extras?: string): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <link rel="apple-touch-icon" href="/apple-touch-icon.png">
  <link rel="icon" href="/favicon.ico">
  <meta name="color-scheme" content="light dark">
  <title>${title}</title>
</head>
<body>
  ${extras ? extras + '\n  ' : ''}<div id="app"></div>
  <script type="module" src="/src/pages/${name}.ts"></script>
</body>
</html>`
}

// Generate page HTML files from manifest
mkdirSync(resolve(__dirname, 'pages'), { recursive: true })
for (const [name, { title, extras }] of Object.entries(pages)) {
  writeFileSync(resolve(__dirname, `pages/${name}.html`), pageHTML(name, title, extras) + '\n')
}

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    reportCompressedSize: false,
    target: 'esnext',
    rollupOptions: {
      input: {
        main: resolve(__dirname, 'index.html'),
        ...Object.fromEntries(
          Object.keys(pages).map(name => [name, resolve(__dirname, `pages/${name}.html`)])
        ),
      },
      plugins: [{
        name: 'restore-gitkeep',
        writeBundle() {
          writeFileSync(resolve(__dirname, 'dist/.gitkeep'), '')
        },
      }],
    },
  },
  server: {
    port: 8000,
    proxy: {
      '/cmd': 'http://localhost:8080',
      '/github': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
      '/logout': 'http://localhost:8080',
      '/api': 'http://localhost:8080',
      '/creating/stream': 'http://localhost:8080',
      '/box/creation-log': 'http://localhost:8080',
      '/check-hostname': 'http://localhost:8080',
      '/create-vm': 'http://localhost:8080',
    },
  },
})

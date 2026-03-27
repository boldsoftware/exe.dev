import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'
import { writeFileSync } from 'fs'

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
    // Restore .gitkeep after emptyOutDir deletes it
    rollupOptions: {
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

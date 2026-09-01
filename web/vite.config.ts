import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2022',
    chunkSizeWarningLimit: 550,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/assets/flags': 'http://127.0.0.1:8080',
      '/assets/logo': 'http://127.0.0.1:8080',
      '/install.sh': 'http://127.0.0.1:8080',
      '/install.ps1': 'http://127.0.0.1:8080',
    },
  },
})

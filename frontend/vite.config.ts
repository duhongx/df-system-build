import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  build: {
    chunkSizeWarningLimit: 900,
    rollupOptions: {
      onwarn(warning, warn) {
        if (
          warning.code === 'INVALID_ANNOTATION' &&
          typeof warning.id === 'string' &&
          warning.id.includes('/node_modules/@vueuse/core/')
        ) {
          return
        }
        warn(warning)
      },
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) {
            return undefined
          }
          if (id.includes('/node_modules/xterm') || id.includes('/node_modules/xterm-addon-fit')) {
            return 'xterm'
          }
          if (id.includes('/node_modules/element-plus/') || id.includes('/node_modules/@element-plus/')) {
            return 'element-plus'
          }
          if (id.includes('/node_modules/@vueuse/')) {
            return 'vueuse'
          }
          if (
            id.includes('/node_modules/vue/') ||
            id.includes('/node_modules/vue-router/') ||
            id.includes('/node_modules/pinia/')
          ) {
            return 'vue-vendor'
          }
          return 'vendor'
        },
      },
    },
  },
  server: {
    port: 3001,
    host: '0.0.0.0',
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})

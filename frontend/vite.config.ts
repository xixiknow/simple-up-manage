import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'

export default defineConfig({
  plugins: [
    vue(),
    AutoImport({
      dts: 'src/auto-imports.d.ts',
      imports: [
        'vue',
        'vue-router',
        'pinia',
        {
          '@/components/ui': ['useDialog', 'useMessage'],
        },
      ],
    }),
    Components({
      dts: 'src/components.d.ts',
      resolvers: [(name) => name.startsWith('Ui') ? { name, from: '@/components/ui' } : undefined],
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Only `/api/` (with slash) so the SPA route `/api-keys` is not
      // forwarded to the backend on hard refresh.
      '/api/': {
        target: process.env.VITE_BACKEND_ORIGIN || 'http://localhost:8080',
        changeOrigin: true,
        timeout: 0,
        proxyTimeout: 0,
      },
      '/health': process.env.VITE_BACKEND_ORIGIN || 'http://localhost:8080',
    },
  },
})

import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    rolldownOptions: {
      output: {
        // Libraries change less often than the panel's own code; separate
        // files let the browser keep them cached across updates. Pages are
        // split off by the dynamic imports in each module's `page`.
        codeSplitting: {
          groups: [
            { name: 'react', test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/, priority: 2 },
            // Only the parts needed for the first screen; the rest travels with the pages.
            { name: 'ui-sdk', test: /node_modules[\\/]@ui-sdk[\\/]/, tags: ['$initial'], priority: 1 },
          ],
        },
      },
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8600',
    },
  },
  test: {
    environment: 'node',
  },
})

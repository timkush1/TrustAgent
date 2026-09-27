import { fileURLToPath } from 'node:url'
import { searchForWorkspaceRoot } from 'vite'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const fromHere = (path: string) => fileURLToPath(new URL(path, import.meta.url))

// Shared contract and benchmark files that live outside frontend-react/
// (docs/v2/SPEC.md §2.13, §3.3).
const contractsDir = fromHere('../contracts')
const benchDir = fromHere('../docs/benchmarks')

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // Keep in sync with compilerOptions.paths in tsconfig.app.json.
    alias: {
      '@': fromHere('./src'),
      '@contracts': contractsDir,
      '@bench': benchDir,
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
  },
  server: {
    fs: {
      // Setting `allow` replaces Vite's default (the workspace root), so keep it
      // and add only the two aliased directories that live outside it.
      allow: [searchForWorkspaceRoot(fromHere('.')), contractsDir, benchDir],
    },
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
})

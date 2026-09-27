import { fileURLToPath } from 'node:url'
import { searchForWorkspaceRoot } from 'vite'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'

const fromHere = (path: string) => fileURLToPath(new URL(path, import.meta.url))

// Shared contract and benchmark files that live outside frontend-react/
// (docs/v2/SPEC.md §2.13, §3.3).
const contractsDir = fromHere('../contracts')
const benchDir = fromHere('../docs/benchmarks')

// The proxy the dev server forwards to: the local stack's published proxy port
// (PROXY_HOST_PORT, contracts/config/keys.v1.json; default 8080).
const devProxyTarget = `http://localhost:${process.env.PROXY_HOST_PORT || '8080'}`

// https://vite.dev/config/
export default defineConfig({
  // VITE_BASE_PATH: "/" for the dashboard image, "/TrustAgent/" for GitHub Pages.
  base: process.env.VITE_BASE_PATH ?? '/',
  plugins: [
    // File routes under src/routes; generates src/routeTree.gen.ts (committed, because
    // `tsc -b` runs before `vite build`). Must come before react().
    tanstackRouter({ target: 'react', autoCodeSplitting: true, quoteStyle: 'single' }),
    react(),
    tailwindcss(),
  ],
  build: {
    rolldownOptions: {
      output: {
        // Framework code changes far less often than the app: separate chunks stay cached
        // across deploys.
        codeSplitting: {
          groups: [
            { name: 'react', test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/ },
            { name: 'tanstack', test: /node_modules[\\/]@tanstack[\\/]/ },
          ],
        },
      },
    },
  },
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
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['src/test/setup.ts'],
  },
  server: {
    fs: {
      // Setting `allow` replaces Vite's default (the workspace root), so keep it
      // and add only the two aliased directories that live outside it.
      allow: [searchForWorkspaceRoot(fromHere('.')), contractsDir, benchDir],
    },
    // Same paths as production (no rewrite): /api/*, /v1/*, /health* and /ws live on the proxy.
    proxy: {
      '/api': { target: devProxyTarget, changeOrigin: true },
      '/v1': { target: devProxyTarget, changeOrigin: true },
      '/health': { target: devProxyTarget, changeOrigin: true },
      '/ws': { target: devProxyTarget.replace(/^http/, 'ws'), ws: true },
    },
  },
})

import { fileURLToPath } from 'node:url'
import { searchForWorkspaceRoot } from 'vite'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { copyModules, scenarioModules, sitePages, type AppMode } from './vite.site.ts'

const fromHere = (path: string) => fileURLToPath(new URL(path, import.meta.url))

// Shared contract and benchmark files that live outside frontend-react/
// (docs/v2/SPEC.md §2.13, §3.3).
const contractsDir = fromHere('../contracts')
const benchDir = fromHere('../docs/benchmarks')

// The proxy the dev server forwards to: the local stack's published proxy port
// (PROXY_HOST_PORT, contracts/config/keys.v1.json; default 8080).
const devProxyTarget = `http://localhost:${process.env.PROXY_HOST_PORT || '8080'}`

// https://vite.dev/config/
export default defineConfig(({ mode, isPreview }) => {
  // VITE_APP_MODE (contracts/config/keys.v1.json): `full` dashboard or the `static` GitHub Pages site.
  // `vite build --mode static` (npm run build:static) implies it; an explicit env var wins. Setting
  // it here, before Vite reads the environment, also exposes it as import.meta.env.VITE_APP_MODE.
  if (mode === 'static' && !process.env.VITE_APP_MODE) process.env.VITE_APP_MODE = 'static'
  const appMode: AppMode = process.env.VITE_APP_MODE === 'static' ? 'static' : 'full'

  return {
    // VITE_BASE_PATH: "/" for the dashboard image, "/TrustAgent/" for GitHub Pages (the static default).
    base: process.env.VITE_BASE_PATH ?? (appMode === 'static' ? '/TrustAgent/' : '/'),
    // A static preview behaves like GitHub Pages (no SPA fallback; 404.html boots the app).
    appType: isPreview && appMode === 'static' ? 'mpa' : 'spa',
    plugins: [
      // File routes under src/routes; generates src/routeTree.gen.ts (committed, because
      // `tsc -b` runs before `vite build`). Must come before react().
      tanstackRouter({ target: 'react', autoCodeSplitting: true, quoteStyle: 'single' }),
      react(),
      tailwindcss(),
      scenarioModules(contractsDir),
      copyModules(contractsDir),
      ...sitePages(appMode),
    ],
    build: {
      // .vite/manifest.json: what each entry loads (scripts/check-size.ts reads it).
      manifest: true,
      // Fonts stay files: the static site's CSP (default-src 'self', no font-src data:) refuses data: URIs.
      assetsInlineLimit: (file: string) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
      // Every supported browser has <link rel=modulepreload>; the polyfill only costs bytes.
      modulePreload: { polyfill: false },
      rolldownOptions: {
        // Two pages: the app, and the landing page (its own entry: no router, no charts, SPEC §3.3).
        input: { app: fromHere('./index.html'), landing: fromHere('./landing.html') },
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
  }
})

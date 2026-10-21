/**
 * Boot: theme → runtime config (/config.json) → services → live stream → router. The static build
 * (VITE_APP_MODE=static) swaps the gateway for ReplayDataSource, which replays the dataset the build
 * copied to demo/ (D110). The stream starts here, outside React, so StrictMode and remounts cannot
 * open a second socket; the full dashboard opens it only once this browser may read (CO-10). The
 * static PKCE callback (D105) boots with neither the recording nor the stream.
 */
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import './index.css';

import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { expected as expectedDataset } from 'virtual:trustagent/dataset';
import { idleDataSource, isStaticCallbackPath } from './app/boot';
import { ServicesContext, type AppServices } from './app/services';
import { loadRuntimeConfig } from './config/runtime';
import { LiveDataSource } from './data/live/liveDataSource';
import type { DataSource } from './data/source';
import { startLiveStream } from './data/liveStream';
import { createQueryClient } from './data/queries';
import { initTheme } from './lib/theme';
import { createAppRouter } from './router';

async function boot(): Promise<void> {
  initTheme();
  const config = await loadRuntimeConfig();
  for (const warning of config.warnings) console.warn(`[TrustAgent] ${warning}`);

  const queryClient = createQueryClient();
  // The static PKCE callback takes the code out of the address bar before anything else reaches the
  // network: no recording, no replay stream (D105).
  const callbackOnly = config.appMode === 'static' && isStaticCallbackPath(window.location.pathname, config.basePath);
  let dataSource: DataSource;
  if (callbackOnly) {
    dataSource = idleDataSource();
  } else if (config.appMode === 'static') {
    // GitHub Pages: replay the dataset the build chose (SPEC §3.3, D110). Loaded only by the static build.
    const { createReplayDataSource } = await import('./data/replay/replayDataSource');
    dataSource = createReplayDataSource({ basePath: config.basePath, dataset: expectedDataset });
  } else {
    dataSource = new LiveDataSource(config);
  }
  const services: AppServices = { config, dataSource, queryClient };
  if (!callbackOnly) startLiveStream(dataSource, queryClient, { gateOnReadAccess: config.appMode === 'full' });
  const router = createAppRouter(services);

  const root = document.getElementById('root');
  if (!root) throw new Error('index.html has no #root element');
  createRoot(root).render(
    <StrictMode>
      <ServicesContext.Provider value={services}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ServicesContext.Provider>
    </StrictMode>,
  );
}

void boot();

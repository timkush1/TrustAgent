/**
 * Boot: theme → runtime config (/config.json) → services → live stream → router.
 * The stream starts here, outside React, so StrictMode and remounts cannot open a second socket.
 */
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import './index.css';

import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { ServicesContext, type AppServices } from './app/services';
import { loadRuntimeConfig } from './config/runtime';
import { LiveDataSource } from './data/live/liveDataSource';
import { startLiveStream } from './data/liveStream';
import { createQueryClient } from './data/queries';
import { initTheme } from './lib/theme';
import { createAppRouter } from './router';

async function boot(): Promise<void> {
  initTheme();
  const config = await loadRuntimeConfig();
  for (const warning of config.warnings) console.warn(`[TrustAgent] ${warning}`);

  const queryClient = createQueryClient();
  const dataSource = new LiveDataSource(config);
  const services: AppServices = { config, dataSource, queryClient };
  startLiveStream(dataSource, queryClient);
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

/**
 * The landing page entry (SPEC §3.3: a separate Vite entry with no router or chart libraries, under
 * a 110 KB gzip budget that `npm run size` enforces). Fonts are self-hosted like the dashboard's.
 */
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import './landing.css';

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { initTheme } from '@/lib/theme';
import { Landing } from './Landing';

initTheme();
const root = document.getElementById('root');
if (!root) throw new Error('landing.html has no #root element');
createRoot(root).render(
  <StrictMode>
    <Landing />
  </StrictMode>,
);

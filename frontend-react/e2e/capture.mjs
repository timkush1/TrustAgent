// Verification screenshot capture (Phase A2). Drives the running dashboard
// with headless Chromium and captures each tab. Not the final README assets —
// those are re-shot with the cloud judge model once configured.
import { chromium } from '@playwright/test';

const BASE = process.env.BASE_URL || 'http://localhost:5173';
const OUT = process.env.OUT_DIR || '/tmp/shots';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1512, height: 945 }, deviceScaleFactor: 2 });
page.on('console', (m) => { if (m.type() === 'error') console.log('PAGE-ERR:', m.text().slice(0, 200)); });

await page.goto(BASE, { waitUntil: 'networkidle', timeout: 30000 });
await sleep(1500);
await page.screenshot({ path: `${OUT}/01-dashboard-live.png` });
console.log('shot 01 dashboard');

// History tab (Postgres-backed; has real audits)
await page.getByRole('tab', { name: 'History' }).click();
await sleep(2500);
await page.screenshot({ path: `${OUT}/02-history.png` });
console.log('shot 02 history');

// Expand the first history row if expandable
try {
  const firstRow = page.locator('tbody tr').first();
  await firstRow.click({ timeout: 3000 });
  await sleep(1200);
  await page.screenshot({ path: `${OUT}/03-history-expanded.png` });
  console.log('shot 03 history-expanded');
} catch (e) { console.log('no expandable row:', String(e).split('\n')[0]); }

// Knowledge Base tab (Qdrant-backed; 24 seeded docs)
await page.getByRole('tab', { name: 'Knowledge Base' }).click();
await sleep(2500);
await page.screenshot({ path: `${OUT}/04-kb.png` });
console.log('shot 04 kb');

await browser.close();
console.log('DONE');

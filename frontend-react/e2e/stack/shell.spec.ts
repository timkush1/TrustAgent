/**
 * The app shell on a running stack: navigation, placeholders, Health pill, theme, and no serious
 * accessibility violations on any route in either theme. Needs no traffic.
 */
import { expect, test } from '@playwright/test';
import { seriousViolations } from './stack';

const ROUTES = [
  { path: '/app/overview', title: 'Overview' },
  { path: '/app/traffic', title: 'Traffic' },
  { path: '/app/evaluators', title: 'Evaluators' },
  { path: '/app/policies', title: 'Policies' },
  { path: '/app/playground', title: 'Playground' },
  { path: '/app/settings/connections', title: 'Settings' },
  { path: '/app/setup', title: 'Setup' },
];

test.describe('shell', () => {
  test('opens on Overview with the six navigation items', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/app\/overview$/);
    const nav = page.getByRole('navigation', { name: 'Main' });
    for (const name of ['Overview', 'Traffic', 'Evaluators', 'Policies', 'Playground', 'Settings']) {
      await expect(nav.getByRole('link', { name })).toBeVisible();
    }
    await expect(page.getByRole('button', { name: /System health/ })).toBeVisible();
  });

  test('the Health pill opens its checklist', async ({ page }) => {
    await page.goto('/app/overview');
    await page.getByRole('button', { name: /System health/ }).click();
    await expect(page.getByRole('dialog').getByText('Proxy')).toBeVisible();
  });

  test('the theme toggle switches data-theme without a reload', async ({ page }) => {
    await page.goto('/app/overview');
    const html = page.locator('html');
    const toggle = page.getByRole('button', { name: /^Theme:/ });
    await page.evaluate(() => localStorage.setItem('trustagent.theme', 'light'));
    await page.reload();
    await expect(html).toHaveAttribute('data-theme', 'light');
    await toggle.click();
    await expect(html).toHaveAttribute('data-theme', 'dark');
  });

  for (const theme of ['light', 'dark'] as const) {
    test(`every page has a title and no serious a11y violations (${theme})`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('trustagent.theme', t), theme);
      for (const route of ROUTES) {
        await page.goto(route.path);
        await expect(page.getByRole('heading', { level: 1 })).toHaveText(route.title);
        expect(await seriousViolations(page), route.path).toEqual([]);
      }
    });
  }
});

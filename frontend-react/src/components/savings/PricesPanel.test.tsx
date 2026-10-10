/**
 * The price overrides (Settings → Budgets, PricesPanel): a saved price far above the built-in one
 * comes back as a warning, and the panel shows it on the row it is about (CO-7), in the contract's
 * words, with the ratio and the built-in price the gateway sent.
 */
import { configure, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import farAbove from '@contracts/fixtures/api/SettingsView.price-far-above.json';
import { copy, fillTemplate } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';
import type { SettingsView } from '@/types/api';

installMswServer();
warmRoutes('/app/settings/budgets');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const saved = farAbove as unknown as SettingsView;

describe('PricesPanel warnings', { timeout: 20_000 }, () => {
  it('shows a price far above the built-in one on its own row, in the contract’s words', async () => {
    const user = userEvent.setup();
    renderPage('/app/settings/budgets');
    // A second row first, so the warning has a row to stay off.
    await user.click(await screen.findByRole('button', { name: 'Add a price' }));
    await user.click(screen.getByRole('button', { name: 'Add a price' }));
    const fill = async (index: number, provider: string, model: string, input: string) => {
      const row = document.querySelector(`[data-override-row="${index}"]`) as HTMLElement;
      await user.clear(within(row).getByRole('combobox', { name: 'Provider' }));
      await user.type(within(row).getByRole('combobox', { name: 'Provider' }), provider);
      await user.type(within(row).getByRole('textbox', { name: /^Model/ }), model);
      await user.type(within(row).getByRole('textbox', { name: 'Input, USD per million tokens' }), input);
      await user.type(within(row).getByRole('textbox', { name: 'Output, USD per million tokens' }), '5');
    };
    await fill(0, 'anthropic', 'claude-haiku-4-5*', '150');
    await fill(1, 'openai', 'gpt-4o*', '2.5');
    server.use(
      http.patch(`${API}/api/settings`, () =>
        HttpResponse.json({
          ...saved,
          runtime: {
            ...saved.runtime,
            pricing_overrides: [
              ...saved.runtime.pricing_overrides,
              { provider: 'openai', match: 'gpt-4o*', input_per_mtok: 2.5, output_per_mtok: 5, label: 'override' },
            ],
          },
        }),
      ),
    );
    await user.click(screen.getByRole('button', { name: 'Save prices' }));

    const words = fillTemplate(copy.settings.price_far_above_contracts, { ratio: '150', contracts_per_mtok: '1' });
    const first = document.querySelector('[data-override-row="0"]') as HTMLElement;
    const warning = await within(first).findByText(words);
    expect(warning.closest('[data-override-warning]')).toHaveTextContent(`Input price: ${words}`);
    // Only that row, and not again under the list.
    expect(within(document.querySelector('[data-override-row="1"]') as HTMLElement).queryByText(words)).toBeNull();
    expect(screen.queryByRole('list', { name: 'Check these prices' })).toBeNull();
    expect(formatViolations(await axeViolations(first))).toEqual([]);

    // An edit moves rows about: the warning goes until the next save checks again.
    await user.type(within(first).getByRole('textbox', { name: 'Input, USD per million tokens' }), '0');
    expect(within(first).queryByText(words)).toBeNull();
  });
});

/**
 * The consent dialog (D114, D101, D105, D106): the four lines with every figure labelled, the worst
 * case equal to priceCall's output, Connect as a button that remembers the verifier before leaving,
 * Paste always there, Connect off with its reason when storage is blocked, only "Open in its own
 * tab" inside a frame, Escape and focus return.
 */
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import prices from '@contracts/pricing/prices.v1.json';
import { playground } from '@/config/runtime';
import { formatUsd } from '@/domain/format';
import { copy } from '@/domain/presentation';
import { priceCall } from '@/lib/decision/cost';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { pageNav } from '@/lib/openrouter/navigate';
import type { PricingTable } from '@/types/api';
import { axeViolations, formatViolations } from '@/test/axe';
import { ConsentDialog } from './ConsentDialog';
import { worstCaseSpend } from './figures';
import { resetKeyCheckForTests } from './keyCheck';

function Harness({ returnTo = 'app/playground/?scenario=refund-contradiction-flag#live' }: { returnTo?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Run the check live
      </button>
      <ConsentDialog open={open} onClose={() => setOpen(false)} returnTo={returnTo} />
    </>
  );
}

async function openDialog() {
  const user = userEvent.setup();
  render(<Harness />);
  await user.click(screen.getByRole('button', { name: 'Run the check live' }));
  const dialog = await screen.findByRole('dialog');
  return { user, dialog };
}

beforeEach(() => {
  resetKeyCheckForTests();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
  vi.spyOn(pageNav, 'assign').mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
});

describe('ConsentDialog', () => {
  it('shows the four consent lines with the per-check cost labelled', async () => {
    const { dialog } = await openDialog();
    const lines = within(within(dialog).getByRole('list')).getAllByRole('listitem');
    expect(lines).toHaveLength(4);
    expect(lines[0]).toHaveTextContent(copy.playground_static.consent[0]);
    expect(lines[1]).toHaveTextContent(`a typical check (about 5,000 tokens of documents) costs about $0.0002`);
    expect(lines[1]).toHaveTextContent(`this demo stops after ${playground.maxCalls} calls to OpenRouter`);
    expect(lines[1].textContent).not.toContain('[estimate]');
    expect(within(lines[1]).getByRole('button', { name: /Estimate: how “One fast check” was produced/ })).toBeInTheDocument();
    // The page it names is a link (opening in a new tab); the words stay the contract's.
    const keysLink = within(lines[3]).getByRole('link', { name: /openrouter\.ai\/settings\/keys/ });
    expect(lines[3].textContent?.replace(keysLink.querySelector('.sr-only')?.textContent ?? '', '')).toBe(
      'Delete the key any time at openrouter.ai/settings/keys.',
    );
  });

  it('labels every dollar figure in the dialog with its value label', async () => {
    const { dialog } = await openDialog();
    const text = dialog.textContent ?? '';
    const figures = text.match(/\$\d[\d,.]*/g) ?? [];
    expect(figures.length).toBe(2);
    expect(dialog.querySelectorAll('[data-value-label]')).toHaveLength(figures.length);
    expect(text).not.toMatch(/\[(estimate|vendor|measured)\]/);
  });

  it('states the worst case as maxCalls × the dearer priceCall at the token cap', async () => {
    const table = prices as unknown as PricingTable;
    const price = (model: string, input: number, output: number) =>
      priceCall({ provider: 'openrouter', model, input_tokens: input, output_tokens: output, reported_cost_usd: null, table }).cost_usd as number;
    const expected =
      playground.maxCalls *
      Math.max(
        price(playground.jevModel, playground.maxTokens, 0),
        price(playground.jevModel, 0, playground.maxTokens),
        price(playground.judgeModel, playground.maxTokens, 0),
        price(playground.judgeModel, 0, playground.maxTokens),
      );
    expect(worstCaseSpend(playground)).toBe(expected);
    const { dialog } = await openDialog();
    const line = within(dialog).getByTestId('consent-worst-case');
    expect(line).toHaveTextContent(`At most ≈ ${formatUsd(expected)} with these caps`);
    expect(within(line).getByRole('button', { name: /Estimate/ })).toBeInTheDocument();
    // It says how the cap is reached, so it does not read as the cost of a typical check.
    expect(within(dialog).getByTestId('consent-worst-case-how')).toHaveTextContent(
      `${playground.maxCalls.toLocaleString('en-US')} calls × up to ${playground.maxTokens.toLocaleString('en-US')} tokens each, all at the dearer model’s price.`,
    );
    // The OpenRouter pages the consent names are links that open in a new tab.
    // (in the consent's last line and the paste hint).
    const keys = within(dialog).getAllByRole('link', { name: /openrouter\.ai\/settings\/keys/ });
    expect(keys).toHaveLength(2);
    for (const link of keys) {
      expect(link).toHaveAttribute('href', 'https://openrouter.ai/settings/keys');
      expect(link).toHaveAttribute('target', '_blank');
      expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    }
  });

  it('omits the worst case, never shows $0, when a model has no price', () => {
    expect(worstCaseSpend({ ...playground, judgeModel: 'anthropic/claude-haiku-unpriced' })).toBeNull();
  });

  it('Connect is a button that remembers the verifier, then leaves for openrouter.ai/auth', async () => {
    const { user, dialog } = await openDialog();
    const connect = within(dialog).getByRole('button', { name: copy.playground_live.connect });
    expect(connect.tagName).toBe('BUTTON');
    await user.click(connect);
    expect(pageNav.assign).toHaveBeenCalledTimes(1);
    const url = new URL(vi.mocked(pageNav.assign).mock.calls[0][0]);
    expect(url.origin + url.pathname).toBe('https://openrouter.ai/auth');
    expect(url.searchParams.get('callback_url')).toBe(`${window.location.origin}/connect/callback/`);
    expect(url.searchParams.get('code_challenge_method')).toBe('S256');
    const pending = browserKeyStore().takePending();
    expect(pending).toMatchObject({ status: 'ok', returnPath: 'app/playground/?scenario=refund-contradiction-flag#live' });
  });

  it('keeps Paste visible next to Connect', async () => {
    const { dialog } = await openDialog();
    expect(within(dialog).getByText(copy.playground_live.paste_key)).toBeInTheDocument();
    const input = within(dialog).getByLabelText('Your OpenRouter key');
    expect(input).toHaveAttribute('type', 'password');
    expect(input).toHaveAttribute('autocomplete', 'off');
    expect(input).toHaveAttribute('spellcheck', 'false');
    expect(input).toHaveAttribute('autocapitalize', 'off');
    expect(input).toHaveAttribute('data-1p-ignore');
    expect(input).toHaveAttribute('data-lpignore', 'true');
    expect(input.closest('form')).toBeNull();
  });

  it('pastes a key: Enter checks it and never navigates', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ data: { limit: 5, limit_remaining: 5 } }), { status: 200, headers: { 'content-type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    const { user, dialog } = await openDialog();
    const before = window.location.href;
    await user.type(within(dialog).getByLabelText('Your OpenRouter key'), 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef{Enter}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(window.location.href).toBe(before);
    expect(pageNav.assign).not.toHaveBeenCalled();
    expect(browserKeyStore().currentKey()).toBe('sk-or-v1-e2e-0123456789abcdef0123456789abcdef');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String((fetchMock.mock.calls[0] as unknown[])[0])).toBe('https://openrouter.ai/api/v1/key');
  });

  it('refuses a key of the wrong shape before any request', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const { user, dialog } = await openDialog();
    await user.type(within(dialog).getByLabelText('Your OpenRouter key'), 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm{Enter}');
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(copy.playground_live.key_invalid);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(browserKeyStore().currentKey()).toBeNull();
    expect(dialog.textContent).not.toContain('cmp0e2eZq7Lw4Rt9Yx2Vb6Nm');
  });

  it('forgets a pasted key OpenRouter refuses and says so', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 401, message: 'No auth credentials found' } }), { status: 401, headers: { 'content-type': 'application/json' } })));
    const { user, dialog } = await openDialog();
    await user.type(within(dialog).getByLabelText('Your OpenRouter key'), 'sk-or-v1-e2e-ffffffffffffffffffffffffffffffff{Enter}');
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(copy.playground_live.errors.unauthorized);
    expect(browserKeyStore().currentKey()).toBeNull();
  });

  it('closes on Escape and returns focus to the trigger', async () => {
    const { user } = await openDialog();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(screen.getByRole('button', { name: 'Run the check live' })).toHaveFocus();
  });

  it('renders only "Open in its own tab" inside a frame (D106)', async () => {
    const top = Object.getOwnPropertyDescriptor(window, 'top');
    Object.defineProperty(window, 'top', { configurable: true, get: () => ({}) });
    try {
      const { dialog } = await openDialog();
      expect(within(dialog).getByText(copy.playground_live.frame_blocked)).toBeInTheDocument();
      expect(within(dialog).getByRole('link', { name: copy.playground_live.open_in_tab })).toHaveAttribute('rel', 'noopener noreferrer');
      expect(within(dialog).queryByLabelText('Your OpenRouter key')).toBeNull();
      expect(within(dialog).queryByRole('button', { name: copy.playground_live.connect })).toBeNull();
    } finally {
      if (top) Object.defineProperty(window, 'top', top);
    }
  });

  it('has no axe violations', async () => {
    await openDialog();
    expect(formatViolations(await axeViolations())).toEqual([]);
  });
});

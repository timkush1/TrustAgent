/**
 * The consent dialog and the live panel when this browser blocks storage (D102). A file of its own: the tab's key
 * store switches to memory for good once storage refuses, so no other test may share its module.
 */
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { copy } from '@/domain/presentation';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { pageNav } from '@/lib/openrouter/navigate';
import { memoryStorage } from '@/lib/openrouter/testing';
import { createUsageStore } from '@/lib/openrouter/usage';
import { ConsentDialog } from './ConsentDialog';
import LivePanel from './LivePanel';

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      <ConsentDialog open={open} onClose={() => setOpen(false)} returnTo="app/playground/#live" />
    </>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ConsentDialog with storage blocked', () => {
  it('turns Connect off with the reason, and Paste still works in memory', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'QuotaExceededError');
    });
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    const assign = vi.spyOn(pageNav, 'assign').mockImplementation(() => {});
    expect(browserKeyStore().getSnapshot().storage_blocked).toBe(true);
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Open' }));
    const dialog = await screen.findByRole('dialog');
    const connect = within(dialog).getByRole('button', { name: copy.playground_live.connect });
    expect(connect).toHaveAttribute('aria-disabled', 'true');
    const reason = document.getElementById(connect.getAttribute('aria-describedby') ?? '');
    expect(reason).toHaveTextContent(copy.playground_live.storage_blocked);
    await user.click(connect);
    expect(assign).not.toHaveBeenCalled();
    expect(within(dialog).getByLabelText('Your OpenRouter key')).toBeInTheDocument();
    expect(browserKeyStore().set('sk-or-v1-e2e-0123456789abcdef0123456789abcdef')).toBe(true);
    expect(browserKeyStore().getSnapshot().state).toBe('memory');
  });

  it('the live panel says the key lives in memory only, and still runs', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'QuotaExceededError');
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ data: { limit: 5, limit_remaining: 5 } }), { status: 200, headers: { 'content-type': 'application/json' } })),
    );
    browserKeyStore().set('sk-or-v1-e2e-0123456789abcdef0123456789abcdef');
    render(<LivePanel usage={createUsageStore({ storage: memoryStorage(), target: null })} />);
    expect(screen.getByTestId('live-storage-blocked')).toHaveTextContent(copy.playground_live.storage_blocked);
    expect(screen.getByTestId('live-panel')).toHaveAttribute('data-key-state', 'memory');
    expect(screen.getByRole('button', { name: copy.playground_live.run_check })).not.toHaveAttribute('aria-disabled', 'true');
  });
});

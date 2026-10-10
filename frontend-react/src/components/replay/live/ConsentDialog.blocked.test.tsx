/**
 * The consent dialog when this browser blocks storage (D102). A file of its own: the tab's key
 * store switches to memory for good once storage refuses, so no other test may share its module.
 */
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { copy } from '@/domain/presentation';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { pageNav } from '@/lib/openrouter/navigate';
import { ConsentDialog } from './ConsentDialog';

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
});

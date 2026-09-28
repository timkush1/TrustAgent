import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/button';
import { ApiError } from '@/data/errors';
import { ConfirmDialog } from './ConfirmDialog';

function renderConfirm(props: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onConfirm = props.onConfirm ?? vi.fn();
  render(
    <ConfirmDialog
      trigger={<Button>Remove key</Button>}
      title="Remove the OpenRouter key?"
      description="Fast checks stop until you connect again."
      confirmLabel="Remove key"
      onConfirm={onConfirm}
      {...props}
    />,
  );
  return { onConfirm };
}

describe('ConfirmDialog', () => {
  it('asks in an alert dialog with Cancel focused first, so Enter never destroys by accident', async () => {
    const { onConfirm } = renderConfirm();
    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Remove the OpenRouter key?' });
    expect(dialog).toHaveAccessibleDescription('Fast checks stop until you connect again.');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus());
    await userEvent.keyboard('{Enter}');
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('carries out the action with the danger-filled confirm button and closes', async () => {
    const { onConfirm } = renderConfirm();
    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    const dialog = await screen.findByRole('alertdialog');
    const confirm = screen.getAllByRole('button', { name: 'Remove key' }).find((b) => dialog.contains(b)) as HTMLElement;
    expect(confirm.className).toContain('bg-tone-danger');
    await userEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
  });

  it('uses the primary fill for a neutral confirmation', async () => {
    renderConfirm({ tone: 'neutral', confirmLabel: 'Save policy', trigger: <Button>Save</Button> });
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    const confirm = await screen.findByRole('button', { name: 'Save policy' });
    expect(confirm.className).toContain('bg-fg');
    expect(confirm.className).not.toContain('tone-danger');
  });

  it('needs the typed confirmation before the action is available, and says so', async () => {
    const { onConfirm } = renderConfirm({
      title: 'Allow uncalibrated blocking?',
      confirmLabel: 'Allow blocking',
      trigger: <Button>Allow uncalibrated blocking</Button>,
      confirmationText: 'support-bot',
    });
    await userEvent.click(screen.getByRole('button', { name: 'Allow uncalibrated blocking' }));
    const input = await screen.findByRole('textbox', { name: 'To confirm, type support-bot' });
    await waitFor(() => expect(input).toHaveFocus());
    const confirm = screen.getByRole('button', { name: 'Allow blocking' });
    expect(confirm).toHaveAttribute('aria-disabled', 'true');
    expect(confirm).toHaveAccessibleDescription('To confirm, type support-bot');
    await userEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
    await userEvent.type(input, 'support-bo');
    expect(confirm).toHaveAttribute('aria-disabled', 'true');
    await userEvent.type(input, 't');
    expect(confirm).not.toHaveAttribute('aria-disabled', 'true');
    await userEvent.keyboard('{Enter}');
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  it("stays open on failure and shows the server's message, then succeeds on a retry", async () => {
    let fail = true;
    const onConfirm = vi.fn(async () => {
      if (fail) {
        fail = false;
        throw new ApiError({ kind: 'http', status: 409, code: 'stale_version', message: 'The policy changed since you opened it' });
      }
    });
    renderConfirm({ onConfirm });
    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    const dialog = await screen.findByRole('alertdialog');
    const confirm = () => screen.getAllByRole('button', { name: 'Remove key' }).find((b) => dialog.contains(b)) as HTMLElement;
    await userEvent.click(confirm());
    expect(await screen.findByRole('alert')).toHaveTextContent('The policy changed since you opened it');
    expect(screen.getByText('HTTP 409 · stale_version')).toBeInTheDocument();
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    await userEvent.click(confirm());
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(onConfirm).toHaveBeenCalledTimes(2);
  });

  it('starts clean each time a parent opens it', async () => {
    function Controlled() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <Button onClick={() => setOpen(true)}>Allow uncalibrated blocking</Button>
          <ConfirmDialog
            open={open}
            onOpenChange={setOpen}
            title="Allow uncalibrated blocking?"
            confirmLabel="Allow blocking"
            confirmationText="support-bot"
            onConfirm={() => Promise.reject(new Error('Policy is locked'))}
          />
        </>
      );
    }
    render(<Controlled />);
    const opener = screen.getByRole('button', { name: 'Allow uncalibrated blocking' });
    await userEvent.click(opener);
    const input = await screen.findByRole('textbox', { name: 'To confirm, type support-bot' });
    await userEvent.type(input, 'support-bot{Enter}');
    expect(await screen.findByRole('alert')).toHaveTextContent('Policy is locked');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    await userEvent.click(opener);
    expect(await screen.findByRole('textbox', { name: 'To confirm, type support-bot' })).toHaveValue('');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('button', { name: 'Allow blocking' })).toHaveAttribute('aria-disabled', 'true');
  });

  it('shows progress and cannot be dismissed while the action runs', async () => {
    let resolve: () => void = () => {};
    const onConfirm = vi.fn(() => new Promise<void>((r) => (resolve = r)));
    renderConfirm({ onConfirm });
    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }));
    const dialog = await screen.findByRole('alertdialog');
    const confirm = screen.getAllByRole('button', { name: 'Remove key' }).find((b) => dialog.contains(b)) as HTMLElement;
    await userEvent.click(confirm);
    expect(confirm).toHaveAttribute('aria-busy', 'true');
    await userEvent.keyboard('{Escape}');
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    resolve();
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
  });
});

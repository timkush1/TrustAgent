import { act, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Toaster } from '@/components/ui/toast';
import { ApiError } from '@/data/errors';
import { notify } from './notify';

describe('Toaster and notify', () => {
  it('shows a success message and an error with the server message and code', async () => {
    render(<Toaster />);
    act(() => {
      notify.success('Key removed');
    });
    expect(await screen.findByText('Key removed')).toBeInTheDocument();
    act(() => {
      notify.error("Couldn't save the policy", new ApiError({ kind: 'http', status: 412, code: 'stale_version', message: 'Reload and reapply' }));
    });
    expect(await screen.findByText("Couldn't save the policy")).toBeInTheDocument();
    expect(screen.getByText('Reload and reapply (HTTP 412 · stale_version)')).toBeInTheDocument();
    act(() => notify.dismiss());
    await waitFor(() => expect(screen.queryByText('Key removed')).toBeNull());
  });
});

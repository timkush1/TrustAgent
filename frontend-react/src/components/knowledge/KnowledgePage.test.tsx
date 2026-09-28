import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { kbClaim } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { adminStatus, renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const enabled = { knowledge: { state: 'up' as const, enabled: true, kb_claims: 42 } };

describe('Knowledge page', { timeout: 20_000 }, () => {
  it('appears in the navigation only when the add-on is enabled', async () => {
    renderPage('/app/overview', { status: enabled });
    expect(await screen.findByRole('link', { name: 'Knowledge' })).toHaveAttribute('href', '/app/knowledge');
    stopLive();
  });

  it('is not in the navigation when the add-on is off', async () => {
    renderPage('/app/overview');
    await screen.findByRole('link', { name: 'Playground' });
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Knowledge' })).not.toBeInTheDocument());
  });

  it('explains how to turn the add-on on', async () => {
    renderPage('/app/knowledge');
    expect(await screen.findByText('The knowledge base add-on is off')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Turn it on' }).textContent).toContain('COMPOSE_PROFILES=knowledge make up');
  });

  it('treats 409 knowledge_disabled as off, whatever the status said', async () => {
    const control = createControlApi({ kbStats: errorReply(409, 'knowledge_disabled', 'The knowledge add-on is off.') });
    renderPage('/app/knowledge', { control, status: enabled });
    expect(await screen.findByText('The knowledge base add-on is off')).toBeInTheDocument();
  });

  it('shows stats, claims with paging, and conflicts', async () => {
    const user = userEvent.setup();
    const claims = Array.from({ length: 25 }, (_, i) => kbClaim(i, i === 3 ? { status: 'quarantined', quarantine_reason: 'Not supported by its source' } : {}));
    const control = createControlApi({ kbClaims: { claims, total: 25, limit: 50, offset: 0 } });
    renderPage('/app/knowledge', { control, status: enabled });
    const stats = await screen.findByLabelText('Knowledge base');
    expect(within(stats).getByText('42')).toBeInTheDocument();
    expect(await screen.findByText('1–20 of 25 claims')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Next page/ }));
    expect(await screen.findByText('21–25 of 25 claims')).toBeInTheDocument();
    expect(control.callsTo('/api/kb/claims').at(-1)?.query.get('offset')).toBe('20');
    await user.click(screen.getByRole('radio', { name: 'Quarantined' }));
    expect(await screen.findByText('Not supported by its source')).toBeInTheDocument();
    expect(control.callsTo('/api/kb/claims').at(-1)?.query.get('status')).toBe('quarantined');
    expect(screen.getByText('Annual plans can be refunded within 60 days.')).toBeInTheDocument();
  });

  it('uploads a document file and shows the full result', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/knowledge', { status: enabled });
    const upload = await screen.findByRole('button', { name: 'Upload' });
    expect(upload).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('Choose a JSON file first.')).toBeInTheDocument();
    await user.upload(screen.getByLabelText('File'), new File(['[{"content":"Refunds within 30 days."}]'], 'docs.json', { type: 'application/json' }));
    await user.click(screen.getByRole('button', { name: 'Upload' }));
    const result = await screen.findByRole('region', { name: 'Upload result' });
    expect(within(result).getByText(/2 documents · 5 claims accepted · 1 quarantined · 1 conflicts · cost \$0\.004/)).toBeInTheDocument();
    expect(within(result).getByText('Refunds take 60 days.')).toBeInTheDocument();
    expect(within(result).getByText('Conflicts with an accepted claim')).toBeInTheDocument();
    expect(control.callsTo('/api/kb/documents', 'POST')).toHaveLength(1);
  });

  it('refuses a file over the upload limit before sending it', async () => {
    const user = userEvent.setup();
    const status = { ...enabled, limits: { ...adminStatus().limits, max_upload_bytes: 10 } };
    const { control } = renderPage('/app/knowledge', { status });
    await user.upload(await screen.findByLabelText('File'), new File(['x'.repeat(50)], 'big.json', { type: 'application/json' }));
    expect(await screen.findByText(/this proxy accepts up to 10/)).toBeInTheDocument();
    expect(control.callsTo('/api/kb/documents')).toHaveLength(0);
  });

  it('says why uploads are not possible on a read-only instance', async () => {
    renderPage('/app/knowledge', { status: { ...enabled, readonly: true } });
    expect(await screen.findByText('Not available on a read-only instance')).toBeInTheDocument();
  });

  it('shows the server message with Retry', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ kbStats: errorReply(503, 'engine_unavailable', 'The audit engine is not reachable.') });
    renderPage('/app/knowledge', { control, status: enabled });
    expect(await screen.findByText("Couldn't load this: The audit engine is not reachable.")).toBeInTheDocument();
    control.state.kbStats = { total_claims: 1, accepted: 1, quarantined: 0, conflict_pairs: 0 };
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByLabelText('Knowledge base')).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/knowledge', { status: enabled });
    await screen.findByText('1–3 of 3 claims');
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

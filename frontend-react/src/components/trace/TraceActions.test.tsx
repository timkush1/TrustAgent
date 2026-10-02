/**
 * The P4 trace actions against the real data layer over msw: "Was this right?" (labels, D59),
 * "Open evaluator" and "Adjust threshold" (EVW-5), and the glass box "Show exact Jev request"
 * (EVW-4, D63) in its match, mismatch, expired, not-served, no-content-scope, redacted and simulated
 * states.
 */
import { cleanup, configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import decisionMismatch from '@contracts/fixtures/api/DecisionRequestView.mismatch.json';
import decisionMatch from '@contracts/fixtures/api/DecisionRequestView.match.json';
import noEvidenceFixture from '@contracts/fixtures/api/TraceDetail.no-evidence-not-assessed.json';
import refundFixture from '@contracts/fixtures/api/TraceDetail.refund-contradiction-flag.json';
import { http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import { createFakeApi } from '@/test/apiHandlers';
import { axeViolations, formatViolations } from '@/test/axe';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { adminStatus, renderPage } from '@/test/control/renderPage';
import { API, installMswServer, server } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import type { DecisionRequestView, SystemStatus, TraceDetail } from '@/types/api';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 5000 });

const FIXTURES = { refund: refundFixture, 'no-evidence': noEvidenceFixture } as const;
/** A fresh copy of a golden trace detail, safe to change in a test. */
const traceDetail = (id: keyof typeof FIXTURES) => structuredClone(FIXTURES[id]) as unknown as TraceDetail;

function setup(detail: TraceDetail = traceDetail('refund'), options: { control?: ReturnType<typeof createControlApi>; status?: Partial<SystemStatus> } = {}) {
  const api = createFakeApi({ status: adminStatus(options.status), details: { [detail.trace_id]: detail } }, { controlPlane: false });
  const page = renderPage(`/app/traffic/${detail.trace_id}`, { api, control: options.control });
  // Like the gateway, GET /api/traces/:id carries the labels written so far.
  server.use(
    http.get(`${API}/api/traces/:id`, ({ params }) => {
      const stored = api.state.details[String(params.id)];
      if (!stored) return HttpResponse.json({ error: { code: 'not_found', message: 'No trace with this id.' } }, { status: 404 });
      const written = page.control.state.labels
        .filter((l) => l.trace_id === stored.trace_id)
        .map((l) => ({ target: l.target, label: l.label, labeled_by: 'setup-admin', created_at: new Date().toISOString(), note: l.note ?? null }));
      return HttpResponse.json({ ...stored, labels: [...stored.labels.filter((l) => !written.some((w) => w.target === l.target)), ...written] });
    }),
  );
  return { detail, ...page };
}

const outcome = () => screen.getByRole('region', { name: 'Outcome' });

async function openGlassBox() {
  const user = userEvent.setup();
  await user.click(await screen.findByText(copy.trace.details));
  const toggle = await screen.findByText(copy.trace.show_jev_request);
  await user.click(toggle);
  return (await screen.findByTestId('jev-request')) as HTMLElement;
}

describe('Was this right?', { timeout: 20_000 }, () => {
  it('labels every check at Elevated or above as Correct, then undoes it', async () => {
    const user = userEvent.setup();
    const { control, detail } = setup();
    const group = await within(await screen.findByRole('region', { name: 'Outcome' })).findByRole('group', { name: copy.trace.was_this_right });
    expect(within(outcome()).getByText(/Counts toward calibrating Groundedness, Contradiction/)).toBeInTheDocument();
    await user.click(within(group).getByRole('button', { name: copy.trace.correct }));

    await waitFor(() => expect(control.callsTo('/api/labels', 'PUT')).toHaveLength(2));
    expect(control.callsTo('/api/labels', 'PUT').map((c) => c.body)).toEqual([
      { trace_id: detail.trace_id, target: 'groundedness@1', label: 'should_fire' },
      { trace_id: detail.trace_id, target: 'contradiction@1', label: 'should_fire' },
    ]);
    expect(await within(outcome()).findByText(/Labelled correct/)).toHaveTextContent('by setup-admin');

    await user.click(within(outcome()).getByRole('button', { name: 'Undo' }));
    await waitFor(() => expect(control.callsTo('/api/labels', 'DELETE')).toHaveLength(2));
    expect(await within(outcome()).findByRole('button', { name: copy.trace.false_alarm })).toBeInTheDocument();
  });

  it('shows the label at once, but lets Undo wait until the save has landed', async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { control } = setup();
    server.use(
      http.put(`${API}/api/labels`, async ({ request }) => {
        control.state.labels.push((await request.json()) as (typeof control.state.labels)[number]);
        await gate;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    await user.click(await within(await screen.findByRole('region', { name: 'Outcome' })).findByRole('button', { name: copy.trace.correct }));
    expect(await within(outcome()).findByText(/Labelled correct/)).toHaveTextContent('by you');
    // Focusable while it waits (aria-disabled), so a keyboard user keeps their place.
    expect(within(outcome()).getByRole('button', { name: 'Undo' })).toHaveAttribute('aria-disabled', 'true');
    release();
    await waitFor(() => expect(within(outcome()).getByRole('button', { name: 'Undo' })).not.toHaveAttribute('aria-disabled', 'true'));
    expect(control.callsTo('/api/labels', 'DELETE')).toHaveLength(0);
  });

  it('writes should_not_fire for a false alarm', async () => {
    const user = userEvent.setup();
    const { control } = setup();
    await user.click(await within(await screen.findByRole('region', { name: 'Outcome' })).findByRole('button', { name: copy.trace.false_alarm }));
    await waitFor(() => expect(control.callsTo('/api/labels', 'PUT')).toHaveLength(2));
    expect(control.callsTo('/api/labels', 'PUT').every((c) => (c.body as { label: string }).label === 'should_not_fire')).toBe(true);
  });

  it('shows labels already given, by whom, with Undo', async () => {
    const detail = traceDetail('refund');
    detail.labels = [
      ...detail.labels,
      { target: 'groundedness@1', label: 'should_not_fire', labeled_by: 'ops-lead', created_at: detail.created_at, note: null },
      { target: 'contradiction@1', label: 'should_not_fire', labeled_by: 'ops-lead', created_at: detail.created_at, note: null },
    ];
    setup(detail);
    expect(await within(await screen.findByRole('region', { name: 'Outcome' })).findByText(/Labelled false alarm/)).toHaveTextContent('by ops-lead');
    expect(within(outcome()).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
    expect(within(outcome()).queryByRole('button', { name: copy.trace.correct })).toBeNull();
  });

  it('names who labelled by their key, never a session fingerprint or key id', async () => {
    const detail = traceDetail('refund');
    detail.labels = [
      ...detail.labels,
      { target: 'groundedness@1', label: 'should_fire', labeled_by: 'env:admin#3fa9c1d2e4b5a6f7', created_at: detail.created_at, note: null },
      { target: 'contradiction@1', label: 'should_fire', labeled_by: 'env:admin#3fa9c1d2e4b5a6f7', created_at: detail.created_at, note: null },
    ];
    setup(detail);
    const line = await within(await screen.findByRole('region', { name: 'Outcome' })).findByText(/Labelled correct/);
    expect(line).toHaveTextContent('by admin');
    expect(line).not.toHaveTextContent(/env:|3fa9c1d2/);
  });

  it('says why when the stored text expired, and when the server refuses with body_expired', async () => {
    const detail = traceDetail('refund');
    detail.body = { ...detail.body, available: false, unavailable_reason: 'expired' };
    setup(detail);
    const button = await within(await screen.findByRole('region', { name: 'Outcome' })).findByRole('button', { name: copy.trace.correct });
    expect(button).toHaveAttribute('aria-disabled', 'true');
    expect(within(outcome()).getByText(copy.labels.body_expired)).toBeInTheDocument();
    stopLive();
    cleanup();

    const user = userEvent.setup();
    const fresh = traceDetail('refund');
    setup(fresh, { control: createControlApi({ expiredTraces: [fresh.trace_id] }) });
    await user.click(await within(await screen.findByRole('region', { name: 'Outcome' })).findByRole('button', { name: copy.trace.correct }));
    expect(await within(outcome()).findByRole('alert')).toHaveTextContent(copy.labels.body_expired);
    // Nothing was saved: the answers are offered again, not a label that does not exist.
    expect(within(outcome()).queryByText(/Labelled correct/)).toBeNull();
  });

  it('is not offered when no check reached Elevated risk', async () => {
    setup(traceDetail('no-evidence'));
    await screen.findByRole('region', { name: 'Outcome' });
    await screen.findByText(copy.trace.checks);
    expect(screen.queryByTestId('was-this-right')).toBeNull();
  });

  it('needs an admin key', async () => {
    setup(traceDetail('refund'), { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read', 'content'] } } });
    const region = await screen.findByRole('region', { name: 'Outcome' });
    await waitFor(() => expect(within(region).getByRole('button', { name: copy.trace.correct })).toHaveAttribute('aria-disabled', 'true'));
    expect(within(region).getByText('Needs an admin key')).toBeInTheDocument();
  });
});

describe('Open evaluator and Adjust threshold', { timeout: 20_000 }, () => {
  it('point at the riskiest check, its evaluator and its threshold on this route', async () => {
    setup();
    const region = await screen.findByRole('region', { name: 'Outcome' });
    expect(await within(region).findByRole('link', { name: `${copy.trace.open_evaluator}: Contradiction` })).toHaveAttribute('href', '/app/evaluators/contradiction');
    expect(within(region).getByRole('link', { name: /Adjust threshold: Contradiction on support-bot/ })).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/app\/policies\?route=support-bot&evaluator=contradiction(%40|@)1$/),
    );
  });

  it.each(['calibration_missing', 'uncalibrated_snapshot'])('open the Calibration tab when %s capped a block', async (reason) => {
    const detail = traceDetail('refund');
    detail.reasons = [...detail.reasons, reason];
    setup(detail);
    const region = await screen.findByRole('region', { name: 'Outcome' });
    expect(await within(region).findByRole('link', { name: `${copy.trace.open_evaluator}: Contradiction` })).toHaveAttribute(
      'href',
      '/app/evaluators/contradiction?tab=calibration',
    );
    // The cap reads as words from outcomes.v1.json, never the raw reason.
    expect(within(region).getByRole('list', { name: 'Reasons' })).toHaveTextContent(/Would have blocked/);
    expect(region).not.toHaveTextContent(reason);
  });
});

describe('Show exact Jev request', { timeout: 20_000 }, () => {
  const matching = (detail: TraceDetail): DecisionRequestView => ({ ...(decisionMatch as DecisionRequestView), request_sha256: [...(detail.decisions[0].compile?.request_sha256 ?? [])] });

  it('fetches only when opened, and matches a recompile with the recorded fingerprints', async () => {
    const detail = traceDetail('refund');
    const control = createControlApi({ decisionRequests: { [detail.trace_id]: matching(detail) } });
    setup(detail, { control });
    await screen.findByRole('region', { name: 'Outcome' });
    expect(control.callsTo(`/api/traces/${detail.trace_id}/decision-request`)).toHaveLength(0);
    const box = await openGlassBox();
    expect(within(box).getByText(/^1 Jev call · 2 questions · 402 ms · \$\S+ \(Vendor\) · typesafe\/jev-1\.13-20260917$/)).toBeInTheDocument();
    expect(await within(box).findByText(copy.trace.recompile_match)).toBeInTheDocument();
    expect(control.callsTo(`/api/traces/${detail.trace_id}/decision-request`)[0].query.get('attempt')).toBe('0');
    // The state and the questions it was sent, in words and in the raw request.
    expect(within(box).getAllByText(/Is every claim in `assistant_response` supported by `evidence`\?/).length).toBeGreaterThan(0);
    expect(within(box).getByText('Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.', { selector: 'dd' })).toBeInTheDocument();
    expect(within(box).getByRole('region', { name: /Request · typesafe\/jev/ })).toBeInTheDocument();
  });

  it('shows how the fast check split its answer', async () => {
    const detail = traceDetail('refund');
    detail.decisions[0].evaluators['groundedness@1'] = { ...detail.decisions[0].evaluators['groundedness@1'], probabilities: { yes: 0.04, no: 0.96 }, top: 'no' };
    setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: matching(detail) } }) });
    const box = await openGlassBox();
    const bars = within(box).getByRole('list', { name: 'Groundedness' });
    expect(within(bars).getAllByRole('listitem')).toHaveLength(2);
    expect(bars).toHaveTextContent('0.96');
  });

  it('says when the recompile differs: by fingerprint, or for a reason the server gives', async () => {
    setup();
    let box = await openGlassBox();
    expect(await within(box).findByText(copy.trace.recompile_mismatch)).toBeInTheDocument();
    expect(within(box).getByText("The recompile's fingerprint differs from the one recorded with the decision.")).toBeInTheDocument();
    stopLive();
    cleanup();

    const detail = traceDetail('refund');
    setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: decisionMismatch as DecisionRequestView } }) });
    box = await openGlassBox();
    expect(await within(box).findByText(copy.trace.recompile_mismatch)).toBeInTheDocument();
    expect(within(box).getByText(/The stored text was cut to the storage limit/)).toBeInTheDocument();
  });

  it('explains an expired body, a proxy that cannot recompile, and a key without the content scope', async () => {
    const detail = traceDetail('refund');
    setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: errorReply(409, 'body_expired', 'Expired.') } }) });
    let box = await openGlassBox();
    expect(await within(box).findByText(/stored text has expired, so the exact request can.t be rebuilt/)).toBeInTheDocument();
    stopLive();
    cleanup();

    setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: undefined } }) });
    box = await openGlassBox();
    expect(await within(box).findByText(/This proxy can.t rebuild the request/)).toBeInTheDocument();
    stopLive();
    cleanup();

    const control = createControlApi();
    setup(detail, { control, status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read', 'admin'] } } });
    box = await openGlassBox();
    expect(within(box).getByText(/Needs a key with the content scope/)).toBeInTheDocument();
    expect(control.callsTo(`/api/traces/${detail.trace_id}/decision-request`)).toHaveLength(0);
  });

  it('labels a simulated fast check and a redacted request', async () => {
    const detail = traceDetail('refund');
    const usage = detail.decisions[0].decision_model;
    detail.decisions[0].decision_model = usage ? { ...usage, backend: 'mock', cost_source: 'simulated', cost_usd: 0 } : usage;
    detail.redacted = true;
    setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: matching(detail) } }) });
    const box = await openGlassBox();
    expect(within(box).getByText(/a mock fast check answered this request, so no call reached Jev/)).toBeInTheDocument();
    expect(within(box).getByText(/\(Simulated\)/)).toBeInTheDocument();
    expect(within(box).getByText(/the rebuilt request reads the redacted text/)).toBeInTheDocument();
  });

  it('has no axe violations with the glass box open', async () => {
    const detail = traceDetail('refund');
    const { container } = setup(detail, { control: createControlApi({ decisionRequests: { [detail.trace_id]: matching(detail) } }) });
    const box = await openGlassBox();
    await within(box).findByText(copy.trace.recompile_match);
    const violations = await axeViolations(container);
    expect(formatViolations(violations)).toEqual([]);
  });
});

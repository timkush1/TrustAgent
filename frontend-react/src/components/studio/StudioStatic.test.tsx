/**
 * Studio on the static site (D107), through the route tree with the static plane over
 * fakes: without a key "Start from a template" is the primary start and drafting is a disabled
 * action that says why and offers Connect; with a key, Draft with AI shows the judge and the
 * estimate and drafts through the tab's chat client; the test bench shows the estimate before Run,
 * refuses at the cap with a reason and runs through the browser gate; Save becomes Copy JSON and
 * Download .json (a validated evaluator.v1 file that round-trips through the Simple form and never
 * carries the key); one card says what only your own stack does; there is no Save, Watch or
 * Activate button anywhere.
 */
import { configure, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import prompt from '@contracts/prompts/evaluator-drafter.v1.json';
import { copy, fillTemplate } from '@/domain/presentation';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { checkSpecDocument, isSavable } from '@/lib/evaluator/checkSpec';
import { chatAnswer, fakeChat, fakeJev, fakeLedger, fakeStudioLive, type FakeChat, type FakeJev } from '@/lib/studio/fakeLive';
import { readSimpleForm, applySimpleForm } from '@/lib/studio/simpleForm';
import { createStaticStudioPlane } from '@/lib/studio/staticPlane';
import { createStaticEvaluatorReads, staticRegistry } from '@/lib/studio/staticRegistry';
import { axeViolations, formatViolations } from '@/test/axe';
import { fakeDataSource } from '@/test/fakeDataSource';
import { committedDetail, committedManifest } from '@/test/recording';
import { renderApp } from '@/test/renderApp';
import { warmRoutes } from '@/test/warmRoutes';
import type { EvaluatorSpec } from '@/types/api';
import type { StudioLiveView } from './useStudioLive';

warmRoutes('/app/evaluators/studio', '/app/evaluators/studio/refusal/1');
configure({ asyncUtilTimeout: 5000 });

/** The fake tab key and the no-pattern secret: neither may reach an export (Rule 9). */
const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

const view: { current: StudioLiveView } = { current: liveView({}) };
function liveView(over: Partial<StudioLiveView>): StudioLiveView {
  return { checks: true, drafting: true, connected: false, key: null, judgeModel: 'anthropic/claude-haiku-4.5', jevModel: 'typesafe/jev-1.13', maxCalls: 100, maxTokens: 8000, used: 0, inflight: 0, paused: false, resume: () => {}, ...over };
}

vi.mock('@/components/replay/useReplay', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/replay/useReplay')>()),
  useIsStatic: () => true,
}));
vi.mock('./useStudioLive', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./useStudioLive')>()),
  useStudioLive: () => view.current,
}));

const manifest = committedManifest();
const registry = staticRegistry({ started: new Date('2026-10-01T00:00:00Z') });

function staticSource(parts: { chat?: FakeChat; jev?: FakeJev; ledger?: ReturnType<typeof fakeLedger> }) {
  const plane = createStaticStudioPlane(
    { message: 'Run it yourself with make demo.', traceIds: async () => manifest.traces.map((t) => t.trace_id), trace: async (id) => committedDetail(id) },
    { live: fakeStudioLive(parts), registry, random: () => 0 },
  );
  return fakeDataSource({ ...plane, ...createStaticEvaluatorReads(registry) });
}

const step = (name: RegExp) => screen.getByRole('region', { name });
const EXAMPLE = prompt.examples[0].output as unknown as Record<string, unknown>;

beforeEach(() => {
  view.current = liveView({});
});
// The export test sets the clipboard and object URLs this page may use; they are put back afterwards.
const originalUrl = { createObjectURL: URL.createObjectURL, revokeObjectURL: URL.revokeObjectURL };
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
afterEach(() => {
  Object.assign(URL, originalUrl);
  if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard);
  else Reflect.deleteProperty(navigator, 'clipboard');
  vi.restoreAllMocks();
});

async function startFromTemplate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Use this template' }));
  await screen.findByRole('region', { name: /Step 2: Review the check/ });
}

describe('Studio on the static site', { timeout: 30_000 }, () => {
  it('without a key: templates are the way in, drafting says why and offers Connect, and nothing is called', async () => {
    const user = userEvent.setup();
    const chat = fakeChat([]);
    const { container } = renderApp('/app/evaluators/studio', staticSource({ chat }));
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    expect(screen.queryByText('Coming soon')).toBeNull();
    // The page's one primary action is the template start.
    expect(within(describe).getByRole('button', { name: 'Use this template' })).toHaveClass('bg-fg');
    expect(within(describe).getByRole('button', { name: 'Draft with AI' })).not.toHaveClass('bg-fg');
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), 'Flag answers that recommend a competitor.');
    expect(within(describe).getByRole('button', { name: 'Draft with AI' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(describe).getByText(copy.studio.static_draft_needs_key)).toBeInTheDocument();
    expect(within(describe).getByRole('button', { name: copy.playground_live.connect })).not.toHaveAttribute('aria-disabled', 'true');
    // The steps the online demo has, and the card for what needs your own stack.
    const steps = screen.getByRole('navigation', { name: 'Studio steps' });
    expect(within(steps).getAllByRole('listitem').map((li) => li.textContent?.replace(/\(.*\)/, '').replace(/^\d+\.\s*/, '').trim())).toEqual([
      'Describe',
      'Review',
      'Test',
      'Suggest a threshold',
      'Export',
    ]);
    expect(screen.getByRole('region', { name: 'On your own stack' })).toHaveTextContent(/watch it in shadow, test it on past traffic, make it active and calibrate it for blocking/);
    expect(within(screen.getByRole('region', { name: 'On your own stack' })).getByText('make demo').tagName).toBe('CODE');
    expect(chat.calls).toHaveLength(0);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });

  it('has no Save, Watch, Activate or past-traffic button, only the export', async () => {
    const user = userEvent.setup();
    renderApp('/app/evaluators/studio', staticSource({}));
    await startFromTemplate(user);
    for (const name of [/^Save/, /Watch/, /Make active/, /Activate/, /past traffic/, /Calibrate now/]) {
      expect(screen.queryByRole('button', { name })).toBeNull();
    }
    const exportStep = step(/Step 5: Export the check/);
    expect(within(exportStep).getByRole('button', { name: copy.studio.static_export_copy })).not.toHaveAttribute('aria-disabled', 'true');
    expect(within(exportStep).getByRole('button', { name: copy.studio.static_export_download })).not.toHaveAttribute('aria-disabled', 'true');
    expect(screen.queryByRole('region', { name: /Step 6/ })).toBeNull();
  });

  it('with a key: shows the judge and the estimate, drafts through the tab key and fills the steps below', async () => {
    view.current = liveView({ connected: true, key: KEY });
    const user = userEvent.setup();
    const chat = fakeChat([chatAnswer(JSON.stringify(EXAMPLE))]);
    const ledger = fakeLedger();
    renderApp('/app/evaluators/studio', staticSource({ chat, ledger }));
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    expect(within(describe).getByText(/Uses anthropic\/claude-haiku-4\.5 \(OpenRouter\)/)).toBeInTheDocument();
    expect(within(describe).getByText('≈ $0.005')).toBeInTheDocument();
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), 'Flag answers that recommend a competitor.');
    await user.click(within(describe).getByRole('button', { name: 'Draft with AI' }));
    expect(await within(describe).findByText(/Drafted “.+” with anthropic\/claude-haiku-4\.5/)).toBeInTheDocument();
    expect(within(describe).getByRole('button', { name: /Vendor: how “Cost of this draft” was produced/ })).toBeInTheDocument();
    expect(chat.calls).toHaveLength(1);
    expect(ledger.reserved).toHaveLength(1);
    const test = step(/Step 3: Test on samples/);
    expect(within(test).getAllByText(/From the AI's draft/)).toHaveLength(6);
  });

  it('at the cap, drafting is disabled with the cap’s reason and the template is the way in', async () => {
    view.current = liveView({ connected: true, key: KEY, used: 100 });
    const user = userEvent.setup();
    const chat = fakeChat([chatAnswer(JSON.stringify(EXAMPLE))]);
    renderApp('/app/evaluators/studio', staticSource({ chat }));
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), 'Flag answers that recommend a competitor.');
    expect(within(describe).getByRole('button', { name: 'Draft with AI' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(describe).getByText(fillTemplate(copy.playground_static.cap_reached, { max_calls: '100' }))).toBeInTheDocument();
    expect(within(describe).getByRole('button', { name: 'Use this template' })).toHaveClass('bg-fg');
    await user.click(within(describe).getByRole('button', { name: 'Draft with AI' }));
    expect(chat.calls).toHaveLength(0);
  });

  it('words a refused draft by OpenRouter’s reason', async () => {
    view.current = liveView({ connected: true, key: KEY });
    const user = userEvent.setup();
    const { ApiError } = await import('@/data/errors');
    const chat = fakeChat([new ApiError({ kind: 'http', status: 402, code: 'credits', message: 'Payment required' })]);
    renderApp('/app/evaluators/studio', staticSource({ chat }));
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), 'Flag answers that recommend a competitor.');
    await user.click(within(describe).getByRole('button', { name: 'Draft with AI' }));
    const alert = await within(describe).findByRole('alert');
    expect(alert).toHaveTextContent(`Couldn't draft: ${copy.playground_live.errors.no_credits}`);
  });

  it('asking for a draft again is the Retry the paused caps offer: it resumes them first', async () => {
    const resume = vi.fn();
    view.current = liveView({ connected: true, key: KEY, used: 7, paused: true, resume });
    const user = userEvent.setup();
    const chat = fakeChat([chatAnswer(JSON.stringify(EXAMPLE))]);
    renderApp('/app/evaluators/studio', staticSource({ chat }));
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    expect(within(describe).getByText('7 of 100 calls to OpenRouter (checks and AI drafts)')).toBeInTheDocument();
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), 'Flag answers that recommend a competitor.');
    await user.click(within(describe).getByRole('button', { name: 'Draft with AI' }));
    expect(resume).toHaveBeenCalledTimes(1);
    expect(await within(describe).findByText(/Drafted “.+” with anthropic\/claude-haiku-4\.5/)).toBeInTheDocument();
  });

  it('test bench: the estimate first, Run needs a key, and at the cap Run is disabled with a reason', async () => {
    const user = userEvent.setup();
    renderApp('/app/evaluators/studio', staticSource({}));
    await startFromTemplate(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Load samples' }));
    expect(await within(test).findByText(/^Added \d answers? to the samples above\.$/)).toBeInTheDocument();
    expect(within(test).getAllByText(/From the recording/).length).toBeGreaterThan(1);
    expect(within(test).getByText(/^Samples from the simulated recording, route /)).toBeInTheDocument();
    expect(within(test).getByRole('button', { name: /Estimate: how “Cost of this test” was produced/ })).toBeInTheDocument();
    expect(within(test).getByRole('button', { name: 'Run test' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(test).getByText(/^Testing calls OpenRouter with your key from this tab\./)).toBeInTheDocument();
  });

  it('at the cap, Run is disabled with a reason and nothing is sent', async () => {
    view.current = liveView({ connected: true, key: KEY, used: 100 });
    const user = userEvent.setup();
    const jev = fakeJev(() => ({ type: 'noul', noul: 0.9 }));
    renderApp('/app/evaluators/studio', staticSource({ jev }));
    await startFromTemplate(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Load samples' }));
    await within(test).findByText(/^Added \d answers? to the samples above\.$/);
    expect(within(test).getByRole('button', { name: 'Run test' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(test).getByText(fillTemplate(copy.playground_static.cap_reached, { max_calls: '100' }))).toBeInTheDocument();
    expect(jev.requests).toHaveLength(0);
  });

  it('with fewer calls left than samples, Run says how many the test needs', async () => {
    view.current = liveView({ connected: true, key: KEY, used: 99 });
    const user = userEvent.setup();
    const jev = fakeJev(() => ({ type: 'noul', noul: 0.9 }));
    renderApp('/app/evaluators/studio', staticSource({ jev }));
    await startFromTemplate(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Load samples' }));
    const added = await within(test).findByText(/^Added \d answers to the samples above\.$/);
    const n = Number(/\d/.exec(added.textContent ?? '')?.[0]);
    expect(within(test).getByRole('button', { name: 'Run test' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(test).getByText(`This test needs ${n} calls · 99 of 100 calls to OpenRouter (checks and AI drafts)`)).toBeInTheDocument();
    expect(jev.requests).toHaveLength(0);
  });

  it('runs the test through the browser gate with the tab key and labels the cost', async () => {
    view.current = liveView({ connected: true, key: KEY, used: 3 });
    const user = userEvent.setup();
    const jev = fakeJev(() => ({ type: 'noul', noul: 0.9 }), { cost: () => 0.00001 });
    renderApp('/app/evaluators/studio', staticSource({ jev }));
    await startFromTemplate(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Load samples' }));
    await within(test).findByText(/^Added \d answers? to the samples above\.$/);
    expect(within(test).getByText('3 of 100 calls to OpenRouter (checks and AI drafts)')).toBeInTheDocument();
    await user.click(within(test).getByRole('button', { name: 'Run test' }));
    const results = await within(test).findByRole('region', { name: 'Test results' });
    expect(within(results).getAllByText('Fires').length).toBeGreaterThan(0);
    expect(within(results).getByRole('button', { name: /Vendor: how “Cost of this run” was produced/ })).toBeInTheDocument();
    expect(jev.requests.length).toBeGreaterThan(0);
  });

  it('shows no cost figure for a run it cannot price, rather than $0', async () => {
    view.current = liveView({ connected: true, key: KEY, used: 3 });
    const user = userEvent.setup();
    // A decision model the price table does not list, and OpenRouter reports no cost.
    const jev = fakeJev(() => ({ type: 'noul', noul: 0.9 }), { model: 'typesafe/jev-9-preview' });
    renderApp('/app/evaluators/studio', staticSource({ jev }));
    await startFromTemplate(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Load samples' }));
    await within(test).findByText(/^Added \d answers? to the samples above\.$/);
    await user.click(within(test).getByRole('button', { name: 'Run test' }));
    const results = await within(test).findByRole('region', { name: 'Test results' });
    expect(within(results).getByText(/^Cost unknown: /)).toBeInTheDocument();
    expect(within(results).queryByRole('button', { name: /how “Cost of this run” was produced/ })).toBeNull();
    expect(within(results).queryByRole('button', { name: /how “Cost of sample 1” was produced/ })).toBeNull();
  });

  it('exports a validated evaluator.v1 that round-trips through the Simple form, without the key', async () => {
    view.current = liveView({ connected: true, key: KEY });
    const user = userEvent.setup();
    const writeText = vi.fn(async (_text: string) => {});
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const blobs: Blob[] = [];
    const createObjectURL = vi.fn((b: Blob) => {
      blobs.push(b);
      return 'blob:studio-export';
    });
    Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});

    renderApp('/app/evaluators/studio', staticSource({}));
    await startFromTemplate(user);
    const exportStep = step(/Step 5: Export the check/);
    await user.click(within(exportStep).getByRole('button', { name: copy.studio.static_export_copy }));
    expect(await within(exportStep).findByText(/^Copied [a-z0-9_]+\.v1\.json to the clipboard\.$/)).toBeInTheDocument();
    const copied = writeText.mock.calls[0][0];

    await user.click(within(exportStep).getByRole('button', { name: copy.studio.static_export_download }));
    expect(await within(exportStep).findByText(/^Downloaded [a-z0-9_]+\.v1\.json\.$/)).toBeInTheDocument();
    expect(click).toHaveBeenCalledTimes(1);
    const anchor = click.mock.contexts[0] as HTMLAnchorElement;
    expect(anchor.download).toMatch(/^[a-z0-9_]+\.v1\.json$/);
    const downloaded = await blobs[0].text();
    expect(downloaded).toBe(copied);

    for (const text of [copied, downloaded]) {
      expect(text).not.toContain(KEY);
      expect(text).not.toContain(NO_PATTERN);
      const doc = JSON.parse(text) as EvaluatorSpec;
      expect(isSavable(checkSpecDocument(doc))).toBe(true);
      expect(doc.version).toBe(1);
      const read = readSimpleForm(doc);
      expect(read.ok).toBe(true);
      if (read.ok) expect(applySimpleForm(doc, read.form)).toEqual(doc);
    }
  });

  it('refuses to export a check that carries an OpenRouter key', async () => {
    view.current = liveView({ connected: true, key: KEY });
    const user = userEvent.setup();
    renderApp('/app/evaluators/studio', staticSource({}));
    await startFromTemplate(user);
    const review = step(/Step 2: Review the check/);
    const name = within(review).getByRole('textbox', { name: 'Name' });
    fireEvent.change(name, { target: { value: `Leaky ${KEY}` } });
    const exportStep = step(/Step 5: Export the check/);
    await waitFor(() => expect(within(exportStep).getByRole('button', { name: copy.studio.static_export_copy })).toHaveAttribute('aria-disabled', 'true'));
    expect(within(exportStep).getByText(/^The check contains an OpenRouter key\./)).toBeInTheDocument();
  });

  it('opens a built-in version as a copy to export, with no link to the hidden check page', async () => {
    renderApp('/app/evaluators/studio/refusal/1', staticSource({}));
    const first = await screen.findByRole('region', { name: /Step 1: Start from a saved version/ });
    expect(first).toHaveTextContent(/A copy of Refusal version 1/);
    expect(screen.queryByRole('link', { name: /Back to Refusal/ })).toBeNull();
    const exportStep = step(/Step 5: Export the check/);
    expect(exportStep).toHaveTextContent(/Exports refusal_custom as version 1/);
    expect(BUILTIN_TEMPLATES['refusal@1']).toBeDefined();
  });
});

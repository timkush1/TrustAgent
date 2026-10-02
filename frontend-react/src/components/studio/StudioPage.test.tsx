/**
 * Evaluator Studio through the real data layer (LiveDataSource over msw), SPEC §7.3 and P4-PLAN §12:
 * draft with AI, review with live lints, the test bench (estimate first, results, free what-if),
 * quick calibration gating, saving (new check, next version, 412), save and watch on a route through
 * the policy review dialog, and "Test on past traffic" (the D82 confirmation, SSE progress, cancel).
 * The evaluator writes are spied on the data source: their plane belongs to WEB-EV.
 */
import { EditorView } from '@codemirror/view';
import { act, configure, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { afterEach, describe, expect, it, vi } from 'vitest';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import { notify } from '@/components/common/notify';
import { ApiError } from '@/data/errors';
import { axeViolations, formatViolations } from '@/test/axe';
import { evaluatorDetail, evaluatorMeta, evaluatorMetas, evaluatorVersion } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { adminStatus, renderPage } from '@/test/control/renderPage';
import { API, installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import type { DryRunResult, EvaluatorCreateRequest, EvaluatorDraft, EvaluatorSpec, EvaluatorVersion, EvaluatorVersionCreateRequest, StudioDraftResponse } from '@/types/api';

installMswServer();
afterEach(() => {
  stopLive();
  vi.restoreAllMocks();
});
configure({ asyncUtilTimeout: 5000 });

const DESCRIPTION = 'Flag answers that blame the customer.';

function savedVersion(spec: EvaluatorSpec, changelog: string): EvaluatorVersion {
  return {
    evaluator_id: spec.id,
    version: spec.version,
    spec,
    spec_sha256: 'a'.repeat(64),
    question_sha256: 'b'.repeat(64),
    calibration_key: `${spec.id}@${spec.version}:${'c'.repeat(16)}`,
    created_by: 'setup-admin',
    created_at: new Date().toISOString(),
    changelog,
  };
}

const step = (name: RegExp) => screen.getByRole('region', { name });

async function draftWithAi(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByRole('textbox', { name: 'What should TrustAgent check?' }), DESCRIPTION);
  await user.click(screen.getByRole('button', { name: 'Draft with AI' }));
  await screen.findByText(/Drafted “Blames the customer” with anthropic\/claude-haiku-4\.5/);
}

describe('Studio', { timeout: 30_000 }, () => {
  it('drafts with AI, shows the estimate and the model first, and fills the review and the samples', async () => {
    const user = userEvent.setup();
    const { control, container } = renderPage('/app/evaluators/studio');
    const describe = await screen.findByRole('region', { name: /Step 1: Describe a check in plain English/ });
    expect(await within(describe).findByText(/Uses anthropic\/claude-haiku-4\.5 \(OpenRouter\)/)).toBeInTheDocument();
    expect(within(describe).getByText('≈ $0.005')).toBeInTheDocument();
    expect(within(describe).getByRole('button', { name: /Estimate: how “Cost of one draft” was produced/ })).toBeInTheDocument();
    expect(within(describe).getByText('Describe the check in a sentence first')).toBeInTheDocument();

    await draftWithAi(user);
    expect(control.callsTo('/api/studio/draft')[0].body).toEqual({ description: DESCRIPTION });
    // A vendor-reported cost is labelled as such; nothing is saved.
    expect(within(describe).getByRole('button', { name: /Vendor: how “Cost of this draft” was produced/ })).toBeInTheDocument();

    const review = step(/Step 2: Review the check/);
    expect(within(review).getByText('Drafted by AI. Change anything; the lints below update as you type.')).toBeInTheDocument();
    expect(within(review).getByText(/Why the AI shaped it this way/)).toBeInTheDocument();
    expect(within(review).getByRole('textbox', { name: 'Name' })).toHaveValue('Blames the customer');
    expect(within(review).getByRole('radio', { name: 'Pick one' })).toBeChecked();
    expect(within(review).getByText('No problems: the check passes the format and every lint.')).toBeInTheDocument();

    const test = step(/Step 3: Test on samples/);
    expect(within(test).getAllByText(/From the AI's draft/)).toHaveLength(6);
    // The estimate is shown before anything runs.
    expect(within(test).getByText(/6 samples ·/)).toBeInTheDocument();
    expect(within(test).getByRole('button', { name: /Estimate: how “Cost of this test” was produced/ })).toBeInTheDocument();
    expect(control.callsTo('/api/studio/test')).toHaveLength(0);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });

  it('labels a mock draft Simulated and says so', async () => {
    const user = userEvent.setup();
    const draft = { ...structuredClone(draftFixture), cost_usd: 0, cost_source: 'simulated' } as StudioDraftResponse;
    renderPage('/app/evaluators/studio', { control: createControlApi({ studioDraft: draft }) });
    await draftWithAi(user);
    expect(screen.getByRole('button', { name: /Simulated: how “Cost of this draft” was produced/ })).toBeInTheDocument();
    expect(screen.getByText('Drafted by the mock model (simulated). Connect a judge for a real draft.')).toBeInTheDocument();
  });

  it('shows a draft that still breaks the rules, and the server errors with their hint', async () => {
    const user = userEvent.setup();
    const failed: StudioDraftResponse = {
      ...(structuredClone(draftFixture) as StudioDraftResponse),
      draft: null,
      attempts: 2,
      lints: [{ code: 'unresolved_state_reference', severity: 'error', path: 'question.instructions', message: 'The instructions name `policy_text`.' }],
    };
    const control = createControlApi({ studioDraft: failed });
    renderPage('/app/evaluators/studio', { control });
    await user.type(await screen.findByRole('textbox', { name: 'What should TrustAgent check?' }), DESCRIPTION);
    await user.click(screen.getByRole('button', { name: 'Draft with AI' }));
    expect(await screen.findByText(/still broke the evaluator rules after 2 attempts/)).toBeInTheDocument();
    expect(screen.getByText(/Refers to a field the check doesn't read · The instructions name `policy_text`\./)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: /Step 2/ })).not.toBeInTheDocument();

    control.state.studioDraft = errorReply(503, 'no_judge', 'No judge is configured.');
    await user.click(screen.getByRole('button', { name: 'Draft with AI' }));
    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText("Couldn't draft: No judge is configured.")).toBeInTheDocument();
    expect(within(alert).getByText('No deep-audit judge is set. Choose one in Settings → Models.')).toBeInTheDocument();
    expect(within(alert).getByText('You can still start from a template.')).toBeInTheDocument();
  });

  it('makes templates the way in when no judge is configured', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio', { status: { judge: { state: 'disabled', provider: null, model: null, credential_source: 'none', fallback_active: false, fallback_reason: null, warnings: [] } } });
    const describe = await screen.findByRole('region', { name: /Step 1/ });
    expect(await within(describe).findByText('No AI judge is set up to draft with')).toBeInTheDocument();
    expect(within(describe).getByRole('link', { name: 'Choose one in Models' })).toHaveAttribute('href', '/app/settings/models');
    await user.click(within(describe).getByRole('combobox', { name: 'Start from a template' }));
    await user.click(await screen.findByRole('option', { name: /^Refusal/ }));
    await user.click(within(describe).getByRole('button', { name: 'Use this template' }));
    const review = await screen.findByRole('region', { name: /Step 2: Review the check/ });
    expect(within(review).getByRole('textbox', { name: 'Name' })).toHaveValue('Refusal (custom)');
    expect(within(review).getByText(/From the Refusal template/)).toBeInTheDocument();
  });

  it('edits in Simple, Advanced and JSON with live lints, keeping one spec', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const review = step(/Step 2: Review the check/);
    const instructions = within(review).getByRole('textbox', { name: 'What should it catch?' });
    await user.clear(instructions);
    await user.type(instructions, 'Does `policy_text` blame the customer?');
    expect(await within(review).findByText(/1 error to fix before saving/)).toBeInTheDocument();
    expect(within(review).getByText("Refers to a field the check doesn't read")).toBeInTheDocument();
    expect(screen.getAllByText('Fix the errors in step 2 first').length).toBeGreaterThan(0);

    await user.click(within(review).getByRole('tab', { name: 'Advanced' }));
    const id = within(review).getByRole('textbox', { name: 'Id' });
    await user.clear(id);
    await user.type(id, 'Bad Id{Enter}');
    expect(await within(review).findByText(/Use 2 to 48 lower-case letters/)).toBeInTheDocument();

    await user.click(within(review).getByRole('tab', { name: 'JSON' }));
    const editor = await within(review).findByRole('textbox', { name: 'Evaluator spec (JSON)' });
    expect(editor).toHaveAttribute('contenteditable', 'true');
    expect(within(review).getByText(/"blames_the_customer"/)).toBeInTheDocument();
    expect(within(review).getByText(/question\.instructions/)).toBeInTheDocument();
  });

  it('runs the test, then replays another threshold for free and adopts it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Run test' }));
    expect((await within(test).findByText('5 of 6 match')).closest('p')).toHaveTextContent('5 of 6 match · fires on 2 of 6');
    const sent = control.callsTo('/api/studio/test')[0].body as { spec: EvaluatorSpec; samples: { id: string; expected?: string }[] };
    expect(sent.spec.id).toBe('blames_the_customer');
    expect(sent.samples.map((s) => s.expected)).toEqual(['no_fire', 'fire', 'no_fire', 'fire', 'no_fire', 'fire']);
    expect(within(test).getAllByText('Matches your label')).toHaveLength(5);
    expect(within(test).getByText("Doesn't match")).toBeInTheDocument();
    expect(within(test).getByRole('button', { name: /Estimate: how “Cost of this run” was produced/ })).toBeInTheDocument();

    const whatIf = within(test).getByRole('slider', { name: 'What if it flagged at' });
    whatIf.focus();
    for (let i = 0; i < 4; i++) fireEvent.keyDown(whatIf, { key: 'ArrowLeft' });
    expect(await within(test).findByText('At 0.40: fires on 3 of 6 · 6 of 6 match your labels')).toBeInTheDocument();
    expect(control.callsTo('/api/studio/test')).toHaveLength(1);
    await user.click(within(test).getByRole('button', { name: 'Use 0.40 as the threshold' }));
    expect(within(step(/Step 2/)).getByRole('slider', { name: 'Flag when the risk reaches' })).toHaveAttribute('aria-valuenow', '0.4');
    // A new threshold re-scores the stored risks: the run still stands, with no new call.
    expect(await within(test).findByText('At 0.40: fires on 3 of 6 · 6 of 6 match your labels')).toBeInTheDocument();
    expect((await within(test).findByText('6 of 6 match')).closest('p')).toHaveTextContent('6 of 6 match · fires on 3 of 6');
    expect(within(test).queryByRole('button', { name: /as the threshold/ })).not.toBeInTheDocument();
    expect(within(test).queryByText(/changed since this run/)).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /3\. Test \(Done\)/ })).toBeInTheDocument();
    expect(control.callsTo('/api/studio/test')).toHaveLength(1);

    // Changing what the check asks does need a new run.
    await user.type(within(step(/Step 2/)).getByRole('textbox', { name: 'What should it catch?' }), ' Ignore greetings.');
    expect(await within(test).findByText(/The check or the samples changed since this run/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /3\. Test \(To do\)/ })).toBeInTheDocument();
  });

  it('shows a threshold adopted in step 3 in the open JSON tab, so the editor never holds an older spec', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const review = step(/Step 2: Review the check/);
    await user.click(within(review).getByRole('tab', { name: 'JSON' }));
    const editor = await within(review).findByRole('textbox', { name: 'Evaluator spec (JSON)' });
    // The whole document (CodeMirror renders only the lines in view).
    const editorText = () => EditorView.findFromDOM(editor)?.state.doc.toString() ?? '';
    expect(editorText()).toContain('"warn_at": 0.6');

    const test = step(/Step 3: Test on samples/);
    await user.click(within(test).getByRole('button', { name: 'Run test' }));
    const whatIf = await within(test).findByRole('slider', { name: 'What if it flagged at' });
    whatIf.focus();
    for (let i = 0; i < 4; i++) fireEvent.keyDown(whatIf, { key: 'ArrowLeft' });
    await user.click(await within(test).findByRole('button', { name: 'Use 0.40 as the threshold' }));

    await waitFor(() => expect(editorText()).toContain('"warn_at": 0.4'));
    expect(editorText()).not.toContain('"warn_at": 0.6');
    // The step 3 run goes out with the adopted threshold.
    await user.click(within(test).getByRole('button', { name: 'Run again' }));
    await waitFor(() => expect(control.callsTo('/api/studio/test')).toHaveLength(2));
    expect((control.callsTo('/api/studio/test')[1].body as { spec: EvaluatorSpec }).spec.outcomes.warn_at).toBe(0.4);
  });

  it('keeps the other steps on the last workable spec while the JSON lacks what they read', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const review = step(/Step 2: Review the check/);
    await user.click(within(review).getByRole('tab', { name: 'JSON' }));
    const view = EditorView.findFromDOM(await within(review).findByRole('textbox', { name: 'Evaluator spec (JSON)' }));
    if (!view) throw new Error('The JSON tab has no editor.');
    const doc = JSON.parse(view.state.doc.toString()) as Record<string, unknown>;
    delete doc.outcomes;
    act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: JSON.stringify(doc, null, 2) } }));

    expect(await within(review).findByText(/The other tabs show the last version with an id, a name/)).toBeInTheDocument();
    expect(within(review).getByText(/is missing 'outcomes'/)).toBeInTheDocument();
    // The steps below still read the last spec that had its outcomes.
    expect(within(step(/Step 3: Test on samples/)).getByRole('button', { name: 'Run test' })).toBeInTheDocument();
    await user.click(within(review).getByRole('tab', { name: 'Simple' }));
    expect(within(review).getByRole('slider', { name: 'Flag when the risk reaches' })).toHaveAttribute('aria-valuenow', '0.6');
  });

  it('gates quick calibration on 8 labels with 3 of each, and never says calibrated', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const quick = step(/Step 4: Suggest a threshold/);
    expect(within(quick).getByText(/Run the test in step 3 first/)).toBeInTheDocument();
    await user.click(within(step(/Step 3/)).getByRole('button', { name: 'Run test' }));
    expect(await within(quick).findByText("Needs 8 labels (have 6: 3 should fire, 3 shouldn't)")).toBeInTheDocument();
    expect(within(quick).getByTestId('quick-calibration-message')).toHaveTextContent(
      'Label at least 8 answers, with at least 3 where the check should fire and 3 where it should not (you have 3 and 3).',
    );
    expect(within(quick).getByText(/2 more labelled answers: add samples in step 3/)).toBeInTheDocument();
    expect(within(quick).queryByRole('button', { name: /Use the suggested threshold/ })).not.toBeInTheDocument();
    expect(quick.textContent).not.toMatch(/\bcalibrated\b/i);
  });

  it('saves a new check, then watches it on a route through the policy review (If-Match)', async () => {
    const user = userEvent.setup();
    const { control, dataSource } = renderPage('/app/evaluators/studio');
    const create = vi.spyOn(dataSource, 'createEvaluator').mockImplementation((req: EvaluatorCreateRequest) => Promise.resolve(savedVersion(req.spec, req.changelog)));
    await draftWithAi(user);

    const watch = step(/Step 6: Watch it on a route/);
    expect(within(watch).getByText('Save a version first (step 5)')).toBeInTheDocument();

    const save = step(/Step 5: Save a version/);
    expect(within(save).getByText('Say what changed and why first')).toBeInTheDocument();
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'First version from the AI draft');
    await user.click(within(save).getByRole('button', { name: 'Save as a new check' }));
    expect(await within(save).findByText(/Saved as/)).toBeInTheDocument();
    expect(create).toHaveBeenCalledWith({ spec: expect.objectContaining({ id: 'blames_the_customer', version: 1 }), changelog: 'First version from the AI draft' });
    expect(within(save).getByText('No changes since version 1')).toBeInTheDocument();

    await user.click(within(watch).getByRole('button', { name: 'Save and watch on route support-bot' }));
    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    expect(await within(dialog).findByText('The server accepts this policy.')).toBeInTheDocument();
    const validated = control.callsTo('/api/policies/validate')[0].body as { yaml: string };
    expect(validated.yaml).toContain('      - ref: off_brand_tone@1');
    expect(validated.yaml).toMatch(/ {6}- ref: blames_the_customer@1\n {8}stage: shadow\n/);
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Watch blames_the_customer on support-bot');
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(control.callsTo('/api/policies', 'PUT')[0].headers.get('if-match')).toBe('"v3"');

    expect(await within(watch).findByText('Watching on support-bot')).toBeInTheDocument();
    expect(within(watch).getByRole('button', { name: 'Make active' })).toBeInTheDocument();
    expect(within(step(/Step 7: Calibrate for blocking/)).getByRole('link', { name: /Calibrate on the check's page/ })).toHaveAttribute('href', '/app/evaluators/blames_the_customer');
  });

  it('offers a free id after a 409, and clears the conflict once the viewer takes it', async () => {
    const user = userEvent.setup();
    const { dataSource } = renderPage('/app/evaluators/studio');
    const create = vi
      .spyOn(dataSource, 'createEvaluator')
      .mockRejectedValueOnce(
        new ApiError({ kind: 'http', status: 409, code: 'conflict', message: 'An evaluator named blames_the_customer already exists.', details: { names: ['blames_the_customer'] } }),
      )
      .mockImplementation((req: EvaluatorCreateRequest) => Promise.resolve(savedVersion(req.spec, req.changelog)));
    await draftWithAi(user);
    const save = step(/Step 5: Save a version/);
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'First version');
    await user.click(within(save).getByRole('button', { name: 'Save as a new check' }));
    expect(await within(save).findByText('A check with the id blames_the_customer already exists.')).toBeInTheDocument();

    await user.click(within(save).getByRole('button', { name: 'Use the id blames_the_customer_2' }));
    expect(within(save).queryByRole('alert')).not.toBeInTheDocument();
    expect(within(save).getByText(/^Saves a new check,/)).toHaveTextContent('Saves a new check, blames_the_customer_2, as version 1.');
    await user.click(within(save).getByRole('button', { name: 'Save as a new check' }));
    expect(await within(save).findByText(/Saved as/)).toBeInTheDocument();
    expect(create).toHaveBeenLastCalledWith({ spec: expect.objectContaining({ id: 'blames_the_customer_2', version: 1 }), changelog: 'First version' });
  });

  it('confirms a paid test on past traffic with the server estimate, then shows the result (D82)', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/studio');
    await draftWithAi(user);
    const watch = step(/Step 6/);
    await user.click(within(watch).getByRole('radio', { name: '7 days' }));
    await user.click(within(watch).getByRole('button', { name: 'Test on past traffic (paid)' }));
    const confirm = await screen.findByRole('alertdialog', { name: 'Run this paid test?' });
    expect(within(confirm).getByText("This test costs about $0.0021 (200 of 1,840 past answers). $1.84 of today's fast-check budget is left.")).toBeInTheDocument();
    const first = control.callsTo('/api/studio/dry-run')[0].body as Record<string, unknown>;
    expect(first).not.toHaveProperty('confirm_cost');
    expect(first).toMatchObject({ route: 'support-bot', window: '7d', max_samples: 200, spec: expect.objectContaining({ id: 'blames_the_customer' }) });

    await user.click(within(confirm).getByRole('button', { name: 'Run the test' }));
    // 200 of the window's 1,840 answers were tested: the count for the window is the extrapolation.
    expect(await within(watch).findByText('Would have fired ≈ 108 times (5.9%, 95% CI 3.3%–10.2%) on the last 7 days; this test cost $0.00082.')).toBeInTheDocument();
    expect(within(watch).getByRole('button', { name: /Estimate: how “Fires on every answer in the window” was produced/ })).toBeInTheDocument();
    expect(within(watch).getByText('Fired on 11 of 197')).toBeInTheDocument();
    expect(within(watch).getByText(/over the 197 sampled answers it could judge; errors are left out\./)).toBeInTheDocument();
    expect(control.callsTo('/api/studio/dry-run')[1].body).toMatchObject({ confirm_cost: true });
    expect(within(watch).getByText(/off_brand_tone@1 \(6\)|Off-brand tone \(6\)/)).toBeInTheDocument();
    expect(within(watch).getByText('None of these answers had a deep audit to compare with')).toBeInTheDocument();
    expect(within(watch).getByRole('link', { name: '01a0e781-0c1d-7a42-9b7e-3f5d2c8a1b64' })).toHaveAttribute('href', '/app/traffic/01a0e781-0c1d-7a42-9b7e-3f5d2c8a1b64');
  });

  it('cancels a running test and says what it spent', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio', { control: createControlApi({ studioDryRunConfirm: null, studioDryRunHold: true }) });
    await draftWithAi(user);
    const watch = step(/Step 6/);
    await user.click(within(watch).getByRole('button', { name: 'Test on past traffic (paid)' }));
    expect(await within(watch).findByText(/Checked 200 of 200 · fired on 11 · spent ≈ \$0\.00082/)).toBeInTheDocument();
    expect(within(watch).getByRole('progressbar', { name: 'Answers checked' })).toHaveAttribute('aria-valuenow', '200');
    await user.click(within(watch).getByRole('button', { name: 'Cancel the test' }));
    expect(await within(watch).findByText("Test stopped. You're charged only for the checks that already ran (≈ $0.00082).")).toBeInTheDocument();
  });

  it('explains a refused test: over the budget, or no fast-check model', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ studioDryRunConfirm: null, studioDryRun: errorReply(503, 'budget_exhausted', "Today's fast-check budget is used up.") });
    renderPage('/app/evaluators/studio', { control });
    await draftWithAi(user);
    await user.click(within(step(/Step 6/)).getByRole('button', { name: 'Test on past traffic (paid)' }));
    expect(await screen.findByText("The test did not run: Today's fast-check budget is used up.")).toBeInTheDocument();
    expect(screen.getByText(/Raise the daily cap in Settings → Budgets/)).toBeInTheDocument();
  });

  it('says what a test the budget stopped partway spent, instead of that it did not run', async () => {
    const user = userEvent.setup();
    const control = createControlApi({
      studioDryRunConfirm: null,
      studioDryRunStreamError: {
        error: {
          code: 'budget_exhausted',
          message: "Today's decision budget ran out during the test; the results so far were kept.",
          details: { cost_usd: 0.00082, evaluated: 200 },
        },
      },
    });
    renderPage('/app/evaluators/studio', { control });
    await draftWithAi(user);
    const watch = step(/Step 6/);
    await user.click(within(watch).getByRole('button', { name: 'Test on past traffic (paid)' }));
    const alert = await within(watch).findByRole('alert');
    expect(alert).toHaveTextContent("The test stopped partway: Today's decision budget ran out during the test; the results so far were kept.");
    expect(alert).toHaveTextContent("Test stopped. You're charged only for the checks that already ran (≈ $0.00082).");
    expect(alert).toHaveTextContent('Raise the daily cap in Settings → Budgets');
    expect(alert).toHaveTextContent('HTTP 503 · budget_exhausted');
    expect(alert).not.toHaveTextContent('did not run');
  });

  it('says when the route has no stored answers to test, instead of a rate of 0%', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ studioDryRunConfirm: null });
    control.state.studioDryRun = {
      ...(control.state.studioDryRun as DryRunResult),
      window: '24h',
      sampled: 0,
      population: 0,
      assessed: 0,
      not_assessed: 0,
      errors: 0,
      fired: 0,
      fire_rate: 0,
      fire_rate_ci95: [0, 0],
      extrapolated_fires: 0,
      overlap: [],
      examples: [],
      cost_usd: 0,
    };
    control.state.studioDryRunEstimate = { est_cost_usd: 0, sampled: 0, population: 0 };
    control.state.studioDryRunProgress = [];
    renderPage('/app/evaluators/studio', { control });
    await draftWithAi(user);
    const watch = step(/Step 6/);
    await user.click(within(watch).getByRole('button', { name: 'Test on past traffic (paid)' }));
    expect(await within(watch).findByText(/support-bot has no stored answers from the last 24 h to test on, so nothing ran\./)).toBeInTheDocument();
    expect(within(watch).queryByText(/Would have fired/)).not.toBeInTheDocument();
  });

  it('says a shown test result is for an older check once the check changes, but not when it is only saved', async () => {
    const user = userEvent.setup();
    const { dataSource } = renderPage('/app/evaluators/studio', { control: createControlApi({ studioDryRunConfirm: null }) });
    vi.spyOn(dataSource, 'createEvaluator').mockImplementation((req: EvaluatorCreateRequest) => Promise.resolve(savedVersion(req.spec, req.changelog)));
    await draftWithAi(user);
    const watch = step(/Step 6/);
    await user.click(within(watch).getByRole('button', { name: 'Test on past traffic (paid)' }));
    expect(await within(watch).findByText(/^Would have fired/)).toBeInTheDocument();

    const save = step(/Step 5: Save a version/);
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'First version');
    await user.click(within(save).getByRole('button', { name: 'Save as a new check' }));
    expect(await within(save).findByText(/Saved as/)).toBeInTheDocument();
    expect(within(watch).queryByText(/changed since this test/)).not.toBeInTheDocument();

    await user.type(within(step(/Step 2/)).getByRole('textbox', { name: 'Name' }), ' (v2)');
    expect(within(watch).getByText('The check or the route changed since this test. Test again to see how the new one would fire.')).toBeInTheDocument();
  });

  it('keeps writes visible but unavailable, with the reason, on a read-only instance', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio', { status: { readonly: true } });
    const describe = await screen.findByRole('region', { name: /Step 1/ });
    await user.type(within(describe).getByRole('textbox', { name: 'What should TrustAgent check?' }), DESCRIPTION);
    expect(within(describe).getByRole('button', { name: 'Draft with AI' })).toHaveAttribute('aria-disabled', 'true');
    expect(await within(describe).findByText('Not available on a read-only instance')).toBeInTheDocument();
    await user.click(within(describe).getByRole('button', { name: 'Use this template' }));
    const test = await screen.findByRole('region', { name: /Step 3/ });
    expect(within(test).getByRole('button', { name: 'Run test' })).toHaveAttribute('aria-disabled', 'true');
  });

  it('labels every test cost Simulated on a mock fast check (the demo stack)', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio', { status: { demo: true, decision_model: { ...adminStatus().decision_model, backend: 'mock', state: 'mock' } } });
    await draftWithAi(user);
    const test = step(/Step 3/);
    expect(await within(test).findByRole('button', { name: /Simulated: how “Cost of this test” was produced/ })).toBeInTheDocument();
    expect(within(test).getByText('$0')).toBeInTheDocument();
  });

  it('says the fast check is not connected, with the fix', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators/studio', { status: { decision_model: { ...adminStatus().decision_model, backend: 'off', state: 'off' } } });
    const describe = await screen.findByRole('region', { name: /Step 1/ });
    await user.click(within(describe).getByRole('button', { name: 'Use this template' }));
    const test = await screen.findByRole('region', { name: /Step 3/ });
    expect(await within(test).findByText('No fast-check model is connected')).toBeInTheDocument();
    expect(within(test).getByRole('link', { name: 'Connect one' })).toHaveAttribute('href', '/app/settings/connections');
  });

  it('loads samples from recent traffic for free, once each', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/studio');
    await user.click(within(await screen.findByRole('region', { name: /Step 1/ })).getByRole('button', { name: 'Use this template' }));
    const bench = await screen.findByRole('region', { name: /Step 3: Test on samples/ });
    expect(within(bench).getByText('Free: reads answers the gateway already stored. Only answers whose text was kept can be loaded.')).toBeInTheDocument();

    await user.click(within(bench).getByRole('button', { name: 'Load samples' }));
    expect(await within(bench).findByText('Added 2 answers to the samples above.')).toBeInTheDocument();
    expect(within(bench).getAllByText(/From traffic/)).toHaveLength(2);
    expect(within(bench).getAllByRole('link', { name: 'View trace' })[0]).toHaveAttribute('href', '/app/traffic/01a0e780-fe6b-74b8-a21f-09c3d7a6e5f2');
    expect(control.callsTo('/api/studio/samples')[0].body).toEqual({ window: '7d', filter: 'flagged', limit: 20 });

    // The same answers again: nothing is added twice.
    await user.click(within(bench).getByRole('button', { name: 'Load samples' }));
    expect(await within(bench).findByText('Every answer that matched is already in the samples above.')).toBeInTheDocument();
    expect(within(bench).getAllByText(/From traffic/)).toHaveLength(2);
  });

  it('says the policy did not load in step 6, and Retry loads it', async () => {
    const user = userEvent.setup();
    const control = createControlApi();
    const policy = control.state.policies;
    control.state.policies = errorReply(503, 'persistence_disabled', 'The policy store is unavailable.');
    renderPage('/app/evaluators/studio', { control });
    await user.click(within(await screen.findByRole('region', { name: /Step 1/ })).getByRole('button', { name: 'Use this template' }));
    const watch = await screen.findByRole('region', { name: /Step 6: Watch it on a route/ });
    const alert = await within(watch).findByRole('alert');
    expect(alert).toHaveTextContent('The policy could not be loaded: The policy store is unavailable.');

    control.state.policies = policy;
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await within(watch).findByRole('combobox', { name: 'Route' })).toBeInTheDocument();
    expect(within(watch).queryByRole('alert')).not.toBeInTheDocument();
  });
});

describe('Studio on a saved version', { timeout: 30_000 }, () => {
  function dashboardCheck(latest: number) {
    const spec = { ...(draftFixture.draft as unknown as EvaluatorDraft), id: 'blames', version: latest } as unknown as EvaluatorSpec & Record<string, unknown>;
    delete spec.rationale;
    delete spec.test_samples;
    const version = savedVersion(spec as EvaluatorSpec, `Version ${latest}`);
    const detail = { ...evaluatorDetail('refusal'), id: 'blames', name: 'Blames the customer', origin: 'dashboard' as const, latest_version: latest, latest: version };
    return { version, detail };
  }

  it('saves the next version with base_version, and reloads the latest after a 412', async () => {
    const user = userEvent.setup();
    const v1 = dashboardCheck(1);
    const control = createControlApi({ evaluatorDetails: { blames: v1.detail }, evaluatorVersions: { 'blames@1': v1.version } });
    const { dataSource } = renderPage('/app/evaluators/studio/blames/1', { control });
    const createVersion = vi
      .spyOn(dataSource, 'createEvaluatorVersion')
      .mockRejectedValueOnce(new ApiError({ kind: 'http', status: 412, code: 'stale_version', message: 'A newer version exists.' }))
      .mockImplementation((_id: string, req: EvaluatorVersionCreateRequest) => Promise.resolve(savedVersion(req.spec, req.changelog)));

    expect(await screen.findByText('Editing version 1 of Blames the customer')).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'What should TrustAgent check?' })).not.toBeInTheDocument();
    const save = step(/Step 5: Save a version/);
    expect(within(save).getByText('No changes since version 1')).toBeInTheDocument();

    const name = within(step(/Step 2/)).getByRole('textbox', { name: 'Name' });
    await user.clear(name);
    await user.type(name, 'Blames the user');
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'Say user');
    await user.click(within(save).getByRole('button', { name: 'Save version 2' }));
    expect(await within(save).findByText(/Someone saved a newer version of this check/)).toBeInTheDocument();
    expect(createVersion.mock.calls[0][1]).toMatchObject({ base_version: 1, spec: expect.objectContaining({ version: 2, name: 'Blames the user' }) });

    // Meanwhile version 2 was saved elsewhere: reloading makes it the base, and the edit stays.
    const v2 = dashboardCheck(2);
    control.state.evaluatorDetails.blames = v2.detail;
    await user.click(within(save).getByRole('button', { name: 'Reload the latest version' }));
    await user.click(await within(save).findByRole('button', { name: 'Save version 3' }));
    expect(await within(save).findByText(/Saved as/)).toBeInTheDocument();
    expect(createVersion.mock.calls[1][1]).toMatchObject({ base_version: 2, spec: expect.objectContaining({ version: 3, name: 'Blames the user' }) });
  });

  it("shows a 403 that is about the viewer's access as it is, not as a built-in check", async () => {
    const user = userEvent.setup();
    const v1 = dashboardCheck(1);
    const control = createControlApi({ evaluatorDetails: { blames: v1.detail }, evaluatorVersions: { 'blames@1': v1.version } });
    const { dataSource } = renderPage('/app/evaluators/studio/blames/1', { control });
    vi.spyOn(dataSource, 'createEvaluatorVersion').mockRejectedValue(new ApiError({ kind: 'http', status: 403, code: 'forbidden', message: 'This key may not change evaluators.' }));
    expect(await screen.findByText('Editing version 1 of Blames the customer')).toBeInTheDocument();
    const name = within(step(/Step 2/)).getByRole('textbox', { name: 'Name' });
    await user.clear(name);
    await user.type(name, 'Blames the user');
    const save = step(/Step 5: Save a version/);
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'Say user');
    await user.click(within(save).getByRole('button', { name: 'Save version 2' }));
    expect(await within(save).findByText("Couldn't save: This key may not change evaluators.")).toBeInTheDocument();
    expect(within(save).queryByText(/Built-in checks never get new versions/)).not.toBeInTheDocument();
    expect(within(save).queryByRole('button', { name: 'Save as a new check instead' })).not.toBeInTheDocument();
  });

  it('says so when reloading after a 412 fails, and keeps the edit and the way to retry', async () => {
    const user = userEvent.setup();
    const v1 = dashboardCheck(1);
    const control = createControlApi({ evaluatorDetails: { blames: v1.detail }, evaluatorVersions: { 'blames@1': v1.version } });
    const { dataSource } = renderPage('/app/evaluators/studio/blames/1', { control });
    vi.spyOn(dataSource, 'createEvaluatorVersion').mockRejectedValue(new ApiError({ kind: 'http', status: 412, code: 'stale_version', message: 'A newer version exists.' }));
    const failed = vi.spyOn(notify, 'error').mockImplementation(() => '');

    expect(await screen.findByText('Editing version 1 of Blames the customer')).toBeInTheDocument();
    const save = step(/Step 5: Save a version/);
    const name = within(step(/Step 2/)).getByRole('textbox', { name: 'Name' });
    await user.clear(name);
    await user.type(name, 'Blames the user');
    await user.type(within(save).getByRole('textbox', { name: 'What changed and why' }), 'Say user');
    await user.click(within(save).getByRole('button', { name: 'Save version 2' }));
    expect(await within(save).findByText(/Someone saved a newer version of this check/)).toBeInTheDocument();

    vi.spyOn(dataSource, 'getEvaluator').mockRejectedValueOnce(new ApiError({ kind: 'network', status: 0, code: 'network', message: 'The gateway did not answer.' }));
    await user.click(within(save).getByRole('button', { name: 'Reload the latest version' }));
    await waitFor(() => expect(failed).toHaveBeenCalledWith("Couldn't load the latest version", expect.objectContaining({ code: 'network' })));
    expect(within(save).getByText(/Someone saved a newer version of this check/)).toBeInTheDocument();
    expect(within(save).getByRole('button', { name: 'Reload the latest version' })).toBeEnabled();
    expect(within(step(/Step 2/)).getByRole('textbox', { name: 'Name' })).toHaveValue('Blames the user');
  });

  it('copies a built-in check as a new check of your own', async () => {
    renderPage('/app/evaluators/studio/refusal/1');
    const start = await screen.findByRole('region', { name: /Step 1: Start from a saved version/ });
    expect(within(start).getByText(/A copy of Refusal version 1 \(built-in\), saved as a new check/)).toBeInTheDocument();
    const save = step(/Step 5: Save a version/);
    expect(within(save).getByText(/Saves a new check,/)).toBeInTheDocument();
    expect(within(save).getByText('refusal_custom')).toBeInTheDocument();
    expect(within(save).getByRole('button', { name: 'Save as a new check' })).toBeInTheDocument();
    expect(evaluatorVersion('refusal').spec.builtin).toBe(true);
  });

  it('gives the copy an id the library does not have, waiting for the library when it answers last', async () => {
    const control = createControlApi();
    const library = [...evaluatorMetas(), evaluatorMeta('refusal', { id: 'refusal_custom', name: 'My refusal', origin: 'dashboard', latest_version: 2 })];
    control.handlers.unshift(
      http.get(`${API}/api/evaluators`, async () => {
        await delay(300);
        return HttpResponse.json({ items: library });
      }),
    );
    renderPage('/app/evaluators/studio/refusal/1', { control });
    const save = await screen.findByRole('region', { name: /Step 5: Save a version/ });
    expect(within(save).getByText('refusal_custom_2')).toBeInTheDocument();
  });

  it('shows the error with Retry for a version that does not load', async () => {
    const control = createControlApi({ evaluatorDetails: { refusal: errorReply(404, 'not_found', 'No evaluator refusal.') } });
    renderPage('/app/evaluators/studio/refusal/1', { control });
    expect(await screen.findByText(/No evaluator refusal\./)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retry/ })).toBeInTheDocument();
  });

  it('shows a skeleton while the version loads', async () => {
    renderPage('/app/evaluators/studio/refusal/1', { control: createControlApi({ latencyMs: 800 }) });
    expect(await within(await screen.findByRole('main')).findByText('Loading…')).toBeInTheDocument();
    expect(await screen.findByRole('region', { name: /Step 1: Start from a saved version/ })).toBeInTheDocument();
  });

  it('refuses a version that is not a number', async () => {
    renderPage('/app/evaluators/studio/refusal/latest');
    expect(await screen.findByText(/“latest” is not a version number\./)).toBeInTheDocument();
  });
});

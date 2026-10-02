import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { calibrationProgress, evaluatorDetail, evaluatorMeta, evaluatorMetas, evaluatorVersion, reliability } from '@/test/control/fixtures';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';

installMswServer();
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const card = async (name: string) => (await screen.findByRole('article', { name })) as HTMLElement;

describe('Evaluators library', { timeout: 20_000 }, () => {
  it('shows each check with its category, routes and calibration progress', async () => {
    renderPage('/app/evaluators');
    const g = await card('Groundedness');
    expect(within(g).getByText('Grounding')).toBeInTheDocument();
    expect(within(g).getByText(/Built-in · version 1/)).toBeInTheDocument();
    expect(within(g).getAllByText(/support-bot|payments-faq/)).toHaveLength(2);
    expect(await within(g).findByText('Labels 43/200 (31 correct, 12 false alarms)')).toBeInTheDocument();
    expect(within(g).getByRole('meter', { name: 'Labels' })).toHaveAttribute('aria-valuenow', '43');
    expect(within(g).getByText(/Needs 157 more labels, 18 more false alarms/)).toBeInTheDocument();
    // Label queue (this check's answers) and Calibrate now are live for an admin.
    expect(within(g).getByRole('link', { name: 'Label queue for Groundedness' })).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/app\/evaluators\/labels\?evaluator=groundedness(%40|@)1$/),
    );
    expect(within(g).getByRole('button', { name: 'Calibrate now for Groundedness' })).not.toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('link', { name: 'New check' })).toHaveAttribute('href', '/app/evaluators/studio');
    expect(screen.getByRole('navigation', { name: 'Evaluators sections' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Library' })).toHaveAttribute('aria-current', 'page');
    // Checks that run somewhere come first.
    const names = screen.getAllByRole('article').map((a) => a.getAttribute('aria-labelledby') && a.querySelector('h3')?.textContent);
    expect(names.slice(0, 4)).toEqual(['Contradiction', 'Groundedness', 'Unapproved commitments', 'Off-brand tone']);
    const tone = await card('Off-brand tone');
    expect(within(tone).getByText(/Watching/)).toBeInTheDocument();
  });

  it('filters by name, category and use, and clears the filters', async () => {
    const user = userEvent.setup();
    renderPage('/app/evaluators');
    await card('Groundedness');
    await user.type(screen.getByRole('searchbox', { name: 'Search evaluators' }), 'tone');
    expect(screen.getByRole('article', { name: 'Off-brand tone' })).toBeInTheDocument();
    expect(screen.queryByRole('article', { name: 'Groundedness' })).not.toBeInTheDocument();
    await user.clear(screen.getByRole('searchbox', { name: 'Search evaluators' }));
    await user.click(screen.getByRole('combobox', { name: 'Routes' }));
    await user.click(await screen.findByRole('option', { name: 'Not on any route' }));
    await waitFor(() => expect(screen.queryByRole('article', { name: 'Groundedness' })).not.toBeInTheDocument());
    expect(screen.getByRole('article', { name: 'Refusal' })).toBeInTheDocument();
    await user.type(screen.getByRole('searchbox', { name: 'Search evaluators' }), 'zzz');
    expect(await screen.findByText('No evaluators match these filters')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(await card('Groundedness')).toBeInTheDocument();
  });

  it('asks the server for archived evaluators on request', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ evaluators: [...evaluatorMetas(), evaluatorMeta('refusal', { id: 'old_refusal', name: 'Old refusal', archived: true })] });
    control.state.evaluatorDetails.old_refusal = evaluatorDetail('refusal', { id: 'old_refusal', name: 'Old refusal', archived: true });
    renderPage('/app/evaluators', { control });
    await card('Groundedness');
    expect(screen.queryByRole('article', { name: /Old refusal/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole('switch', { name: 'Show archived' }));
    const old = await card('Old refusal');
    expect(within(old).getByText('Archived')).toBeInTheDocument();
    expect(control.callsTo('/api/evaluators').at(-1)?.query.get('include_archived')).toBe('true');
  });

  it('shows the error with Retry and degrades on an older proxy', async () => {
    const user = userEvent.setup();
    const control = createControlApi({ evaluators: errorReply(503, 'persistence_disabled', 'No database.') });
    renderPage('/app/evaluators', { control });
    expect(await screen.findByText("Couldn't load this: No database.")).toBeInTheDocument();
    control.state.evaluators = evaluatorMetas();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await card('Groundedness')).toBeInTheDocument();
  });

  it('degrades when the library is not served', async () => {
    renderPage('/app/evaluators', { control: createControlApi({ evaluators: undefined }) });
    expect(await screen.findByText('The evaluator library is not available on this proxy')).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/evaluators');
    const g = await card('Groundedness');
    await within(g).findByText(/Labels 43\/200/);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

describe('Evaluator detail', { timeout: 20_000 }, () => {
  it('explains the check, where it runs and its calibration', async () => {
    renderPage('/app/evaluators/groundedness');
    expect(await screen.findByRole('heading', { level: 2, name: 'Groundedness' })).toBeInTheDocument();
    expect(screen.getByText('Yes / no')).toBeInTheDocument();
    expect(screen.getByText('Needs your documents (not checked without them)')).toBeInTheDocument();
    expect(screen.getByText('0.80')).toBeInTheDocument();
    expect(screen.getByText('0.95')).toBeInTheDocument();
    expect(screen.getByText('Labels 43/200 (31 correct, 12 false alarms)')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Adopt calibration' })).not.toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('link', { name: 'Duplicate and edit' })).toHaveAttribute('href', '/app/evaluators/studio/groundedness/1');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true');
  });

  it('draws the reliability chart with its caption, table and sources', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/groundedness');
    expect(await screen.findByRole('heading', { name: 'Reliability' })).toBeInTheDocument();
    expect(screen.getByText('Agreement with labels, not ground truth')).toBeInTheDocument();
    expect(screen.getByText('From your labels').nextElementSibling?.textContent).toContain('31');
    expect(screen.getByText('From the deep-audit sample').nextElementSibling?.textContent).toContain('12');
    expect(screen.getByText('Calibration error').nextElementSibling?.textContent).toContain('0.06');
    const call = control.callsTo('/api/gate/reliability')[0].query;
    expect([call.get('evaluator'), call.get('window'), call.get('labels')]).toEqual(['groundedness@1', '7d', 'human']);
    const chart = screen.getByRole('application', { name: /Reliability of groundedness@1/ });
    chart.focus();
    await user.keyboard('{ArrowRight}');
    expect(await screen.findByText(/Risk 0.00 to 0.10: .* labelled a problem, 2 answers\./)).toBeInTheDocument();
    await user.click(screen.getByRole('radio', { name: 'Table' }));
    expect(screen.getByRole('table', { name: /Reliability of groundedness@1/ })).toBeInTheDocument();
  });

  it('labels reliability from deep-audit labels as an estimate, not a measurement', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/evaluators/groundedness');
    await screen.findByRole('heading', { name: 'Reliability' });
    expect(await screen.findByRole('button', { name: /^Measured: how “Reliability” was produced/ })).toBeInTheDocument();
    await user.click(screen.getByRole('combobox', { name: 'Labels from' }));
    await user.click(await screen.findByRole('option', { name: 'Deep-audit sample' }));
    await waitFor(() => expect(control.callsTo('/api/gate/reliability').at(-1)?.query.get('labels')).toBe('uniform_audit'));
    expect(await screen.findByRole('button', { name: /^Estimate: how “Reliability” was produced/ })).toBeInTheDocument();
  });

  it('says so when there are no labelled answers yet', async () => {
    const control = createControlApi({ reliability: reliability({ n: 0, bins: [], ece: null, n_by_source: {} }) });
    renderPage('/app/evaluators/groundedness', { control });
    expect(await screen.findByText('No labelled answers in this window')).toBeInTheDocument();
  });

  it('compares two versions', async () => {
    const control = createControlApi();
    const v2 = evaluatorVersion('groundedness', 2, { changelog: 'Clearer criteria' });
    v2.spec = { ...v2.spec, description: 'A sharper description.' };
    control.state.evaluatorVersions['groundedness@2'] = v2;
    const { spec: _spec, ...v2meta } = v2;
    const { spec: _spec1, ...v1meta } = evaluatorVersion('groundedness', 1);
    control.state.evaluatorDetails.groundedness = evaluatorDetail('groundedness', {
      latest_version: 2,
      latest: v2,
      versions: [v1meta, v2meta],
      calibration_progress: calibrationProgress('groundedness@2'),
    });
    renderPage('/app/evaluators/groundedness?tab=versions', { control });
    expect(await screen.findByText('Clearer criteria')).toBeInTheDocument();
    const diff = await screen.findByRole('region', { name: 'Version 1 → version 2' });
    expect(diff.textContent).toContain('A sharper description.');
  });

  it('has a not-found state for an unknown id', async () => {
    renderPage('/app/evaluators/nope');
    expect(await screen.findByText('No evaluator called nope')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to the library' })).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage('/app/evaluators/groundedness');
    await screen.findByRole('heading', { name: 'Reliability' });
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

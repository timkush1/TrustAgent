/**
 * Benchmarks (EVW-6, D72): committed measured runs only, a Measured badge over every figure, cost
 * labels, confidence intervals, and the empty state with the command that makes a run.
 */
import { screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { renderWithRouter } from '@/test/renderWithRouter';
import { warmRoutes } from '@/test/warmRoutes';
import type { BenchmarkRun } from '@/types/api';
import { committedRuns } from './benchmarkRuns';
import { BenchmarksPage } from './BenchmarksPage';

installMswServer();
warmRoutes('/app/evaluators/benchmarks');
afterEach(() => stopLive());

const metrics = (p: number, r: number) => ({
  precision: p,
  recall: r,
  f1: (2 * p * r) / (p + r),
  accuracy: null,
  balanced_accuracy: null,
  auroc: 0.91,
  ece: 0.04,
  brier: 0.08,
});

function run(overrides: Partial<BenchmarkRun> = {}): BenchmarkRun {
  return {
    run_id: 'bench-lite-20261001',
    created_at: '2026-10-01T09:00:00Z',
    git_sha: '0123456789abcdef0123456789abcdef01234567',
    tier: 'lite',
    measured: true,
    mock: false,
    dataset: { name: 'RAGTruth', subset: 'qa', split: 'test', n: 200, revision: 'a1b2c3d', license_note: 'MIT' },
    systems: [
      {
        id: 'gate',
        decision_model: { backend: 'openrouter-decisions', snapshot: 'typesafe/jev-1.13-20260917' },
        judge: null,
        metrics: { ...metrics(0.82, 0.71), ci95: { precision: [0.74, 0.88] } },
        latency_ms: { p50: 212, p95: 480 },
        cost: { usd_total: 0.05, usd_per_1k: 0.25, cost_source: 'reported' },
        reliability: [],
      },
      {
        id: 'llm_same_checks',
        decision_model: null,
        judge: { provider: 'openrouter', model: 'anthropic/claude-haiku-4.5' },
        metrics: metrics(0.79, 0.75),
        latency_ms: { p50: 1900, p95: 4200 },
        cost: { usd_total: 1.2, usd_per_1k: 6, cost_source: 'computed' },
        reliability: [],
      },
    ],
    errors: { failed: 2, excluded_over_budget: 0 },
    environment: { region: 'eu-west', concurrency: 4 },
    ...overrides,
  };
}

describe('Benchmarks', () => {
  it('keeps only measured runs, newest first', () => {
    const older = run({ run_id: 'older', created_at: '2026-09-01T00:00:00Z' });
    const mock = { ...run({ run_id: 'mock' }), measured: false, mock: true };
    expect(committedRuns([older, mock, run(), null, 'x']).map((r) => r.run_id)).toEqual(['bench-lite-20261001', 'older']);
  });

  it('shows each system with its metrics, intervals, latency and labelled cost under a Measured badge', async () => {
    await renderWithRouter(<BenchmarksPage runs={[run()]} />);
    const table = screen.getByRole('table', { name: /RAGTruth: qa · Lite run/ });
    const rows = within(table).getAllByRole('row');
    expect(rows).toHaveLength(3);
    const gate = within(table).getByRole('rowheader', { name: /Fast check \(Jev\)/ }).closest('tr') as HTMLElement;
    expect(gate).toHaveTextContent('0.82');
    expect(gate).toHaveTextContent('95% CI 0.74–0.88');
    expect(gate).toHaveTextContent('typesafe/jev-1.13-20260917');
    expect(gate).toHaveTextContent('Vendor');
    const llm = within(table).getByRole('rowheader', { name: /LLM, same checks/ }).closest('tr') as HTMLElement;
    expect(llm).toHaveTextContent('Estimate');
    expect(screen.getByRole('button', { name: /^Measured: how “RAGTruth: qa benchmark” was produced/ })).toBeInTheDocument();
    expect(screen.getByText('200 answers from the test split', { exact: false })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Benchmarks' })).toHaveAttribute('aria-current', 'page');
  });

  it('says how to make a run when none is committed', async () => {
    const { container } = await renderWithRouter(<BenchmarksPage runs={[]} />);
    expect(screen.getByText('No measured benchmark runs yet.')).toBeInTheDocument();
    expect(screen.getByText('make bench-lite', { selector: 'code' })).toBeInTheDocument();
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });

  it('is the /app/evaluators/benchmarks page', async () => {
    renderPage('/app/evaluators/benchmarks');
    expect(await screen.findByRole('link', { name: 'Benchmarks' })).toHaveAttribute('aria-current', 'page');
  });

  it('has no axe violations with a run', async () => {
    const { container } = await renderWithRouter(<BenchmarksPage runs={[run()]} />);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });
});

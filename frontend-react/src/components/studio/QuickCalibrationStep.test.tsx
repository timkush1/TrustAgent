/**
 * Studio step 4 once there are enough labels (SPEC §7.3): it suggests a threshold with the honesty
 * text verbatim, offers it as the check's flag threshold, and never says "calibrated" or sets a
 * critical threshold. The gate below 8 labels is covered on the full page (StudioPage.test.tsx).
 */
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import testFixture from '@contracts/fixtures/api/StudioTestResponse.six.json';
import { blankSpec } from '@/lib/studio/templates';
import type { TestRow } from '@/lib/studio/testBench';
import type { EvaluatorSpec, StudioSample, StudioTestResponse } from '@/types/api';
import { QuickCalibrationStep } from './QuickCalibrationStep';

const base = (testFixture as StudioTestResponse).results[0];
const RISKS = [0.05, 0.1, 0.2, 0.3, 0.35, 0.7, 0.8, 0.85, 0.9, 0.95];

function rows(): TestRow[] {
  return RISKS.map((r, i) => ({ ...base, sample_id: `s${i}`, risk_raw: r, risk: r, fires: r >= 0.6, expected: null, agrees: null, status: 'assessed' }));
}

function samples(): StudioSample[] {
  return RISKS.map((r, i): StudioSample => ({ id: `s${i}`, assistant_response: `Answer ${i}`, expected: r > 0.5 ? 'fire' : 'no_fire' }));
}

function specAt(warnAt: number): EvaluatorSpec {
  const spec = blankSpec([]);
  return { ...spec, outcomes: { ...spec.outcomes, warn_at: warnAt } };
}

describe('QuickCalibrationStep with enough labels', () => {
  it('suggests a threshold from the labels and offers it, never saying calibrated', async () => {
    const user = userEvent.setup();
    const onSpecChange = vi.fn();
    const spec = specAt(0.6);
    const { container } = render(<QuickCalibrationStep spec={spec} results={rows()} samples={samples()} onSpecChange={onSpecChange} />);

    expect(screen.getByText("From 10 labels (5 should fire, 5 shouldn't)")).toBeInTheDocument();
    expect(screen.getByTestId('quick-calibration-message')).toHaveTextContent(/^Suggested threshold from 10 labels: 0.7\./);
    await user.click(screen.getByRole('button', { name: 'Use the suggested threshold (0.70)' }));
    expect(onSpecChange).toHaveBeenCalledTimes(1);
    const next = onSpecChange.mock.calls[0][0] as EvaluatorSpec;
    // Only the flag threshold moves: a critical (blocking) threshold needs a real calibration.
    expect(next.outcomes).toEqual({ ...spec.outcomes, warn_at: 0.7 });
    expect(next.id).toBe(spec.id);
    expect(container.textContent).not.toMatch(/\bcalibrated\b/i);
  });

  it('says so when the check already flags at the suggested threshold', () => {
    render(<QuickCalibrationStep spec={specAt(0.7)} results={rows()} samples={samples()} onSpecChange={vi.fn()} />);
    expect(screen.getByText('The check already flags at the suggested threshold.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Use the suggested threshold/ })).not.toBeInTheDocument();
  });

  it("counts the viewer's labels, so relabelling changes the suggestion without a new run", () => {
    const relabelled = samples().map((s, i) => (i >= 5 ? { ...s, expected: undefined } : s));
    render(<QuickCalibrationStep spec={specAt(0.6)} results={rows()} samples={relabelled} onSpecChange={vi.fn()} />);
    expect(screen.getByText("Needs 8 labels (have 5: 0 should fire, 5 shouldn't)")).toBeInTheDocument();
    expect(screen.getByText(/3 more labelled answers, 3 more that should fire: add samples in step 3/)).toBeInTheDocument();
  });
});

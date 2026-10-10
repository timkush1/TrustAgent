/**
 * The glass box's badge on a request rebuilt from the static replay (D109): a simulated recording
 * says only that it was rebuilt (a match proves nothing there), a recorded one says whether the
 * rebuild matches what was sent and, when it does not, why. Without a replay kind (the gateway, the
 * live Playground) the panel keeps its own words.
 */
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import decisionMatch from '@contracts/fixtures/api/DecisionRequestView.match.json';
import { copy } from '@/domain/presentation';
import { axeViolations, formatViolations } from '@/test/axe';
import { committedDetail, scenarioTrace } from '@/test/recording';
import type { DecisionRequestView, GateDecisionView } from '@/types/api';
import { JevRequestBody } from './JevRequestPanel';

const decision = committedDetail(scenarioTrace('refund-contradiction-flag').trace_id).decisions[0];
/** A view whose fingerprints are the decision's own (a match), or another's (a difference). */
const view = (overrides: Partial<DecisionRequestView> = {}): DecisionRequestView => ({
  ...(decisionMatch as DecisionRequestView),
  request_sha256: decision.compile?.request_sha256 ?? [],
  matches_recorded: true,
  mismatch_reason: null,
  ...overrides,
});
const badge = (container: HTMLElement) => container.querySelector('[data-glassbox]');

describe('JevRequestBody on a replayed trace', () => {
  it('says a simulated recording’s request was rebuilt in the browser, and nothing about a match', async () => {
    const { container } = render(<JevRequestBody decision={decision} view={view()} replayKind="mock" />);
    expect(badge(container)).toHaveAttribute('data-glassbox', 'simulated');
    expect(badge(container)).toHaveTextContent(copy.recording.glassbox.rebuilt_simulated);
    expect(screen.queryByText(copy.trace.recompile_match)).toBeNull();
    expect(screen.queryByText(copy.recording.glassbox.matches)).toBeNull();
    // The questions it was asked are listed, their backticked field names as code, never as literal backticks.
    expect(screen.getAllByText(/: what it was asked$/).length).toBeGreaterThan(0);
    const asked = [...container.querySelectorAll('li')].filter((li) => li.textContent?.includes(' — '));
    const ticked = Object.values((decisionMatch as DecisionRequestView).requests[0]?.questions ?? {}).filter(
      (q) => typeof (q as { instructions?: unknown }).instructions === 'string' && (q as { instructions: string }).instructions.includes('`'),
    );
    expect(ticked.length, 'the fixture asks a question that names a field in backticks').toBeGreaterThan(0);
    for (const li of asked) expect(li.textContent).not.toContain('`');
    expect(asked.some((li) => li.querySelector('code'))).toBe(true);
    expect(formatViolations(await axeViolations(container))).toEqual([]);
  });

  it('says whether a recorded request matches, and why it differs', () => {
    const matching = render(<JevRequestBody decision={decision} view={view()} replayKind="recorded" />);
    expect(badge(matching.container)).toHaveTextContent(copy.recording.glassbox.matches);
    matching.unmount();

    const truncated = render(
      <JevRequestBody decision={decision} view={view({ request_sha256: ['0'.repeat(64)], matches_recorded: false, mismatch_reason: 'body_truncated' })} replayKind="recorded" />,
    );
    expect(badge(truncated.container)).toHaveAttribute('data-glassbox', 'differs');
    expect(badge(truncated.container)).toHaveTextContent(copy.recording.glassbox.differs);
    expect(screen.getByText(copy.recording.glassbox.mismatch.body_truncated)).toBeInTheDocument();
    truncated.unmount();

    // A body the recording's sanitizer changed: the badge alone says it differs.
    const scrubbed = render(<JevRequestBody decision={decision} view={view({ request_sha256: ['0'.repeat(64)], matches_recorded: false })} replayKind="recorded" />);
    expect(badge(scrubbed.container)).toHaveTextContent(copy.recording.glassbox.differs);
    expect(badge(scrubbed.container)?.querySelectorAll('p')).toHaveLength(0);
  });

  it('keeps the gateway’s words without a replay kind', () => {
    const { container } = render(<JevRequestBody decision={decision as GateDecisionView} view={view()} />);
    expect(badge(container)).toBeNull();
    expect(screen.getByText(copy.trace.recompile_match)).toBeInTheDocument();
  });
});

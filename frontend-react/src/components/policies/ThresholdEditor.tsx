/**
 * "Adjust threshold" for one check on one route (SPEC §3.3 Policies; EVW-5): a slider with
 * the flag line and, when the check can block here, the block line, each with a "Now" mark. The
 * change goes into the page's draft and opens the review, which replays past answers (Preview
 * impact) before anything is saved. The thresholds stay in the space the route reads (raw score or
 * calibrated).
 */
import { useEffect, useRef, useState } from 'react';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Slider } from '@/components/ui/slider';
import { formatRisk, r2 } from '@/domain/format';
import { copy } from '@/domain/presentation';
import type { EffectiveRoute } from '@/types/api';
import type { ThresholdEdit } from './policyDraft';
import { routeTitle } from './routeWords';

export interface ThresholdEditorProps {
  route: string;
  evaluator: EffectiveRoute['evaluators'][number];
  name: string;
  writeReason: Reason | null;
  /** Move focus to the editor when it opens (it was opened from a link). */
  focusOnOpen?: boolean;
  onPreview: (edit: ThresholdEdit) => void;
  onCancel: () => void;
}

export function ThresholdEditor({ route, evaluator, name, writeReason, focusOnOpen = false, onPreview, onCancel }: ThresholdEditorProps) {
  const headingRef = useRef<HTMLParagraphElement>(null);
  const blocks = evaluator.critical_at !== null;
  const [warn, setWarn] = useState(evaluator.warn_at);
  const [critical, setCritical] = useState(evaluator.critical_at ?? 1);

  useEffect(() => {
    if (!focusOnOpen) return;
    headingRef.current?.scrollIntoView?.({ block: 'center' }); // absent in jsdom
    headingRef.current?.focus({ preventScroll: true });
  }, [focusOnOpen]);

  const changed = r2(warn) !== r2(evaluator.warn_at) || (blocks && r2(critical) !== r2(evaluator.critical_at ?? 1));
  const reason: Reason | null = writeReason ?? (changed ? null : { text: 'Move a threshold first.' });
  const space = evaluator.threshold_space === 'calibrated' ? 'calibrated score (fitted on your labels)' : 'raw fast-check score (not calibrated)';
  const marks = [{ value: evaluator.warn_at, label: 'Now' }, ...(blocks && evaluator.critical_at !== null ? [{ value: evaluator.critical_at, label: 'Now' }] : [])];

  return (
    <div className="space-y-3 rounded-md border border-border bg-surface-sunken px-3 py-3" data-testid="threshold-editor">
      <p ref={headingRef} tabIndex={-1} className="text-sm font-medium text-fg outline-none">
        Thresholds for {name} on {routeTitle(route)}
      </p>
      {blocks ? (
        <Slider<readonly number[]>
          label="Flags from / Blocks from"
          value={[warn, critical]}
          onValueChange={(v) => {
            setWarn(v[0]);
            setCritical(v[1]);
          }}
          thumbLabels={['Flags from', 'Blocks from']}
          marks={marks}
          description={`On the ${space}. Answers at or above the first line are flagged, at or above the second blocked (when the route blocks).`}
        />
      ) : (
        <Slider
          label="Flags from"
          value={warn}
          onValueChange={setWarn}
          marks={marks}
          description={`On the ${space}. This check never blocks on this route.`}
        />
      )}
      <p className="text-xs text-fg-muted tabular">
        Now: flags from {formatRisk(evaluator.warn_at)}
        {blocks ? `, blocks from ${formatRisk(evaluator.critical_at)}` : ''}. Nothing changes until you review and save.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <DisabledAction reason={reason}>
          {(p) => (
            <Button size="sm" onClick={() => onPreview({ warn_at: r2(warn), ...(blocks ? { critical_at: r2(critical) } : {}) })} {...p}>
              {copy.policies.preview_impact}
            </Button>
          )}
        </DisabledAction>
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

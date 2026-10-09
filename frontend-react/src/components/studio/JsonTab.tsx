/**
 * Studio's JSON tab (SPEC §7.3 step 2: "JSON tab (CodeMirror plus ajv)"): the whole spec as JSON in
 * a lazily loaded CodeMirror editor, checked as you type (syntax, the evaluator.v1 schema, the hard
 * rules and lints), each problem marked on its range and listed as text. Valid JSON is taken over at
 * once, so the other tabs and the test bench follow, as long as it still has what they read (an id,
 * a name, the question, the evidence, the state fields and the outcomes with `warn_at`); until the
 * text is valid JSON with those again, the rest of Studio keeps the last version that was.
 *
 * A change made elsewhere while the tab is open (a threshold adopted in step 3 or 4, a new id after
 * a 409, the version a save returns) replaces the text, so the editor always shows the spec Studio
 * will test and save, and a later keystroke here never writes an older spec back over that change.
 */
import { lazy, Suspense, useId, useMemo, useState } from 'react';
import { Skeleton } from '@/components/ui/skeleton';
import { checkSpecJson, type CheckOptions } from '@/lib/evaluator/checkSpec';
import { isWorkableSpec, specJson } from '@/lib/studio/specFields';
import type { EvaluatorSpec } from '@/types/api';
import { ProblemList } from './ProblemList';

const CodeMirrorJson = lazy(() => import('./editor/CodeMirrorJson'));

export function JsonTab({ spec, onChange, options, disabled = false }: { spec: EvaluatorSpec; onChange: (spec: EvaluatorSpec) => void; options: CheckOptions; disabled?: boolean }) {
  // The editor's text, and the spec it was last in step with: the one it was seeded from, or the
  // one its own last valid edit produced.
  const [text, setText] = useState(() => specJson(spec));
  const [synced, setSynced] = useState<EvaluatorSpec>(spec);
  if (spec !== synced) {
    // Changed outside the tab: show it (React's "adjust state when a prop changes" pattern).
    setSynced(spec);
    setText(specJson(spec));
  }
  const problemsId = useId();
  const check = useMemo(() => checkSpecJson(text, options), [text, options]);

  const onText = (next: string) => {
    if (next === text) return;
    setText(next);
    const result = checkSpecJson(next, options);
    if (result.parsed && isWorkableSpec(result.data)) {
      setSynced(result.data);
      onChange(result.data);
    }
  };

  return (
    <div className="space-y-3">
      <Suspense
        fallback={
          <div role="status" className="space-y-2">
            <span className="sr-only">Loading the JSON editor…</span>
            <Skeleton className="h-64 w-full" />
          </div>
        }
      >
        <CodeMirrorJson
          value={text}
          onChange={onText}
          readOnly={disabled}
          label="Evaluator spec (JSON)"
          describedBy={problemsId}
          check={(t) => checkSpecJson(t, options).problems}
        />
      </Suspense>
      {!check.parsed ? (
        <p className="text-xs text-fg-muted">The other tabs show the last version that was valid JSON.</p>
      ) : !isWorkableSpec(check.data) ? (
        <p className="text-xs text-fg-muted">
          The other tabs show the last version with an id, a name, the question, the evidence, the state fields and the outcomes with warn_at.
        </p>
      ) : null}
      <ProblemList id={problemsId} items={check.problems} />
    </div>
  );
}

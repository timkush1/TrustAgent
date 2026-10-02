/**
 * Studio's JSON tab (SPEC §7.3 step 2: "JSON tab (CodeMirror plus ajv)"): the whole spec as JSON in
 * a lazily loaded CodeMirror editor, checked as you type (syntax, the evaluator.v1 schema, the hard
 * rules and lints), each problem marked on its range and listed as text. Valid JSON is taken over at
 * once, so the other tabs and the test bench follow; while the text is not valid JSON the rest of
 * Studio keeps the last version that was.
 */
import { lazy, Suspense, useId, useMemo, useState } from 'react';
import { Skeleton } from '@/components/ui/skeleton';
import { checkSpecJson, type CheckOptions } from '@/lib/evaluator/checkSpec';
import { specJson } from '@/lib/studio/specFields';
import type { EvaluatorSpec } from '@/types/api';
import { ProblemList } from './ProblemList';

const CodeMirrorJson = lazy(() => import('./editor/CodeMirrorJson'));

export function JsonTab({ spec, onChange, options, disabled = false }: { spec: EvaluatorSpec; onChange: (spec: EvaluatorSpec) => void; options: CheckOptions; disabled?: boolean }) {
  // The editor owns its text while this tab is open; it starts from the spec.
  const [text, setText] = useState(() => specJson(spec));
  const problemsId = useId();
  const check = useMemo(() => checkSpecJson(text, options), [text, options]);

  const onText = (next: string) => {
    setText(next);
    const result = checkSpecJson(next, options);
    if (result.parsed && typeof result.data === 'object' && result.data !== null && !Array.isArray(result.data)) {
      onChange(result.data as EvaluatorSpec);
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
      {!check.parsed ? <p className="text-xs text-fg-muted">The other tabs show the last version that was valid JSON.</p> : null}
      <ProblemList id={problemsId} items={check.problems} />
    </div>
  );
}

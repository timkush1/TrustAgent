/**
 * The body of the YAML editor dialog, loaded on demand with CodeMirror and the precompiled policy
 * validator (neither belongs in the Policies page chunk): the editor, the problems as text, and
 * Copy / Cancel / Apply to draft.
 */
import { CircleAlert, TriangleAlert } from 'lucide-react';
import { useDeferredValue, useId, useMemo, useState } from 'react';
import { CopyButton } from '@/components/common/CopyButton';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { formatCount } from '@/domain/format';
import { checkPolicyYaml } from '@/lib/policy/checkPolicyYaml';
import { yamlDiffers } from '../policyDraft';
import CodeMirrorYaml from './CodeMirrorYaml';

const checkProblems = (text: string) => checkPolicyYaml(text).problems;

export interface YamlEditorBodyProps {
  initial: string;
  readOnly: boolean;
  onApply: (yaml: string) => void;
  onClose: () => void;
}

export default function YamlEditorBody({ initial, readOnly, onApply, onClose }: YamlEditorBodyProps) {
  const [text, setText] = useState(initial);
  const deferred = useDeferredValue(text);
  const problems = useMemo(() => checkProblems(deferred), [deferred]);
  const listId = useId();
  const errors = problems.filter((p) => p.severity === 'error');
  const changed = yamlDiffers(initial, text);

  let applyReason: Reason | null = null;
  if (errors.length > 0) applyReason = { text: `Fix ${errors.length === 1 ? 'the error' : `the ${formatCount(errors.length)} errors`} first.` };
  else if (!changed) applyReason = { text: 'No changes to apply yet.' };

  return (
    <div className="space-y-3">
      <CodeMirrorYaml
        value={text}
        onChange={setText}
        readOnly={readOnly}
        label={readOnly ? 'policies.yaml (read-only)' : 'policies.yaml'}
        describedBy={listId}
        check={checkProblems}
      />
      <div id={listId} aria-live="polite" className="text-sm">
        {problems.length === 0 ? (
          <p className="text-fg-muted">No problems found. The server checks the rest (block readiness, cross-field rules) when you review.</p>
        ) : (
          <ul className="max-h-32 space-y-1 overflow-y-auto" aria-label="Problems">
            {problems.map((p, i) => (
              <li key={`${p.from}-${i}`} className="flex items-start gap-1.5 text-fg">
                {p.severity === 'error' ? (
                  <CircleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-tone-danger" />
                ) : (
                  <TriangleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-tone-warning" />
                )}
                <span>
                  <span className="font-mono text-xs text-fg-muted">
                    line {p.line}:{p.col}
                  </span>{' '}
                  {p.message}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2 pt-1">
        <CopyButton value={text} label="Copy YAML" showLabel />
        <Button variant="secondary" onClick={onClose}>
          {readOnly ? 'Close' : 'Cancel'}
        </Button>
        {readOnly ? null : (
          <DisabledAction reason={applyReason}>
            {(p) => (
              <Button
                variant="primary"
                onClick={() => {
                  onApply(text);
                  onClose();
                }}
                {...p}
              >
                Apply to draft
              </Button>
            )}
          </DisabledAction>
        )}
      </div>
    </div>
  );
}

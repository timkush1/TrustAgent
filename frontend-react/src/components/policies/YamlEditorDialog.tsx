/**
 * The advanced YAML editor (SPEC §3.3 Policies): the whole policy as policies.yaml text, checked
 * as the viewer types against the policy.v1 schema (precompiled ajv, CSP-safe) with each problem
 * marked on its line and listed under the editor. "Apply to draft" hands the text to the page's
 * draft; saving still goes through the review (diff, preview impact, If-Match).
 *
 * Without write access the same dialog shows the policy read-only.
 */
import { Code, TriangleAlert, CircleAlert } from 'lucide-react';
import { lazy, Suspense, useDeferredValue, useId, useMemo, useState } from 'react';
import { CopyButton } from '@/components/common/CopyButton';
import { DisabledAction, DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog';
import { Skeleton } from '@/components/ui/skeleton';
import { formatCount } from '@/domain/format';
import { checkPolicyYaml } from '@/lib/policy/checkPolicyYaml';
import { yamlDiffers } from './policyDraft';

const CodeMirrorYaml = lazy(() => import('./editor/CodeMirrorYaml'));

const checkProblems = (text: string) => checkPolicyYaml(text).problems;

export interface YamlEditorDialogProps {
  /** The text to start from: the draft when there is one, else the policy in effect. */
  yaml: string;
  /** Null when the viewer may edit; otherwise the dialog is a read-only view. */
  writeReason: Reason | null;
  onApply: (yaml: string) => void;
}

function EditorBody({
  initial,
  readOnly,
  onApply,
  onClose,
}: {
  initial: string;
  readOnly: boolean;
  onApply: (yaml: string) => void;
  onClose: () => void;
}) {
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
      <Suspense
        fallback={
          <div role="status" className="space-y-2">
            <span className="sr-only">Loading the editor…</span>
            <Skeleton className="h-[min(60vh,32rem)] min-h-64 w-full" />
          </div>
        }
      >
        <CodeMirrorYaml
          value={text}
          onChange={setText}
          readOnly={readOnly}
          label={readOnly ? 'policies.yaml (read-only)' : 'policies.yaml'}
          describedBy={listId}
          check={checkProblems}
        />
      </Suspense>
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

export function YamlEditorDialog({ yaml, writeReason, onApply }: YamlEditorDialogProps) {
  const [open, setOpen] = useState(false);
  const readOnly = writeReason !== null;
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger
        render={
          <Button>
            <Code aria-hidden="true" />
            {readOnly ? 'View YAML' : 'Edit YAML'}
          </Button>
        }
      />
      <DialogContent
        size="lg"
        className="w-[min(60rem,calc(100vw-2rem))]"
        title={readOnly ? 'The policy as YAML' : 'Edit the policy as YAML'}
        description={
          readOnly
            ? 'This is policies.yaml as it is in effect. You cannot change it here.'
            : 'For changes the cards do not offer. Problems are checked as you type; nothing is saved until you review it.'
        }
      >
        {writeReason ? <DisabledReason reason={writeReason} /> : null}
        {/* Mounted only while open, so every opening starts from the latest text. */}
        {open ? <EditorBody initial={yaml} readOnly={readOnly} onApply={onApply} onClose={() => setOpen(false)} /> : null}
      </DialogContent>
    </Dialog>
  );
}

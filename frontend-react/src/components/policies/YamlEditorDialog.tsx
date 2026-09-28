/**
 * The advanced YAML editor (SPEC §3.3 Policies): the whole policy as policies.yaml text, checked
 * as the viewer types against the policy.v1 schema (precompiled ajv, CSP-safe) with each problem
 * marked on its line and listed under the editor. "Apply to draft" hands the text to the page's
 * draft; saving still goes through the review (diff, preview impact, If-Match).
 *
 * Without write access the same dialog shows the policy read-only. The editor, the YAML checker
 * and the precompiled schema validator load only when the dialog opens.
 */
import { Code } from 'lucide-react';
import { lazy, Suspense, useState } from 'react';
import { DisabledReason, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog';
import { Skeleton } from '@/components/ui/skeleton';

const YamlEditorBody = lazy(() => import('./editor/YamlEditorBody'));

export interface YamlEditorDialogProps {
  /** The text to start from: the draft when there is one, else the policy in effect. */
  yaml: string;
  /** Null when the viewer may edit; otherwise the dialog is a read-only view. */
  writeReason: Reason | null;
  onApply: (yaml: string) => void;
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
        {open ? (
          <Suspense
            fallback={
              <div role="status" className="space-y-2">
                <span className="sr-only">Loading the editor…</span>
                <Skeleton className="h-[min(60vh,32rem)] min-h-64 w-full" />
              </div>
            }
          >
            <YamlEditorBody initial={yaml} readOnly={readOnly} onApply={onApply} onClose={() => setOpen(false)} />
          </Suspense>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

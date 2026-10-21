/**
 * Studio step 5 on the static site (D107): the online demo has nowhere to save a version, so "Save"
 * becomes **Copy JSON** and **Download .json**: the check as a validated evaluator.v1 file, the
 * version a save would create, for the viewer's own stack. Nothing secret leaves: an export that
 * contains the tab's OpenRouter key (or anything shaped like one) is refused with the reason.
 */
import { ClipboardCopy, Download } from 'lucide-react';
import { useState } from 'react';
import { copyText } from '@/components/common/clipboard';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { downloadText } from '@/components/common/download';
import { Button } from '@/components/ui/button';
import { copy } from '@/domain/presentation';
import { exportSpec } from '@/lib/studio/exportSpec';
import type { EvaluatorSpec } from '@/types/api';
import { StudioStep } from './StudioStep';

export interface ExportStepProps {
  spec: EvaluatorSpec;
  /** The newest version of this id the library has (0: a new check). */
  latest: number;
  /** Evaluator ids a corroborator may name. */
  registry: readonly string[];
  /** The tab's OpenRouter key, never to appear in the file. */
  secret: string | null;
  /** Why the check cannot be exported yet (errors in step 2), or null. */
  specReason: Reason | null;
  /** This is the step to do next: its action is the page's one primary button. */
  primary: boolean;
}

type Outcome = { kind: 'copied' | 'downloaded' | 'failed'; fileName: string } | null;

export function ExportStep({ spec, latest, registry, secret, specReason, primary }: ExportStepProps) {
  const [outcome, setOutcome] = useState<Outcome>(null);
  const result = exportSpec(spec, latest, { registry, secrets: [secret] });
  let reason: Reason | null = specReason;
  if (!reason && !result.ok) {
    reason = result.reason === 'secret' ? { text: 'The check contains an OpenRouter key. Remove it before you export the check.' } : { text: 'Fix the errors in step 2 first' };
  }
  const version = latest + 1;

  const copyJson = async () => {
    if (!result.ok) return;
    setOutcome({ kind: (await copyText(result.json)) ? 'copied' : 'failed', fileName: result.fileName });
  };
  const download = () => {
    if (!result.ok) return;
    setOutcome({ kind: downloadText(result.fileName, result.json) ? 'downloaded' : 'failed', fileName: result.fileName });
  };

  return (
    <StudioStep
      n={5}
      id="step-save"
      title="Export the check"
      description="The online demo keeps nothing: take the check with you as a file, then add it to your own TrustAgent stack."
    >
      <p className="text-sm text-fg">
        Exports <span className="font-mono text-xs">{spec.id}</span> as version {version}
        {latest > 0 ? ` (the built-in check is at version ${latest})` : ''}, checked against the evaluator format.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <DisabledAction reason={reason}>
          {(p) => (
            <>
              <Button variant={primary ? 'primary' : 'secondary'} onClick={() => void copyJson()} {...p}>
                <ClipboardCopy aria-hidden="true" />
                {copy.studio.static_export_copy}
              </Button>
              <Button variant="secondary" onClick={download} {...p}>
                <Download aria-hidden="true" />
                {copy.studio.static_export_download}
              </Button>
            </>
          )}
        </DisabledAction>
      </div>
      <p role="status" className="text-sm text-fg">
        {outcome?.kind === 'copied' ? `Copied ${outcome.fileName} to the clipboard.` : null}
        {outcome?.kind === 'downloaded' ? `Downloaded ${outcome.fileName}.` : null}
        {outcome?.kind === 'failed' ? 'This browser did not allow it. Open the JSON tab in step 2 and copy the text from there.' : null}
      </p>
      {result.ok ? (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs font-medium text-fg-muted hover:text-fg">Show the file ({result.fileName})</summary>
          <pre className="mt-2 max-h-72 overflow-auto rounded-md border border-border bg-surface-sunken p-3 font-mono text-2xs text-fg">{result.json}</pre>
        </details>
      ) : null}
    </StudioStep>
  );
}

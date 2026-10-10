/**
 * The consent dialog before any call to openrouter.ai (D114): the four lines, the per-check cost
 * with its value label, the worst case with these caps, Connect OpenRouter (PKCE; a `<button>`, so a
 * middle-click cannot start the flow without this tab's verifier) and Paste a key, which is always
 * there. Connect is primary unless the build says Paste (STATIC_CONNECT_PRIMARY) and absent when
 * the browser cannot finish PKCE (PKCE_FROM_BROWSER). It is off, with the reason, when this browser
 * blocks storage: the verifier would not survive the redirect. Inside a frame the dialog renders
 * only "Open in its own tab" (D106). Base UI traps focus, closes on Escape and returns focus to the
 * trigger. Nothing reaches openrouter.ai until Connect or a pasted key. Owned by WEB-LIVE (LV-6).
 */
import { LogIn } from 'lucide-react';
import { useId, useState } from 'react';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent } from '@/components/ui/dialog';
import { playground } from '@/config/runtime';
import { PKCE_FROM_BROWSER, STATIC_CONNECT_PRIMARY } from '@/domain/liveScenarios';
import { formatUsd } from '@/domain/format';
import { copy, fillTemplate } from '@/domain/presentation';
import { liveWords } from '@/domain/words/live';
import { beginConnect } from '@/lib/openrouter/connect';
import { isFramed } from '@/lib/openrouter/frame';
import { useBrowserKey } from '@/lib/openrouter/useBrowserKey';
import { pageNav } from '@/lib/openrouter/navigate';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { perCheckFigure, worstCaseSpend } from './figures';
import { OpenInTab } from './OpenInTab';
import { PasteKey } from './PasteKey';
import { Tagged } from './Tagged';

export interface ConsentDialogProps {
  open: boolean;
  onClose: () => void;
  /** Where the PKCE callback returns to (router-relative, D105), e.g. `app/playground/#live`. */
  returnTo: string;
}

function ConsentLines() {
  const perCheck = perCheckFigure();
  const worst = worstCaseSpend(playground);
  const lines = copy.playground_static.consent.map((line) =>
    fillTemplate(line, { cost_per_check: perCheck?.text ?? null, max_calls: playground.maxCalls.toLocaleString('en-US') }),
  );
  const perCheckBadge = perCheck ? (
    <EvidenceBadge label={perCheck.label} basis={perCheck.basis} asOf={perCheck.asOf} sourceUrl={perCheck.sourceUrl} subject={liveWords.per_check_subject} />
  ) : null;
  return (
    <div className="space-y-3">
      <ul className="space-y-2 text-sm text-fg" aria-label={liveWords.consent_intro}>
        {lines.map((line) => (
          <li key={line} className="flex gap-2">
            <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-fg-subtle" />
            <span>
              <Tagged text={line} badge={perCheckBadge} />
            </span>
          </li>
        ))}
      </ul>
      {worst !== null ? (
        <p className="text-sm text-fg" data-testid="consent-worst-case">
          <Tagged
            text={fillTemplate(copy.playground_live.worst_case, { max_spend: formatUsd(worst) })}
            badge={<EvidenceBadge label="estimate" basis={liveWords.worst_case_basis} subject={liveWords.worst_case_subject} />}
          />
        </p>
      ) : null}
    </div>
  );
}

function ConsentBody({ onClose, returnTo }: Omit<ConsentDialogProps, 'open'>) {
  const id = useId();
  const { storageBlocked } = useBrowserKey();
  const [failed, setFailed] = useState(false);
  const connectOffered = PKCE_FROM_BROWSER;
  const connectPrimary = connectOffered && STATIC_CONNECT_PRIMARY === 'connect';
  const blocked = storageBlocked || failed;

  function connect() {
    const begun = beginConnect(browserKeyStore(), returnTo);
    if (!begun.ok) {
      setFailed(true);
      return;
    }
    pageNav.assign(begun.url);
  }

  const connectButton = connectOffered ? (
    <Button
      type="button"
      variant={connectPrimary ? 'primary' : 'secondary'}
      onClick={connect}
      disabled={blocked}
      aria-describedby={blocked ? `${id}-blocked` : undefined}
    >
      <LogIn aria-hidden="true" />
      {copy.playground_live.connect}
    </Button>
  ) : null;

  return (
    <div className="space-y-4">
      <ConsentLines />
      {blocked ? (
        <p id={`${id}-blocked`} className="rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm text-fg">
          {copy.playground_live.storage_blocked}
        </p>
      ) : null}
      {connectPrimary ? <div className="flex flex-wrap gap-2">{connectButton}</div> : null}
      <div className="space-y-2 border-t border-border pt-4">
        <p className="text-sm font-medium text-fg">{copy.playground_live.paste_key}</p>
        <PasteKey onDone={onClose} />
      </div>
      {!connectPrimary && connectOffered ? <div className="flex flex-wrap gap-2 border-t border-border pt-4">{connectButton}</div> : null}
      {!connectOffered ? <p className="text-xs text-fg-muted">{liveWords.connect_off_paste}</p> : null}
    </div>
  );
}

export function ConsentDialog({ open, onClose, returnTo }: ConsentDialogProps) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      {open ? (
        <DialogContent title={liveWords.consent_title} size="md">
          {isFramed() ? <OpenInTab /> : <ConsentBody onClose={onClose} returnTo={returnTo} />}
        </DialogContent>
      ) : null}
    </Dialog>
  );
}

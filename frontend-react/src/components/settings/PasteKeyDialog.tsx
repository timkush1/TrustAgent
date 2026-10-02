/**
 * Paste a key (#55, P4-PLAN D75): the key (and, for another gateway, its address), which checks to
 * use it for, then one PUT that stores, checks and binds it. The dialog shows the outcome in place
 * (CredentialResult), including "I added credits — check again". The key leaves the form as soon as
 * it is sent; only its masked form is ever shown again.
 */
import { ClipboardPaste, ExternalLink } from 'lucide-react';
import { useState, type FormEvent, type ReactElement } from 'react';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { SEC_KEY_PAGES, secBindingLabel, type SecVendorName } from '@/domain/presentation';
import type { BindingTarget, CredentialWriteResult, SettingsView } from '@/types/api';
import { defaultBindTargets, simulatedOn, type CardInfo } from './credentialModel';
import { CredentialResult } from './CredentialResult';
import { GroupedAction, ProblemNote } from './SettingsKit';
import { usePutCredential, useVerifyCredential } from './useCredentialWrites';

export interface PasteKeyDialogProps {
  card: CardInfo;
  settings: SettingsView | undefined;
  writeReason: Reason | null;
  /** The button's words ("Paste a key", "Replace key"). */
  label: string;
  /** Called with each outcome (the wizard moves on when the fast check is bound). */
  onResult?: (result: CredentialWriteResult) => void;
  trigger?: (props: { disabled: boolean; 'aria-describedby'?: string }) => ReactElement;
  /** The id of a reason written once for the card's actions (GroupedAction). */
  reasonId?: string;
}

function validUrl(value: string): boolean {
  try {
    const u = new URL(value);
    return u.protocol === 'https:' || u.protocol === 'http:';
  } catch {
    return false;
  }
}

export function PasteKeyDialog({ card, settings, writeReason, label, onResult, trigger, reasonId }: PasteKeyDialogProps) {
  const [open, setOpen] = useState(false);
  const [secret, setSecret] = useState('');
  const [baseUrl, setBaseUrl] = useState('');
  const [targets, setTargets] = useState<BindingTarget[]>([]);
  const [wasSimulated, setWasSimulated] = useState(false);
  const put = usePutCredential();
  const verify = useVerifyCredential();
  const result = verify.data ?? put.data ?? null;

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      setTargets(defaultBindTargets(card, settings));
      setWasSimulated(simulatedOn(settings));
    } else {
      setSecret('');
      setBaseUrl('');
      put.reset();
      verify.reset();
    }
  };

  const envManaged = (t: BindingTarget) => settings?.env_managed.includes(`bindings.${t}`) ?? false;
  const problem =
    secret.trim() === '' ? 'Paste the key first' : card.needsBaseUrl && !validUrl(baseUrl.trim()) ? 'Enter the gateway address (https://…)' : null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (problem) return;
    const request = {
      provider: card.provider,
      secret: secret.trim(),
      verify: true,
      ...(card.needsBaseUrl ? { base_url: baseUrl.trim() } : {}),
      ...(targets.length > 0 ? { bind: targets } : {}),
    };
    // The key leaves the form as soon as it is sent.
    setSecret('');
    verify.reset();
    put.mutate({ name: card.name, request }, { onSuccess: (r) => onResult?.(r) });
  };

  const checkAgain = () =>
    verify.mutate({ name: card.name, request: targets.length > 0 ? { bind: targets } : {} }, { onSuccess: (r) => onResult?.(r) });

  const keyPage = card.name in SEC_KEY_PAGES ? SEC_KEY_PAGES[card.name as SecVendorName] : null;
  const failure = verify.error ?? put.error;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <GroupedAction reason={writeReason} reasonId={reasonId}>
        {(p) => (
          <DialogTrigger
            render={
              trigger ? (
                trigger(p)
              ) : (
                <Button size="sm" {...p}>
                  <ClipboardPaste aria-hidden="true" />
                  {label}
                </Button>
              )
            }
          />
        )}
      </GroupedAction>
      <DialogContent
        title={`${card.title} key`}
        description={card.purpose}
        footer={
          result ? (
            <Button variant="primary" onClick={() => onOpenChange(false)}>
              Done
            </Button>
          ) : (
            <>
              <Button variant="secondary" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <DisabledAction reason={problem ? { text: problem } : null}>
                {(p) => (
                  <Button variant="primary" type="submit" form={`paste-${card.name}`} pending={put.isPending} {...p}>
                    Save and check
                  </Button>
                )}
              </DisabledAction>
            </>
          )
        }
      >
        {result ? (
          <CredentialResult result={result} wasSimulated={wasSimulated} onCheckAgain={checkAgain} checkingAgain={verify.isPending} />
        ) : (
          <form id={`paste-${card.name}`} onSubmit={submit} className="space-y-4">
            <Field
              label="API key"
              description={
                keyPage ? (
                  <>
                    Create one at{' '}
                    <a href={keyPage.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 font-medium text-fg underline underline-offset-2">
                      {keyPage.label}
                      <ExternalLink aria-hidden="true" className="size-3" />
                    </a>
                    . It is encrypted here and only ever shown masked.
                  </>
                ) : (
                  'It is encrypted here and only ever shown masked.'
                )
              }
            >
              <Input type="password" value={secret} onValueChange={setSecret} autoComplete="off" spellCheck={false} placeholder={keyPage?.placeholder} />
            </Field>
            {card.needsBaseUrl ? (
              <Field label="Gateway address" description="The key is sent only to this address. Changing it later means pasting the key again.">
                <Input type="url" value={baseUrl} onValueChange={setBaseUrl} autoComplete="off" spellCheck={false} placeholder="https://ai-gateway.example.com/v1" />
              </Field>
            ) : null}
            <fieldset className="space-y-2">
              <legend className="text-sm font-medium text-fg">Use it for</legend>
              {card.targets.map((t) => (
                <Checkbox
                  key={t}
                  label={secBindingLabel(t)}
                  description={envManaged(t) ? 'Set by the environment, so it stays as it is.' : undefined}
                  disabled={envManaged(t)}
                  checked={targets.includes(t)}
                  onCheckedChange={(on) => setTargets((cur) => (on === true ? [...cur, t] : cur.filter((x) => x !== t)))}
                />
              ))}
              <p className="text-xs text-fg-muted">The key is used only if the check passes.</p>
            </fieldset>
            {failure ? <ProblemNote error={failure} title="The key was not saved" /> : null}
          </form>
        )}
        {result && failure ? <ProblemNote error={failure} title="The check did not run" /> : null}
      </DialogContent>
    </Dialog>
  );
}

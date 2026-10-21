/**
 * Paste an OpenRouter key (D102): a password field that password managers and spellcheck leave
 * alone, with no form and so no submit path (Enter uses the key and never navigates). The key must
 * have the OpenRouter shape, then the free key check (`GET /api/v1/key`, never counted) runs: a key
 * OpenRouter refuses is forgotten and said so; a check that cannot get through (an extension, the
 * network) keeps the key, since the check itself proves nothing then. Inside a frame it renders
 * only "Open in its own tab" (D106). Owned by WEB-LIVE (LV-6).
 */
import { useId, useRef, useState, type KeyboardEvent } from 'react';
import { Button } from '@/components/ui/button';
import { fieldControlClass } from '@/components/ui/control-styles';
import { copy } from '@/domain/presentation';
import { liveText } from './liveText';
import { cn } from '@/lib/cn';
import { errorKind } from '@/lib/openrouter/errors';
import { isFramed } from '@/lib/openrouter/frame';
import { browserKeyStore, isKeyShape } from '@/lib/openrouter/keyStore';
import { checkKeyOnce } from './keyCheck';
import { OpenInTab } from './OpenInTab';
import { OpenRouterLinks } from './OpenRouterLinks';

export interface PasteKeyProps {
  /** Called once the key is stored for this tab. */
  onDone: () => void;
}

type Problem = 'shape' | 'refused' | null;

export function PasteKey({ onDone }: PasteKeyProps) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [problem, setProblem] = useState<Problem>(null);
  const [checking, setChecking] = useState(false);

  if (isFramed()) return <OpenInTab />;

  async function save() {
    if (checking) return;
    const store = browserKeyStore();
    const key = (input.current?.value ?? '').trim();
    if (!isKeyShape(key) || !store.set(key)) {
      setProblem('shape');
      input.current?.focus();
      return;
    }
    setProblem(null);
    setChecking(true);
    try {
      await checkKeyOnce(key, store);
    } catch (err) {
      if (errorKind(err) === 'auth') {
        setChecking(false);
        setProblem('refused');
        input.current?.focus();
        return;
      }
    }
    if (input.current) input.current.value = '';
    setChecking(false);
    onDone();
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    void save();
  }

  const problemText = problem === 'shape' ? copy.playground_live.key_invalid : problem === 'refused' ? copy.playground_live.errors.unauthorized : null;
  return (
    <div className="space-y-2">
      <label htmlFor={`${id}-key`} className="block text-sm font-medium text-fg">
        {liveText.paste_label}
      </label>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input
          ref={input}
          id={`${id}-key`}
          name="openrouter-key"
          type="password"
          autoComplete="off"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          data-1p-ignore=""
          data-lpignore="true"
          data-form-type="other"
          aria-invalid={problem !== null}
          aria-describedby={`${id}-help${problemText ? ` ${id}-problem` : ''}`}
          onKeyDown={onKeyDown}
          onChange={() => setProblem(null)}
          className={cn(fieldControlClass, 'h-8 flex-1 font-mono outline-offset-2 aria-invalid:border-tone-danger')}
          placeholder="sk-or-v1-…"
        />
        <Button type="button" variant="secondary" onClick={() => void save()} pending={checking}>
          {checking ? liveText.paste_checking : liveText.paste_save}
        </Button>
      </div>
      <p id={`${id}-help`} className="text-xs text-fg-muted">
        <OpenRouterLinks text={liveText.paste_help} />
      </p>
      {problemText ? (
        <p id={`${id}-problem`} role="alert" className="text-xs font-medium text-fg">
          {problemText}
        </p>
      ) : null}
    </div>
  );
}

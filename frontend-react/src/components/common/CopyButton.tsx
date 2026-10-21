/**
 * Copies a value (an id, a key, a command) and says so: the icon turns into a check for two
 * seconds and a polite status announces "Copied". When the browser refuses (plain http, a denied
 * permission) it says that too, so the viewer knows to select and copy by hand.
 */
import { Check, Copy, TriangleAlert } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { IconButton } from '@/components/ui/icon-button';
import { copyText } from './clipboard';

type CopyState = 'idle' | 'copied' | 'failed';

export interface CopyButtonProps {
  value: string;
  /** Accessible name and tooltip ("Copy trace id"). */
  label?: string;
  /** Show the label as text (a secondary button) instead of an icon-only button. */
  showLabel?: boolean;
  size?: 'sm' | 'md';
  onCopied?: () => void;
  className?: string;
}

const RESET_MS = 2000;

const statusText: Record<CopyState, string> = {
  idle: '',
  copied: 'Copied',
  failed: "Couldn't copy. Select the text and copy it with your keyboard.",
};

export function CopyButton({ value, label = 'Copy', showLabel = false, size = 'sm', onCopied, className }: CopyButtonProps) {
  const [state, setState] = useState<CopyState>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = async () => {
    const ok = await copyText(value);
    setState(ok ? 'copied' : 'failed');
    if (ok) onCopied?.();
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState('idle'), RESET_MS);
  };

  const Icon = state === 'copied' ? Check : state === 'failed' ? TriangleAlert : Copy;
  return (
    <>
      {showLabel ? (
        <Button variant="secondary" size={size} onClick={() => void copy()} className={className} data-copy-state={state}>
          <Icon aria-hidden="true" />
          {state === 'copied' ? 'Copied' : label}
        </Button>
      ) : (
        <IconButton icon={Icon} aria-label={label} size={size} onClick={() => void copy()} className={className} data-copy-state={state} />
      )}
      <span role="status" className="sr-only">
        {statusText[state]}
      </span>
    </>
  );
}

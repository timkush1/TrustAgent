/**
 * What a key surface shows inside a frame (D106): why, and a link that opens this page in its own
 * tab. No key input and no exchange. Owned by WEB-LIVE (LV-6).
 */
import { SquareArrowOutUpRight } from 'lucide-react';
import { buttonVariants } from '@/components/ui/button-variants';
import { copy } from '@/domain/presentation';

export function OpenInTab({ href }: { href?: string }) {
  const target = href ?? (typeof location === 'undefined' ? '' : location.href);
  return (
    <div className="space-y-3" data-testid="live-framed">
      <p className="text-sm text-fg">{copy.playground_live.frame_blocked}</p>
      <a href={target} target="_blank" rel="noopener noreferrer" className={buttonVariants({ variant: 'primary' })}>
        <SquareArrowOutUpRight aria-hidden="true" />
        {copy.playground_live.open_in_tab}
      </a>
    </div>
  );
}

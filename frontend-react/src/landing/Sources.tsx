/**
 * "Where every number on this page comes from": the footer's source list, loaded after the first
 * paint. Three groups: every landing claim with its label, basis, caveat and source; the vendor
 * prices the calculator offers for the deep-audit judge; and the documentation the comparison
 * cites. A label chip (or a shared link to #source-…) opens the list and scrolls to its entry.
 */
import { ArrowRight, ArrowUpRight } from 'lucide-react';
import { useEffect, useRef, type ReactNode } from 'react';
import { CLAIMS_AS_OF, LABEL_TEXT, landingClaims, sourceAnchor } from './claims';
import { compareSources, sourceHost } from './compareData';
import { judgeOptions } from './costModel';
import { priceAnchorId } from './priceSources';

/** Opens the list when a label chip (or a shared link) points into it, now or later. */
function useOpenOnHash(ref: React.RefObject<HTMLDetailsElement | null>) {
  useEffect(() => {
    const open = () => {
      if (!window.location.hash.startsWith('#source-') || !ref.current) return;
      ref.current.open = true;
      document.getElementById(decodeURIComponent(window.location.hash.slice(1)))?.scrollIntoView({ block: 'center' });
    };
    open();
    window.addEventListener('hashchange', open);
    return () => window.removeEventListener('hashchange', open);
  }, [ref]);
}

function SourceLink({ url, children }: { url: string | null | undefined; children?: ReactNode }) {
  if (!url) return <>Computed in this project</>;
  return (
    <a href={url} className="inline-flex items-center gap-0.5 underline underline-offset-2 hover:text-fg" rel="noreferrer">
      {children ?? 'Source'}
      <ArrowUpRight aria-hidden="true" className="size-3" />
    </a>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="mt-8">
      <h3 className="text-xs font-semibold tracking-wide text-fg-muted uppercase">{title}</h3>
      <ul className="mt-4 grid grid-cols-1 gap-x-10 gap-y-5 md:grid-cols-2" aria-label={title}>
        {children}
      </ul>
    </div>
  );
}

const perMTok = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 3 });
/** A price note without the table's internal "[A]" (assumption) marker. */
const noteText = (note: string | undefined) => (note ?? '').replace(/^\[[A-Z]\]\s*/, '');

export function Sources() {
  const ref = useRef<HTMLDetailsElement>(null);
  useOpenOnHash(ref);
  const claims = landingClaims();
  const prices = judgeOptions().filter((o) => !o.local);
  const compare = compareSources();
  return (
    <details ref={ref} id="sources" className="group">
      <summary className="cursor-pointer list-none text-sm font-semibold text-fg marker:hidden">
        <span className="inline-flex items-center gap-1.5">
          <ArrowRight aria-hidden="true" className="size-4 text-fg-muted transition-transform group-open:rotate-90" />
          Where every number on this page comes from
        </span>
      </summary>
      <p className="mt-3 max-w-3xl text-sm text-fg-muted">
        Each figure carries a label. <b className="font-medium text-fg">Measured</b>: our own benchmark run.{' '}
        <b className="font-medium text-fg">Estimate</b>: our arithmetic from published prices. <b className="font-medium text-fg">Third-party</b> and{' '}
        <b className="font-medium text-fg">Vendor</b>: someone else’s published numbers. <b className="font-medium text-fg">Simulated</b>: the demo, not
        model output. Claims as of {CLAIMS_AS_OF}.
      </p>
      <Group title="Figures">
        {claims.map((c) => (
          <li key={c.id} id={sourceAnchor(c.id)} className="scroll-mt-24 text-sm">
            <p className="text-fg">
              <span className="font-medium">{c.text}:</span> <span className="tabular">{c.display}</span> <span className="text-fg-muted">({c.labelText})</span>
            </p>
            <p className="mt-0.5 text-fg-muted">{c.basis}</p>
            {c.caveat ? <p className="mt-0.5 text-fg-muted">{c.caveat}</p> : null}
            <p className="mt-0.5 text-xs text-fg-muted">
              <SourceLink url={c.sourceUrl} />
              {c.asOf ? ` · as of ${c.asOf}` : ''}
            </p>
          </li>
        ))}
      </Group>
      <Group title="Judge prices in the calculator">
        {prices.map(({ entry, name }) => (
          <li key={entry.provider + entry.match} id={sourceAnchor(priceAnchorId(entry))} className="scroll-mt-24 text-sm">
            <p className="text-fg">
              <span className="font-medium">{name}:</span>{' '}
              <span className="tabular">
                {perMTok.format(entry.input_per_mtok)} input · {perMTok.format(entry.output_per_mtok)} output per million tokens
              </span>{' '}
              <span className="text-fg-muted">({LABEL_TEXT.vendor})</span>
            </p>
            {entry.note ? <p className="mt-0.5 text-fg-muted">{noteText(entry.note)}</p> : null}
            <p className="mt-0.5 text-xs text-fg-muted">
              <SourceLink url={entry.source} />
              {entry.as_of ? ` · as of ${entry.as_of}` : ''} · contracts/pricing/prices.v1.json
            </p>
          </li>
        ))}
      </Group>
      <Group title="Comparison sources">
        {compare.map((s) => (
          <li key={s.url} className="text-sm">
            <p className="text-fg">
              <SourceLink url={s.url}>{s.vendor} docs</SourceLink>{' '}
              <span className="text-fg-muted">
                · {sourceHost(s.url)} · read <span className="whitespace-nowrap">{s.asOf}</span>
              </span>
            </p>
            <p className="mt-0.5 text-fg-muted">{s.cells.join('; ')}</p>
          </li>
        ))}
      </Group>
    </details>
  );
}

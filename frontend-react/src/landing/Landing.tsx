/**
 * The landing page (SPEC §8.1): at most seven sections, one hero, every number from
 * contracts/claims/value-claims.v1.json with its label chip, and a footer listing where each
 * number comes from. A separate Vite entry: no router, no chart library (SPEC §3.3).
 */
import { ArrowRight, ArrowUpRight } from 'lucide-react';
import { useEffect, useRef } from 'react';
import { Affordable } from './Affordable';
import { CLAIMS_AS_OF, landingClaims, sourceAnchor } from './claims';
import { Compare } from './Compare';
import { DEMO_HREF, REPO_URL } from './content';
import { Hero } from './Hero';
import { HowItWorks } from './HowItWorks';
import { SelfHost } from './SelfHost';
import { TryIt } from './TryIt';
import { GitHubMark, Logo, ThemeButton } from './ui';
import { buttonClass, container } from './styles';
import { WriteChecks } from './WriteChecks';

const navLinks = [
  { href: '#how', label: 'How it works' },
  { href: '#cost', label: 'Cost' },
  { href: '#compare', label: 'Compare' },
  { href: '#self-host', label: 'Run it' },
];

function Header() {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-bg/90 backdrop-blur-sm">
      <div className={`${container} flex h-16 items-center gap-6`}>
        <a href="#top" className="flex items-center gap-2 text-base font-semibold text-fg">
          <Logo className="size-5" />
          TrustAgent
        </a>
        <nav aria-label="Sections" className="flex items-center gap-1 max-md:hidden">
          {navLinks.map((l) => (
            <a key={l.href} href={l.href} className={buttonClass.ghost}>
              {l.label}
            </a>
          ))}
        </nav>
        <div className="ml-auto flex items-center gap-1">
          <ThemeButton />
          <a href={REPO_URL} className={`${buttonClass.ghost} w-9 px-0`} aria-label="TrustAgent on GitHub" title="TrustAgent on GitHub">
            <GitHubMark className="size-4" />
          </a>
          <a href={DEMO_HREF} className="ml-2 inline-flex h-9 items-center gap-1.5 rounded-md bg-fg px-3.5 text-sm font-medium text-fg-inverse transition-opacity hover:opacity-90 max-sm:px-3">
            Try the demo
            <ArrowRight aria-hidden="true" className="size-4 max-sm:hidden" />
          </a>
        </div>
      </div>
    </header>
  );
}

/** Opens the sources list when a label chip (or a shared link) points into it. */
function useOpenSourcesOnHash(ref: React.RefObject<HTMLDetailsElement | null>) {
  useEffect(() => {
    const open = () => {
      if (!window.location.hash.startsWith('#source-') || !ref.current) return;
      ref.current.open = true;
      document.getElementById(window.location.hash.slice(1))?.scrollIntoView({ block: 'center' });
    };
    open();
    window.addEventListener('hashchange', open);
    return () => window.removeEventListener('hashchange', open);
  }, [ref]);
}

function Footer() {
  const ref = useRef<HTMLDetailsElement>(null);
  useOpenSourcesOnHash(ref);
  const claims = landingClaims();
  return (
    <footer className="border-t border-border bg-surface-sunken">
      <div className={`${container} py-12`}>
        <details ref={ref} id="sources" className="group">
          <summary className="cursor-pointer list-none text-sm font-semibold text-fg marker:hidden">
            <span className="inline-flex items-center gap-1.5">
              <ArrowRight aria-hidden="true" className="size-4 text-fg-muted transition-transform group-open:rotate-90" />
              Where every number on this page comes from
            </span>
          </summary>
          <p className="mt-3 max-w-3xl text-sm text-fg-muted">
            Each figure carries a label. <b className="font-medium text-fg">Measured</b>: our own benchmark run.{' '}
            <b className="font-medium text-fg">Estimate</b>: our arithmetic from published prices.{' '}
            <b className="font-medium text-fg">Third-party</b> and <b className="font-medium text-fg">Vendor</b>: someone else’s
            published numbers. <b className="font-medium text-fg">Simulated</b>: the demo, not model output. Claims as of {CLAIMS_AS_OF}.
          </p>
          <ul className="mt-6 grid grid-cols-1 gap-x-10 gap-y-5 md:grid-cols-2">
            {claims.map((c) => (
              <li key={c.id} id={sourceAnchor(c.id)} className="scroll-mt-24 text-sm">
                <p className="text-fg">
                  <span className="font-medium">{c.text}:</span> <span className="tabular">{c.display}</span>{' '}
                  <span className="text-fg-muted">({c.labelText})</span>
                </p>
                <p className="mt-0.5 text-fg-muted">{c.basis}</p>
                {c.caveat ? <p className="mt-0.5 text-fg-muted">{c.caveat}</p> : null}
                <p className="mt-0.5 text-xs text-fg-subtle">
                  {c.sourceUrl ? (
                    <a href={c.sourceUrl} className="inline-flex items-center gap-0.5 underline underline-offset-2 hover:text-fg" rel="noreferrer">
                      Source
                      <ArrowUpRight aria-hidden="true" className="size-3" />
                    </a>
                  ) : (
                    'Computed in this project'
                  )}
                  {c.asOf ? ` · as of ${c.asOf}` : ''}
                </p>
              </li>
            ))}
          </ul>
        </details>
        <div className="mt-10 flex flex-wrap items-center justify-between gap-4 border-t border-border pt-6 text-sm text-fg-muted">
          <span className="flex items-center gap-2">
            <Logo className="size-4" />
            TrustAgent · open source, MIT licensed
          </span>
          <a href={REPO_URL} className="inline-flex items-center gap-1.5 hover:text-fg">
            <GitHubMark className="size-4" />
            github.com/timkush1/TrustAgent
          </a>
        </div>
      </div>
    </footer>
  );
}

export function Landing() {
  return (
    <div id="top" className="min-h-dvh bg-bg text-fg">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-surface-raised focus:px-3 focus:py-2 focus:text-sm"
      >
        Skip to content
      </a>
      <Header />
      <main id="main" tabIndex={-1} className="outline-none">
        <Hero />
        <HowItWorks />
        <Affordable />
        <WriteChecks />
        <TryIt />
        <Compare />
        <SelfHost />
      </main>
      <Footer />
    </div>
  );
}

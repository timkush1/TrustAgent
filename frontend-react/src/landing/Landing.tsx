/**
 * The landing page (SPEC §8.1): at most seven sections, one hero, every number from
 * contracts/claims/value-claims.v1.json with its label chip, and a footer listing where each
 * number comes from. A separate Vite entry: no router, no chart library (SPEC §3.3).
 *
 * The first load (110 KB gzip budget, npm run size) is the header, the hero, How it works and the
 * cost section. The sections below them, and the footer's source list, are their own chunks: they
 * start loading as soon as the page runs, each behind a placeholder of about its height.
 */
import { ArrowRight, ChevronRight } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { Affordable } from './Affordable';
import { DEMO_HREF, REPO_URL } from './content';
import { Hero } from './Hero';
import { HowItWorks } from './HowItWorks';
import { lazyPartsLoaded } from './lazyLoaders';
import { Compare, LazyPart, SelfHost, Sources, TryIt, WriteChecks } from './lazyParts';
import { GitHubMark, Logo, ThemeButton } from './ui';
import { buttonClass, container } from './styles';

const navLinks = [
  { href: '#how', label: 'How it works' },
  { href: '#cost', label: 'Cost' },
  { href: '#checks', label: 'Checks' },
  { href: '#try', label: 'Try it' },
  { href: '#compare', label: 'Compare' },
  { href: '#self-host', label: 'Run it' },
];

/**
 * The section links. Below 1024 px they are a second header row that scrolls sideways; while more
 * links lie past the right edge, a chevron there says so and scrolls them into view.
 */
function SectionNav() {
  const ref = useRef<HTMLElement>(null);
  const [more, setMore] = useState(false);
  useEffect(() => {
    const nav = ref.current;
    if (!nav) return;
    const update = () => setMore(nav.scrollWidth - nav.clientWidth - nav.scrollLeft > 4);
    update();
    nav.addEventListener('scroll', update, { passive: true });
    window.addEventListener('resize', update);
    return () => {
      nav.removeEventListener('scroll', update);
      window.removeEventListener('resize', update);
    };
  }, []);
  return (
    <div className="relative max-lg:order-last max-lg:-mx-2 max-lg:w-[calc(100%+1rem)]">
      <nav ref={ref} aria-label="Sections" className="flex items-center gap-0.5 max-lg:overflow-x-auto max-lg:pb-2 max-lg:[scrollbar-width:none]">
        {navLinks.map((l) => (
          <a key={l.href} href={l.href} className={`${buttonClass.ghost} shrink-0 whitespace-nowrap`}>
            {l.label}
          </a>
        ))}
      </nav>
      {more ? (
        <button
          type="button"
          onClick={() => ref.current?.scrollBy({ left: ref.current.clientWidth / 2, behavior: 'smooth' })}
          aria-label="Show more section links"
          data-nav-more
          className="absolute top-0 right-0 inline-flex h-9 w-9 items-center justify-center border-l border-border bg-bg text-fg-muted hover:text-fg lg:hidden"
        >
          <ChevronRight aria-hidden="true" className="size-4" />
        </button>
      ) : null}
    </div>
  );
}

function Header() {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-bg/90 backdrop-blur-sm">
      {/* One nav: inline on wide screens, a second row that scrolls sideways below 1024 px. */}
      <div className={`${container} flex flex-wrap items-center gap-x-6`}>
        <a href="#top" className="flex h-16 items-center gap-2 text-base font-semibold text-fg">
          <Logo className="size-5" />
          TrustAgent
        </a>
        <SectionNav />
        <div className="ml-auto flex h-16 items-center gap-1">
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

function Footer() {
  return (
    <footer className="border-t border-border bg-surface-sunken">
      <div className={`${container} py-12`}>
        <LazyPart minHeight="1.5rem" section={false}>
          <Sources />
        </LazyPart>
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

/**
 * A link into a section from another page (the replay's "Run it yourself" opens #self-host): the
 * browser looks for the anchor before React has rendered it, and the sections below the fold load
 * after the first paint, so scroll there once every part of the page is in place. A source anchor
 * (#source-…) is the source list's own business.
 */
function useScrollToHashOnLoad() {
  useEffect(() => {
    const id = decodeURIComponent(window.location.hash.slice(1));
    if (!id || id.startsWith('source-')) return undefined;
    let cancelled = false;
    let frames = 0;
    const scroll = () => {
      if (cancelled) return;
      // React commits a resolved lazy section on its own schedule: wait for the placeholders to go.
      if (document.querySelector('[data-lazy-placeholder]') && frames++ < 120) {
        window.requestAnimationFrame(scroll);
        return;
      }
      document.getElementById(id)?.scrollIntoView({ block: 'start', behavior: 'instant' });
    };
    void lazyPartsLoaded().then(() => window.requestAnimationFrame(scroll));
    return () => {
      cancelled = true;
    };
  }, []);
}

export function Landing() {
  useScrollToHashOnLoad();
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
        <LazyPart minHeight="44rem">
          <WriteChecks />
        </LazyPart>
        <LazyPart minHeight="40rem">
          <TryIt />
        </LazyPart>
        <LazyPart minHeight="60rem">
          <Compare />
        </LazyPart>
        <LazyPart minHeight="34rem">
          <SelfHost />
        </LazyPart>
      </main>
      <Footer />
    </div>
  );
}

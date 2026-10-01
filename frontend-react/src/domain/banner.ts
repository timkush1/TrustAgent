/**
 * The single full-width banner (SPEC §3.3): at most one, and only for demo, read-only, budget
 * exhausted, a rejected fast-check key (unauthorized / no credits) or an open breaker on an
 * enforcing route. When several apply, the one that changes what users receive wins.
 */
import type { EffectiveRoute, SystemStatus } from '@/types/api';
import { copy, fillTemplate } from './presentation';

export type BannerId = 'unauthorized' | 'no_credits' | 'budget_exhausted' | 'breaker_open' | 'readonly' | 'demo';

export interface BannerView {
  id: BannerId;
  title: string;
  body: string;
  action: string | null;
  tone: 'neutral' | 'warning' | 'danger';
}

export interface BannerInputs {
  demo: boolean;
  readonly: boolean;
  status: SystemStatus | null;
  /** Routes in block or fallback mode (enforcingRoutes); the breaker banner needs one. */
  enforcingRoutes?: { name: string; fail: 'open' | 'closed' }[];
}

/**
 * The routes that enforce decisions: configured to block or fall back. The configured mode, not
 * the effective one, because an open breaker is itself one of the reasons a route acts as flag.
 */
export function enforcingRoutes(routes: readonly Pick<EffectiveRoute, 'name' | 'mode' | 'fail'>[]): { name: string; fail: 'open' | 'closed' }[] {
  return routes.filter((r) => r.mode === 'block' || r.mode === 'fallback').map((r) => ({ name: r.name, fail: r.fail }));
}

function view(id: BannerId, tone: BannerView['tone'], vars: Record<string, string> = {}): BannerView {
  const b = copy.banners[id] as { title: string; body: string; action?: string };
  return {
    id,
    tone,
    title: fillTemplate(b.title, vars),
    body: fillTemplate(b.body, vars),
    action: b.action ? fillTemplate(b.action, vars) : null,
  };
}

export function selectBanner(inputs: BannerInputs): BannerView | null {
  const dm = inputs.status?.decision_model.state;
  if (dm === 'unauthorized') return view('unauthorized', 'danger');
  if (dm === 'no_credits') return view('no_credits', 'danger');
  if (inputs.status?.budgets.decision_exhausted) return view('budget_exhausted', 'warning');
  const enforcing = inputs.enforcingRoutes?.[0];
  if (dm === 'breaker_open' && enforcing) {
    return view('breaker_open', 'warning', { route: enforcing.name, fail: enforcing.fail });
  }
  if (inputs.readonly) return view('readonly', 'neutral');
  if (inputs.demo) return view('demo', 'neutral');
  return null;
}

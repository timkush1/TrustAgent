import { describe, expect, it } from 'vitest';
import { systemStatus } from '@/test/status';
import { enforcingRoutes, selectBanner } from './banner';

describe('selectBanner', () => {
  it('shows nothing on a normal stack', () => {
    expect(selectBanner({ demo: false, readonly: false, status: null })).toBeNull();
  });

  it('shows the demo banner with the command to leave it', () => {
    const b = selectBanner({ demo: true, readonly: false, status: null });
    expect(b).toMatchObject({ id: 'demo', title: 'Demo stack', action: 'To use your own keys: make demo-down && make up' });
  });

  it('prefers the problem that changes what users receive', () => {
    const dm = systemStatus().decision_model;
    const unauthorized = systemStatus({ decision_model: { ...dm, state: 'unauthorized' } });
    expect(selectBanner({ demo: true, readonly: true, status: unauthorized })?.id).toBe('unauthorized');
    const exhausted = systemStatus({ budgets: { ...systemStatus().budgets, decision_exhausted: true } });
    expect(selectBanner({ demo: true, readonly: true, status: exhausted })?.id).toBe('budget_exhausted');
    expect(selectBanner({ demo: true, readonly: true, status: null })?.id).toBe('readonly');
  });

  it('shows the breaker banner only for an enforcing route', () => {
    const open = systemStatus({ decision_model: { ...systemStatus().decision_model, state: 'breaker_open' } });
    expect(selectBanner({ demo: false, readonly: false, status: open })).toBeNull();
    const b = selectBanner({ demo: false, readonly: false, status: open, enforcingRoutes: [{ name: 'payments', fail: 'closed' }] });
    expect(b).toMatchObject({ id: 'breaker_open' });
    expect(b?.body).toContain('Route payments enforces decisions');
    expect(b?.body).toContain('(closed)');
  });

  it('takes the enforcing routes from the policy by their configured mode', () => {
    // An open breaker is itself why a block route acts as flag, so the configured mode counts.
    const routes = enforcingRoutes([
      { name: 'support-bot', mode: 'flag', fail: 'open' },
      { name: 'help-center', mode: 'block', fail: 'open' },
      { name: 'payments-faq', mode: 'fallback', fail: 'closed' },
    ]);
    expect(routes).toEqual([
      { name: 'help-center', fail: 'open' },
      { name: 'payments-faq', fail: 'closed' },
    ]);
  });
});

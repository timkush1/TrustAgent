/**
 * The Health pill's checklist (SPEC §3.3 top bar), computed from what the dashboard knows:
 * GET /health, GET /api/status when available, the WS connection, the `hello` status summary and
 * `status` transitions. A component the dashboard cannot see is "unknown", never "running".
 */
import type { ComponentStateName, DecisionModelStatus, HealthStatus, SystemStatus, WsHello } from '@/types/api';
import type { ConnectionState } from '@/data/stream/types';
import type { ComponentName, ComponentTransition } from '@/data/stream/streamStore';
import { TIER, copy, fillTemplate } from './presentation';

export type CheckState = 'ok' | 'issue' | 'unknown' | 'info';

export interface HealthCheck {
  id: 'proxy' | 'live' | 'fast_check' | 'engine' | 'judge' | 'database';
  label: string;
  state: CheckState;
  detail: string;
}

export interface HealthInputs {
  health: { data?: HealthStatus; error?: unknown; pending: boolean };
  status: SystemStatus | null;
  connection: ConnectionState;
  hello: WsHello | null;
  components: Partial<Record<ComponentName, ComponentTransition>>;
}

export interface HealthSummary {
  checks: HealthCheck[];
  issues: number;
  /** Pill tone: danger when the proxy itself is unreachable. */
  tone: 'success' | 'warning' | 'danger' | 'neutral';
  label: string;
}

function componentState(state: ComponentStateName | string | undefined, name: string): Pick<HealthCheck, 'state' | 'detail'> {
  switch (state) {
    case 'up':
      return { state: 'ok', detail: `${name} is up` };
    case 'degraded':
      return { state: 'issue', detail: `${name} is degraded` };
    case 'down':
      return { state: 'issue', detail: `${name} is down` };
    case 'disabled':
      return { state: 'info', detail: `${name} is turned off` };
    default:
      return { state: 'unknown', detail: 'Unknown (status not available)' };
  }
}

const decisionModelWords: Record<DecisionModelStatus['state'], { state: CheckState; detail: string }> = {
  ready: { state: 'ok', detail: 'Ready' },
  mock: { state: 'info', detail: 'Simulated (mock checks)' },
  checking: { state: 'unknown', detail: 'Checking the connection…' },
  off: { state: 'issue', detail: 'Turned off: answers pass unchecked' },
  degraded: { state: 'issue', detail: 'Degraded' },
  breaker_open: { state: 'issue', detail: 'Paused after repeated errors' },
  unauthorized: { state: 'issue', detail: 'Key rejected' },
  no_credits: { state: 'issue', detail: 'No OpenRouter credits' },
  budget_exhausted: { state: 'issue', detail: "Today's budget is used up" },
  misconfigured: { state: 'issue', detail: 'Misconfigured' },
  unreachable: { state: 'issue', detail: 'Unreachable' },
};

function proxyCheck(h: HealthInputs['health']): HealthCheck {
  const label = 'Proxy';
  // The latest poll wins: a proxy that answered earlier but fails now is unreachable.
  if (h.error) return { id: 'proxy', label, state: 'issue', detail: 'Unreachable: the dashboard cannot load data' };
  if (h.data) return { id: 'proxy', label, state: 'ok', detail: `Reachable · version ${h.data.version}` };
  return { id: 'proxy', label, state: 'unknown', detail: copy.states.loading };
}

function liveCheck(c: ConnectionState): HealthCheck {
  const label = 'Live updates';
  switch (c.phase) {
    case 'open':
      return { id: 'live', label, state: 'ok', detail: 'Connected' };
    case 'connecting':
    case 'idle':
      return { id: 'live', label, state: 'unknown', detail: 'Connecting…' };
    case 'unauthorized':
      return { id: 'live', label, state: 'issue', detail: 'Refused: this session may not read live events' };
    case 'reconnecting':
      return { id: 'live', label, state: 'issue', detail: `Reconnecting (attempt ${c.attempt})` };
  }
}

export function summarizeHealth(inputs: HealthInputs): HealthSummary {
  const { status, hello, components } = inputs;
  const checks: HealthCheck[] = [proxyCheck(inputs.health), liveCheck(inputs.connection)];

  // `hello.status` is read defensively: a hub that omits it must not take the shell down.
  const lite: Partial<WsHello['status']> = hello?.status ?? {};

  // Fast check (decision model): full status, else the hello summary.
  const dmState = (components.decision_model?.state as DecisionModelStatus['state'] | undefined) ??
    status?.decision_model.state ??
    lite.decision_model;
  const dm = dmState ? decisionModelWords[dmState] : undefined;
  checks.push({
    id: 'fast_check',
    label: TIER.fast_check,
    state: dm?.state ?? 'unknown',
    detail: dm?.detail ?? 'Unknown (status not available)',
  });

  // Deep audit engine: a status transition, the full status, hello, then /health's ping.
  const engineState = components.engine?.state ?? status?.engine.state ?? lite.engine;
  if (engineState) {
    checks.push({ id: 'engine', label: `${TIER.deep_audit} engine`, ...componentState(engineState, 'The engine') });
  } else if (inputs.health.data && !inputs.health.error) {
    checks.push({
      id: 'engine',
      label: `${TIER.deep_audit} engine`,
      state: inputs.health.data.audit_engine ? 'ok' : 'issue',
      detail: inputs.health.data.audit_engine ? 'Reachable' : 'Unreachable: deep audits cannot run',
    });
  } else {
    checks.push({ id: 'engine', label: `${TIER.deep_audit} engine`, state: 'unknown', detail: 'Unknown (status not available)' });
  }

  const judgeState = components.judge?.state ?? status?.judge.state ?? lite.judge;
  checks.push({ id: 'judge', label: `${TIER.deep_audit} judge`, ...componentState(judgeState, 'The judge') });

  if (status) {
    const p = status.persistence;
    checks.push(
      !p.configured
        ? { id: 'database', label: 'Database', state: 'info', detail: copy.states.applies_until_restart }
        : { id: 'database', label: 'Database', ...componentState(components.database?.state ?? p.state, 'The database') },
    );
  }

  const issues = checks.filter((c) => c.state === 'issue').length;
  const proxyDown = checks[0].state === 'issue';
  const tone = proxyDown ? 'danger' : issues > 0 ? 'warning' : checks[0].state === 'ok' ? 'success' : 'neutral';
  const label =
    issues === 0
      ? copy.health.pill_ok
      : fillTemplate(copy.health.pill_issues, { count: issues }).replace(/\bissues\b/, issues === 1 ? 'issue' : 'issues');
  return { checks, issues, tone, label };
}

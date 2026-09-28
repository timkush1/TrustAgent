/**
 * Hooks for system-wide state: health, full status (when the proxy serves it), demo and
 * read-only flags, the Health pill summary and the banner. Every source is optional; the UI
 * says "unknown" rather than guessing.
 */
import { useQuery } from '@tanstack/react-query';
import { useMemo } from 'react';
import { useDataSource } from '@/app/services';
import { selectBanner, type BannerView } from '@/domain/banner';
import { summarizeHealth, type HealthSummary } from '@/domain/health';
import type { SystemStatus } from '@/types/api';
import { healthQuery, isStatusUnavailable, statusQuery } from './queries';
import { useStreamStore } from './stream/streamStore';

export function useHealth() {
  return useQuery(healthQuery(useDataSource()));
}

/** GET /api/status, or null while loading or when this proxy does not serve it. */
export function useSystemStatus(): { status: SystemStatus | null; unavailable: boolean } {
  const q = useQuery(statusQuery(useDataSource()));
  return { status: q.data ?? null, unavailable: isStatusUnavailable(q.error) };
}

export interface AppMode {
  demo: boolean;
  readonly: boolean;
}

/** Demo and read-only flags from any source that reports them (/health, hello, /api/status). */
export function useAppMode(): AppMode {
  const health = useHealth();
  const { status } = useSystemStatus();
  const hello = useStreamStore((s) => s.hello);
  const demo = Boolean(health.data?.demo || hello?.demo || status?.demo);
  const readonly = Boolean(hello?.readonly || status?.readonly);
  return useMemo(() => ({ demo, readonly }), [demo, readonly]);
}

export function useHealthSummary(): HealthSummary {
  const health = useHealth();
  const { status } = useSystemStatus();
  const connection = useStreamStore((s) => s.connection);
  const hello = useStreamStore((s) => s.hello);
  const components = useStreamStore((s) => s.components);
  return useMemo(
    () =>
      summarizeHealth({
        health: { data: health.data, error: health.error ?? undefined, pending: health.isPending },
        status,
        connection,
        hello,
        components,
      }),
    [health.data, health.error, health.isPending, status, connection, hello, components],
  );
}

export function useBanner(): BannerView | null {
  const mode = useAppMode();
  const { status } = useSystemStatus();
  return useMemo(() => selectBanner({ ...mode, status }), [mode, status]);
}

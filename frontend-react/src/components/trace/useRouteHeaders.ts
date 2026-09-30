/**
 * The `headers` setting (full or action_only) of the route a request ran on, at the policy version
 * it ran under: the current policy when that is the version, else that stored version. Null while
 * it is not known (not asked yet, loading, or a proxy that does not serve policies).
 */
import { useQuery } from '@tanstack/react-query';
import { useDataSource } from '@/app/services';
import { policiesQuery, policyVersionQuery } from '@/data/queries';
import type { HeadersSetting } from '@/domain/decisionHeaders';
import type { PolicyDoc } from '@/types/api';

/** A route's setting merged over the policy defaults (`full` when neither sets it). */
export function routeHeadersSetting(doc: PolicyDoc, route: string): HeadersSetting {
  return doc.routes?.find((r) => r.name === route)?.headers ?? doc.defaults?.headers ?? 'full';
}

export function useRouteHeaders(route: string, version: number | null, enabled: boolean): HeadersSetting | null {
  const ds = useDataSource();
  const current = useQuery({ ...policiesQuery(ds), enabled: enabled && version !== null });
  const isCurrent = current.data !== undefined && current.data.id === version;
  const past = useQuery({
    ...policyVersionQuery(ds, version ?? 0),
    enabled: enabled && version !== null && current.isSuccess && !isCurrent,
    retry: false,
  });
  const doc = isCurrent ? current.data?.doc : past.data?.doc;
  return doc ? routeHeadersSetting(doc, route) : null;
}

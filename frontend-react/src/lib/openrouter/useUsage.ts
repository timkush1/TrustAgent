/**
 * The tab's call counter and running cost for components (D101): `useSyncExternalStore` over the
 * usage ledger. Owned by WEB-LIVE (LV-1).
 */
import { useSyncExternalStore } from 'react';
import { browserUsage, SERVER_USAGE_SNAPSHOT, type UsageSnapshot, type UsageStore } from './usage';

const serverSnapshot = () => SERVER_USAGE_SNAPSHOT;

export function useUsage(store: UsageStore = browserUsage()): UsageSnapshot {
  return useSyncExternalStore(store.subscribe, store.getSnapshot, serverSnapshot);
}

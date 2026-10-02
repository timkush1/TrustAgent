/**
 * Whether Settings may change things here, and why not (DESIGN.md "no dead buttons"). One answer for
 * every Settings section, so a reason reads the same on Connections, Models and Access:
 *
 * 1. a read-only instance refuses every write;
 * 2. the demo stack runs mock checks with no keys of its own: writes point at `make up` instead;
 * 3. a caller without the admin scope signs in as an admin first;
 * 4. key writes also need the credentials store (P4-PLAN D32): without a database, or without a
 *    master key, they explain what to set.
 */
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import type { Reason } from '@/components/common/DisabledReason';
import { secStoreView } from '@/domain/presentation';

/** The reason a settings write is unavailable, or null. */
export function useSettingsWriteReason(): Reason | null {
  const mode = useAppMode();
  const { status } = useSystemStatus();
  if (mode.readonly) return 'readonly';
  if (mode.demo) return 'demo';
  const scopes = status?.auth.caller_scopes;
  if (scopes && !scopes.includes('admin')) return 'needs_admin';
  return null;
}

/** The reason a key cannot be saved, removed or bound here, or null (adds the store state to the above). */
export function useCredentialWriteReason(): Reason | null {
  const base = useSettingsWriteReason();
  const { status } = useSystemStatus();
  if (base) return base;
  const store = status?.credentials.store;
  if (store === 'no_database') return 'needs_database';
  if (store === 'unavailable') return { text: secStoreView('unavailable').title };
  return null;
}

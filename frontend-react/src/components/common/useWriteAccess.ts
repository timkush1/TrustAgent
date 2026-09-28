/**
 * Why a write is not available here, for <DisabledAction> (DESIGN.md "no dead buttons"). Two
 * levels, matching SPEC §2.4:
 *
 * - `admin`: PUT/POST that change things (save a policy, run the Playground) need the admin scope
 *   and a writable instance.
 * - `post`: read-scope POSTs (validate, Preview impact) change nothing, but a read-only instance
 *   refuses every non-GET `/api/*` call, so they are unavailable there too.
 *
 * The caller's scopes come from `/api/status`; when that is not served the answer is "allowed"
 * and the server's own 401/403 message is shown if the write is refused.
 */
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import type { DisabledReasonId } from './DisabledReason';

export function useWriteReason(level: 'admin' | 'post' = 'admin'): DisabledReasonId | null {
  const mode = useAppMode();
  const { status } = useSystemStatus();
  if (mode.readonly) return 'readonly';
  if (level === 'post') return null;
  const scopes = status?.auth.caller_scopes;
  if (scopes && !scopes.includes('admin')) return 'needs_admin';
  return null;
}

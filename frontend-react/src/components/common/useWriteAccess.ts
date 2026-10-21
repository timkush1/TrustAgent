/**
 * Why a write is not available here, for <DisabledAction> (DESIGN.md "no dead buttons"). Two
 * levels, matching SPEC §2.4:
 *
 * - `admin`: PUT/POST that change things (save a policy, run the Playground) need the admin scope
 *   and a writable instance.
 * - `post`: read-scope POSTs (validate, Preview impact) change nothing, but a read-only instance
 *   refuses every non-GET `/api/*` call, so they are unavailable there too.
 *
 * The static site (GitHub Pages) has no gateway at all: every write, and every page a recording
 * cannot answer, is unavailable there for that reason (`static`), not because an operator made
 * the instance read-only.
 *
 * The caller's scopes come from `/api/status`; when that is not served the answer is "allowed"
 * and the server's own 401/403 message is shown if the write is refused.
 */
import { createElement } from 'react';
import { useRuntimeConfig } from '@/app/services';
import { useRunItYourselfHref } from '@/components/replay/useReplay';
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import { copy } from '@/domain/presentation';
import type { CustomReason, DisabledReasonId } from './DisabledReason';

export function useWriteReason(level: 'admin' | 'post' = 'admin'): DisabledReasonId | null {
  const isStatic = useRuntimeConfig().appMode === 'static';
  const mode = useAppMode();
  const { status } = useSystemStatus();
  if (isStatic) return 'static';
  if (mode.readonly) return 'readonly';
  if (level === 'post') return null;
  const scopes = status?.auth.caller_scopes;
  if (scopes && !scopes.includes('admin')) return 'needs_admin';
  return null;
}

/**
 * On the static site, the `static` reason with its fix: the landing's "Run it yourself" section
 * (one command, no keys). Null on a gateway, where the action is available or has its own reason.
 */
export function useStaticReason(): CustomReason | null {
  const isStatic = useRuntimeConfig().appMode === 'static';
  const href = useRunItYourselfHref();
  if (!isStatic) return null;
  return {
    text: copy.disabled_reasons.static,
    fix: createElement('a', { href, className: 'font-medium text-fg underline underline-offset-2' }, 'Set it up in one command'),
  };
}

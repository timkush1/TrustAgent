/**
 * Words for the security server values (P4-PLAN §10 ON-1, D44, D75, D86): every value the gateway can
 * send has words, none of them is the raw value, and sentences come from contracts/ui.
 */
import { describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import type { BindingSkipReason, CredentialProvider, CredentialVerifyStatus, CredentialView, Scope } from '@/types/api';
import {
  SEC_AUDIT_ACTIONS,
  SEC_BACKEND_CARDS,
  SEC_JUDGE_PROVIDERS,
  secAuditActionLabel,
  secBackendCardOf,
  secBackendSettingLabel,
  secCredentialLabel,
  secJoinWords,
  secJudgeSettingLabel,
  secProbeReadyText,
  secProviderLabel,
  secScopeView,
  secSkipReasonText,
  secStoreView,
  secUsedByLabel,
  secVerifyView,
  secWarningText,
} from './security';

const notRaw = (word: string, raw: string) => {
  expect(word).toBeTruthy();
  expect(word).not.toBe(raw);
};

describe('security words', () => {
  it('names every provider, credential, verify status, use and scope in plain words', () => {
    for (const p of ['openrouter', 'typesafe', 'anthropic', 'openai', 'generic'] satisfies CredentialProvider[]) notRaw(secProviderLabel(p), p);
    for (const n of ['openrouter', 'compatible', 'upstream-default', 'upstream-anthropic', 'upstream-fallback']) notRaw(secCredentialLabel(n), n);
    for (const s of ['ok', 'unauthorized', 'no_credits', 'unreachable', 'unknown'] satisfies CredentialVerifyStatus[]) notRaw(secVerifyView(s).label, s);
    expect(secVerifyView(null).label).toBe('Not tested yet');
    const uses: CredentialView['used_by'] = ['decision_model', 'judge', 'upstream_default', 'upstream_anthropic', 'upstream_fallback'];
    for (const u of uses) notRaw(secUsedByLabel(u), u);
    expect(secUsedByLabel('decision_model')).toBe(copy.tiers.fast_check);
    for (const s of ['ingest', 'read', 'content', 'admin'] satisfies Scope[]) notRaw(secScopeView(s).label, s);
    expect(secScopeView('content').label.toLowerCase()).toContain('prompts and answers');
  });

  it('has words for every admin audit action of D44', () => {
    expect(SEC_AUDIT_ACTIONS).toHaveLength(22);
    for (const a of SEC_AUDIT_ACTIONS) {
      notRaw(secAuditActionLabel(a), a);
      expect(secAuditActionLabel(a)).not.toMatch(/[._]/);
    }
    expect(secAuditActionLabel('something.new_here')).toBe('Something new here');
  });

  it('explains every skipped binding, with the contract copy for an env-managed judge', () => {
    for (const r of ['env_managed', 'backend_pinned', 'verification_failed'] satisfies BindingSkipReason[]) {
      expect(secSkipReasonText(r, 'decision_model')).not.toContain(r);
    }
    expect(secSkipReasonText('env_managed', 'judge')).toBe(copy.setup.step_connect.judge_env_managed);
  });

  it('describes store states with the contract copy', () => {
    expect(secStoreView('ready').writable).toBe(true);
    expect(secStoreView('unavailable')).toMatchObject({ writable: false, body: copy.settings.store_unavailable });
    expect(secStoreView('no_database')).toMatchObject({ writable: false, body: copy.settings.no_database });
  });

  it('maps backends to the three Models cards', () => {
    expect(SEC_BACKEND_CARDS.map((c) => c.title)).toEqual(['OpenRouter', 'TypeSafe direct', 'Other gateway']);
    expect(secBackendCardOf('openrouter-systemone')?.id).toBe('openrouter');
    expect(secBackendCardOf('mock')).toBeNull();
    for (const b of ['auto', 'mock', 'off', 'compatible'] as const) notRaw(secBackendSettingLabel(b), b);
    for (const p of SEC_JUDGE_PROVIDERS) notRaw(secJudgeSettingLabel(p), p);
  });

  it('fills warnings and the probe line', () => {
    expect(secWarningText('master_key_changed', { names: ['anthropic', 'compatible'] })).toContain('Anthropic and Other gateway');
    expect(secWarningText('fail_closed_unprotected', { routes: ['payments-faq'] })).toContain('payments-faq');
    expect(secWarningText('something_else', undefined)).toBeNull();
    expect(secProbeReadyText(231, '$0.00001')).toBe('Ready · 231 ms · $0.00001');
    expect(secProbeReadyText(null, '$0.00001')).toBe('Ready · $0.00001');
    expect(secProbeReadyText(231, null)).toBe('Ready · 231 ms');
    expect(secJoinWords(['A', 'B', 'C'])).toBe('A, B and C');
  });
});

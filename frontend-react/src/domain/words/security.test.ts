/**
 * Words for the security server values (ON-1, D44, D75, D86): every value the gateway can
 * send has words, none of them is the raw value, and sentences come from contracts/ui.
 */
import { describe, expect, it } from 'vitest';
import { copy } from '@/domain/presentation';
import type { BindingSkipReason, CredentialProvider, CredentialVerifyStatus, CredentialView, Scope } from '@/types/api';
import {
  secAuditChangeSummary,
  secAuditTargetView,
  SEC_AUDIT_ACTIONS,
  SEC_BACKEND_CARDS,
  SEC_JUDGE_PROVIDERS,
  secAuditActionLabel,
  secBackendCardOf,
  secBackendSettingLabel,
  secComponentStateView,
  secCredentialLabel,
  secDecisionStateView,
  secEnvKeyOfPath,
  secFieldErrorText,
  secJoinWords,
  secJudgeSettingLabel,
  secProbeReadySegments,
  secProbeReadyText,
  secProviderLabel,
  secScopeView,
  secSkipReasonText,
  secStoreView,
  secUsedByLabel,
  secVerifyHintText,
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

  it('words every field-error code a refused write sends, with the names and host it carries', () => {
    const codes = ['unknown_setting', 'invalid_type', 'invalid_value', 'binding_mismatch', 'unknown_credential', 'credential_undecryptable', 'invalid_url', 'url_credentials', 'insecure_url', 'forbidden_target'];
    for (const code of codes) {
      const text = secFieldErrorText({ code, message: 'server words' });
      expect(text, code).not.toBe('server words');
      notRaw(text, code);
    }
    expect(secFieldErrorText({ code: 'credential_missing', message: 'm', details: { names: ['typesafe'] } })).toBe('No TypeSafe key is saved. Add it under Connections first.');
    expect(secFieldErrorText({ code: 'reenter_secret', message: 'm', details: { names: ['compatible'] } })).toBe(
      'The address changed, so paste the Other gateway key again to send it there.',
    );
    expect(secFieldErrorText({ code: 'fallback_key_required', message: 'm', details: { host: 'api.example.com' } })).toContain('api.example.com');
    // A sentence whose names or host are missing falls back to the server's own words.
    expect(secFieldErrorText({ code: 'origin_not_allowed', message: 'This key cannot go there.' })).toBe('This key cannot go there.');
    expect(secFieldErrorText({ code: 'something_new', message: 'The proxy says why.' })).toBe('The proxy says why.');
    expect(secFieldErrorText({ code: 'toString', message: 'The proxy says why.' })).toBe('The proxy says why.');
  });

  it('words every hint code a key check sends (credentials/verify.go Hint*, jev error kinds)', () => {
    const kinds = ['not_checked', 'probe_skipped', 'off', 'breaker_open', 'rate_limited', 'overload', 'budget', 'credentials_unavailable', 'timeout', 'data_policy', 'not_found', 'too_large', 'bad_request', 'retryable', 'bad_content_type', 'bad_response'];
    for (const k of kinds) {
      const text = secVerifyHintText(k);
      expect(text, k).toBe(copy.settings.verify_hint[k as keyof typeof copy.settings.verify_hint]);
      notRaw(text ?? '', k);
    }
    expect(secVerifyHintText('no_credits')).toBe(copy.setup.step_connect.no_credits);
    // The no-limit callout says it; no hint, or a code without words, leaves the status words alone.
    expect(secVerifyHintText('no_limit')).toBeNull();
    expect(secVerifyHintText(null)).toBeNull();
    expect(secVerifyHintText('mock_fixture_missing')).toBeNull();
    expect(secVerifyHintText('toString')).toBeNull();
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

  it('names the fast check and component states, and the env var behind a settings path (D33)', () => {
    const states = ['off', 'mock', 'checking', 'ready', 'degraded', 'breaker_open', 'unauthorized', 'no_credits', 'budget_exhausted', 'misconfigured', 'unreachable'] as const;
    for (const st of states) notRaw(secDecisionStateView(st).label, st);
    for (const st of ['up', 'degraded', 'down', 'disabled', 'unknown'] as const) notRaw(secComponentStateView(st).label, st);
    expect(secEnvKeyOfPath('decision_model.backend')).toBe('TRUTHTABLE_JEV_BACKEND');
    expect(secEnvKeyOfPath('judge.provider')).toBe('LLM_PROVIDER');
    expect(secEnvKeyOfPath('bindings.judge')).toBeNull();
  });

  it('fills warnings and the probe line', () => {
    expect(secWarningText('master_key_changed', { names: ['anthropic', 'compatible'] })).toContain('Anthropic and Other gateway');
    expect(secWarningText('fail_closed_unprotected', { routes: ['payments-faq'] })).toContain('payments-faq');
    expect(secWarningText('something_else', undefined)).toBeNull();
    expect(secProbeReadyText(231, '$0.00001')).toBe('Ready: the test call took 231 ms and cost $0.00001');
    expect(secProbeReadyText(1234, '$0.00001')).toBe('Ready: the test call took 1,234 ms and cost $0.00001');
    expect(secProbeReadyText(null, '$0.00001')).toBe('Ready: the test call cost $0.00001');
    expect(secProbeReadyText(231, null)).toBe('Ready: the test call took 231 ms');
    expect(secProbeReadyText(undefined, null)).toBe('Ready');
    // Split for a badge after each figure.
    expect(secProbeReadySegments(231, '$0.00001')).toEqual({ first: 'Ready: the test call took 231 ms', timed: true, second: 'and cost $0.00001' });
    expect(secProbeReadySegments(231, null)).toEqual({ first: 'Ready: the test call took 231 ms', timed: true, second: '' });
    expect(secProbeReadySegments(null, '$0.00001')).toEqual({ first: 'Ready: the test call cost $0.00001', timed: false, second: '' });
    expect(secJoinWords(['A', 'B', 'C'])).toBe('A, B and C');
  });

  it('says in words what an admin audit entry changed, never a field name (D44)', () => {
    // The diffs below are what the proxy records (a live run against mock-upstream).
    expect(secAuditChangeSummary('api_key.create', { label: 'my-app', prefix: 'ta_rK_M_F', scopes: ['ingest'] })).toBe('“my-app” · Send requests');
    expect(secAuditChangeSummary('setup.claim', { label: 'setup-admin', prefix: 'ta_a3lwhI', scopes: ['admin'] })).toBe('“setup-admin” · Admin');
    expect(secAuditChangeSummary('api_key.revoke', { label: 'ci-bot' })).toBe('“ci-bot”');
    expect(secAuditChangeSummary('session.sign_in', { scopes: ['read', 'content'] })).toBe('Read the dashboard and See prompts and answers');
    expect(secAuditChangeSummary('session.revoke_all', { session_epoch: 3 })).toBe('Every dashboard session ended');
    expect(secAuditChangeSummary('setup_token.mint', { forced: false, expires_at: '2026-10-02T17:24:59Z' })).toBe('First-run link');
    expect(secAuditChangeSummary('setup_token.mint', { forced: true, expires_at: '2026-10-02T17:24:59Z' })).toBe('Recovery link (an admin already existed)');
    expect(
      secAuditChangeSummary('credential.connect_openrouter', {
        masked: 'sk-…335b',
        verify: 'ok',
        skipped: [{ reason: 'env_managed', target: 'judge' }],
        bindings: ['decision_model'],
        provider: 'openrouter',
        fingerprint: 'fp_c6768b909533',
      }),
    ).toBe(`sk-…335b · Working · now used for ${copy.tiers.fast_check}`);
    expect(secAuditChangeSummary('credential.put', { masked: 'sk-…cdef', verify: 'no_credits', bindings: [], provider: 'openrouter' })).toBe('sk-…cdef · No credits');
    expect(secAuditChangeSummary('credential.put', { fingerprint: 'fp_5d0c41e9a7b2', bind: ['decision_model', 'judge'] })).toBe(
      `for ${copy.tiers.fast_check} and ${copy.tiers.deep_audit} judge`,
    );
    expect(secAuditChangeSummary('credential.verify', { bind: [], status: 'unauthorized' })).toBe('Key rejected');
    expect(secAuditChangeSummary('credential.delete', { provider: 'openrouter', unbound: ['decision_model'] })).toBe(`was used for ${copy.tiers.fast_check}`);
    expect(secAuditChangeSummary('credential.delete', { provider: 'anthropic', unbound: [] })).toBe('was not in use');
    expect(
      secAuditChangeSummary('settings.update', {
        keys: ['bindings', 'decision_model'],
        credential: 'openrouter',
        'decision_model.backend': { to: 'auto', from: 'mock' },
      }),
    ).toBe(`Which key runs which check and ${copy.tiers.fast_check}`);
    expect(secAuditChangeSummary('settings.update', { keys: ['decision_model'], paths: ['decision_model.backend'], fields: {} })).toBe(copy.tiers.fast_check);
    expect(secAuditChangeSummary('settings.update', { 'budgets.llm_usd_day': { from: 5, to: 3 } })).toBe('Daily budgets');
    expect(secAuditChangeSummary('settings.update', { keys: ['bindings'], unbound: ['judge'], reason: 'credential.delete' })).toBe(
      'Which key runs which check · after a key was removed',
    );
    // Actions this page has no special words for read their changed fields in words.
    expect(secAuditChangeSummary('evaluator.version', { from_version: 2, to_version: 3 })).toBe('From version, To version');
    expect(secAuditChangeSummary('setup.claim', null)).toBe('—');
    expect(secAuditChangeSummary('settings.update', {})).toBe('—');
  });

  it('names what an admin audit entry acted on: keys and settings groups in words, ids as ids', () => {
    expect(secAuditTargetView('credential.put', 'openrouter')).toEqual({ text: 'OpenRouter', mono: false });
    expect(secAuditTargetView('settings.update', 'openrouter')).toEqual({ text: 'OpenRouter', mono: false });
    expect(secAuditTargetView('settings.update', 'budgets')).toEqual({ text: 'Daily budgets', mono: false });
    expect(secAuditTargetView('api_key.create', '01a0fd70-5a2b-7141-ad05-40fb4be57a1a')).toEqual({ text: '01a0fd70-5a2b-7141-ad05-40fb4be57a1a', mono: true });
    expect(secAuditTargetView('setup_token.mint', null)).toEqual({ text: '—', mono: false });
  });
});

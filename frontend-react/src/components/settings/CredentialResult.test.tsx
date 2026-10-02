import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import bound from '@contracts/fixtures/api/CredentialWriteResult.bound.json';
import { copy } from '@/domain/presentation';
import type { CredentialWriteResult } from '@/types/api';
import { CredentialResult } from './CredentialResult';

/** The bound fixture, with its verification reshaped. */
function result(change: (r: CredentialWriteResult) => void): CredentialWriteResult {
  const r = structuredClone(bound) as CredentialWriteResult;
  change(r);
  return r;
}

const noLimit = () => screen.queryByText(copy.setup.step_connect.no_limit, { exact: false });

describe('CredentialResult: the no-limit callout (D87)', () => {
  it('shows it for an OpenRouter key with limit_usd null and the no_limit hint', () => {
    render(<CredentialResult result={result((r) => Object.assign(r.verification!, { limit_usd: null, limit_remaining_usd: null, hint: 'no_limit' }))} />);
    expect(screen.getByText(/^Ready · /)).toBeInTheDocument();
    expect(noLimit()).toBeInTheDocument();
  });

  it('shows it for an OpenRouter key with limit_usd null', () => {
    render(<CredentialResult result={result((r) => (r.verification!.limit_usd = null))} />);
    expect(noLimit()).toBeInTheDocument();
  });

  it('leaves it out when OpenRouter was not asked: the proxy leaves the limit fields out then', () => {
    render(
      <CredentialResult
        result={result((r) => {
          delete r.verification!.key_label;
          delete r.verification!.limit_usd;
          delete r.verification!.limit_remaining_usd;
        })}
      />,
    );
    expect(screen.getByText(/^Ready · /)).toBeInTheDocument();
    expect(noLimit()).toBeNull();
  });

  it('leaves it out for an OpenRouter key with a limit', () => {
    render(<CredentialResult result={result((r) => (r.verification!.limit_usd = 2))} />);
    expect(screen.getByText(/^Ready · /)).toBeInTheDocument();
    expect(noLimit()).toBeNull();
  });

  it('leaves it out for other providers, which have no OpenRouter limit to set', () => {
    render(
      <CredentialResult
        result={result((r) => {
          r.credential = { ...r.credential, name: 'typesafe', provider: 'typesafe', allowed_hosts: ['https://api.typesafe.ai'] };
          delete r.verification!.key_label;
          delete r.verification!.limit_usd;
          delete r.verification!.limit_remaining_usd;
        })}
      />,
    );
    expect(screen.getByText(/^Ready · /)).toBeInTheDocument();
    expect(noLimit()).toBeNull();
  });

  it('leaves it out when the check failed', () => {
    render(
      <CredentialResult
        result={result((r) => {
          r.verification = { ...r.verification!, ok: false, status: 'no_credits', hint: 'no_credits', limit_usd: null };
        })}
      />,
    );
    expect(screen.getByText(copy.setup.step_connect.no_credits)).toBeInTheDocument();
    expect(noLimit()).toBeNull();
  });
});

describe('CredentialResult: the hint codes of the proxy, in words (D86)', () => {
  it('does not call a key Ready when TrustAgent held the test request back (probe_skipped)', () => {
    render(
      <CredentialResult
        result={result((r) => Object.assign(r.verification!, { hint: 'probe_skipped', latency_ms: null, cost_usd: null, cost_source: null }))}
      />,
    );
    expect(screen.getByText(copy.settings.verify_hint.probe_skipped)).toBeInTheDocument();
    expect(screen.queryByText(/^Ready/)).toBeNull();
    // The key check itself answered: the limit it reported still counts.
    expect(noLimit()).toBeNull();
  });

  it('says why a failed check failed, from the hint code', () => {
    render(
      <CredentialResult
        result={result((r) => {
          r.verification = { ...r.verification!, ok: false, status: 'unknown', hint: 'data_policy', latency_ms: null, cost_usd: null, cost_source: null };
        })}
      />,
    );
    expect(screen.getByText(copy.settings.verify_hint.data_policy)).toBeInTheDocument();
    expect(screen.queryByText(/data_policy/)).toBeNull();
  });

  it('keeps the status words for a code without words of its own, never the raw code', () => {
    render(
      <CredentialResult
        result={result((r) => {
          r.verification = { ...r.verification!, ok: false, status: 'unreachable', hint: 'mock_fixture_missing', latency_ms: null, cost_usd: null, cost_source: null };
        })}
      />,
    );
    expect(screen.getByText(/^Couldn't reach the provider\. The provider did not answer/)).toBeInTheDocument();
    expect(screen.queryByText(/mock_fixture_missing/)).toBeNull();
  });
});

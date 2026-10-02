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
  it('shows it for an OpenRouter key whose limit the proxy left out (Go omitempty)', () => {
    render(
      <CredentialResult
        result={result((r) => {
          delete r.verification!.limit_usd;
          delete r.verification!.limit_remaining_usd;
        })}
      />,
    );
    expect(noLimit()).toBeInTheDocument();
  });

  it('shows it for an OpenRouter key with limit_usd null', () => {
    render(<CredentialResult result={result((r) => (r.verification!.limit_usd = null))} />);
    expect(noLimit()).toBeInTheDocument();
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

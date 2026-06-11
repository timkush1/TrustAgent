import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { KnowledgeBaseView } from './KnowledgeBaseView';
import type { ConflictPair, KBClaim, KBStats } from '../../api/kb';

function makeClaim(overrides: Partial<KBClaim> = {}): KBClaim {
  return {
    claim_id: 'c-1',
    claim: 'Paris is the capital of France.',
    source_doc_id: 'doc-1',
    source_excerpt: 'Paris is the capital and largest city of France.',
    status: 'accepted',
    entailment_score: 0.95,
    conflicts_with: [],
    ingested_at_ms: 1760000000000,
    ...overrides,
  };
}

function stubKBFetch({
  stats = { total_claims: 1, accepted: 1, quarantined: 0, conflict_pairs: 0 },
  claims = [makeClaim()],
  conflicts = [],
}: {
  stats?: KBStats;
  claims?: KBClaim[];
  conflicts?: ConflictPair[];
} = {}) {
  const fetchMock = vi.fn().mockImplementation((url: string) => {
    const body = url.includes('/api/kb/stats')
      ? stats
      : url.includes('/api/kb/conflicts')
        ? { conflicts, total: conflicts.length }
        : { claims, total: claims.length, limit: 50, offset: 0 };
    return Promise.resolve({ ok: true, status: 200, json: async () => body });
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('KnowledgeBaseView', () => {
  it('renders stats and claims', async () => {
    stubKBFetch({
      stats: { total_claims: 5, accepted: 3, quarantined: 2, conflict_pairs: 1 },
    });

    render(<KnowledgeBaseView />);

    await waitFor(() => {
      expect(screen.getByText('Paris is the capital of France.')).toBeDefined();
    });
    expect(screen.getByText('Total claims')).toBeDefined();
    expect(screen.getByText('5')).toBeDefined();
    expect(screen.getByText('95%')).toBeDefined();
  });

  it('marks quarantined claims', async () => {
    stubKBFetch({
      claims: [
        makeClaim({
          claim_id: 'q-1',
          claim: 'The moon is made of cheese.',
          status: 'quarantined',
          entailment_score: 0.1,
        }),
      ],
    });

    render(<KnowledgeBaseView />);

    await waitFor(() => {
      expect(screen.getByText('quarantined')).toBeDefined();
    });
  });

  it('shows contradiction pairs', async () => {
    stubKBFetch({
      conflicts: [
        {
          claim_a: makeClaim({ claim_id: 'a', claim: 'The deadline is March 1st.' }),
          claim_b: makeClaim({ claim_id: 'b', claim: 'The deadline is June 30th.' }),
        },
      ],
    });

    render(<KnowledgeBaseView />);

    await waitFor(() => {
      expect(screen.getByText(/Contradicting Claims/)).toBeDefined();
    });
    expect(screen.getByText('The deadline is March 1st.')).toBeDefined();
    expect(screen.getByText('The deadline is June 30th.')).toBeDefined();
  });

  it('filters claims by status', async () => {
    const fetchMock = stubKBFetch();

    render(<KnowledgeBaseView />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText(/status/i), { target: { value: 'quarantined' } });

    await waitFor(() => {
      const claimCalls = fetchMock.mock.calls
        .map((call) => call[0] as string)
        .filter((url) => url.includes('/api/kb/claims'));
      expect(claimCalls.at(-1)).toContain('status=quarantined');
    });
  });

  it('shows the empty state without claims', async () => {
    stubKBFetch({
      stats: { total_claims: 0, accepted: 0, quarantined: 0, conflict_pairs: 0 },
      claims: [],
    });

    render(<KnowledgeBaseView />);

    await waitFor(() => {
      expect(screen.getByText(/upload documents to populate/i)).toBeDefined();
    });
  });

  it('surfaces the engine-unavailable error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: false, status: 503, json: async () => ({}) })
    );

    render(<KnowledgeBaseView />);

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toMatch(/not available/i);
    });
  });
});

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { HistoryView } from './HistoryView';
import type { HistoryAudit } from '../../api/audits';

function makeAudit(overrides: Partial<HistoryAudit> = {}): HistoryAudit {
  return {
    audit_id: 'a-1',
    request_id: 'a-1',
    user_query: 'What is the capital of France?',
    llm_response: 'Paris is the capital of France.',
    faithfulness_score: 0.95,
    grade: 'A',
    hallucination_detected: false,
    processing_time_ms: 1200,
    created_at: '2026-06-10T12:00:00Z',
    claims: [
      { claim: 'Paris is the capital of France', status: 'SUPPORTED', confidence: 0.95, evidence: [] },
    ],
    ...overrides,
  };
}

function mockFetchResponse(audits: HistoryAudit[], total = audits.length) {
  return vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ audits, total, limit: 20, offset: 0 }),
  });
}

beforeEach(() => {
  vi.stubGlobal('fetch', mockFetchResponse([makeAudit()]));
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('HistoryView', () => {
  it('loads and renders audits', async () => {
    render(<HistoryView />);

    await waitFor(() => {
      expect(screen.getByText('What is the capital of France?')).toBeDefined();
    });
    expect(screen.getByText('95%')).toBeDefined();
    // 'A' also exists as a filter <option>; the badge is the second match.
    expect(screen.getAllByText('A').length).toBeGreaterThanOrEqual(2);
  });

  it('requests the audits endpoint with default pagination', async () => {
    const fetchMock = mockFetchResponse([makeAudit()]);
    vi.stubGlobal('fetch', fetchMock);

    render(<HistoryView />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());

    const url = fetchMock.mock.calls[0][0] as string;
    expect(url).toContain('/api/audits');
    expect(url).toContain('limit=20');
    expect(url).toContain('offset=0');
  });

  it('applies the flagged filter and resets to page one', async () => {
    const fetchMock = mockFetchResponse([makeAudit()]);
    vi.stubGlobal('fetch', fetchMock);

    render(<HistoryView />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());

    fireEvent.click(screen.getByLabelText(/hallucinations only/i));

    await waitFor(() => {
      const lastUrl = fetchMock.mock.calls.at(-1)?.[0] as string;
      expect(lastUrl).toContain('flagged=true');
      expect(lastUrl).toContain('offset=0');
    });
  });

  it('applies the grade filter', async () => {
    const fetchMock = mockFetchResponse([makeAudit()]);
    vi.stubGlobal('fetch', fetchMock);

    render(<HistoryView />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText(/grade/i), { target: { value: 'F' } });

    await waitFor(() => {
      const lastUrl = fetchMock.mock.calls.at(-1)?.[0] as string;
      expect(lastUrl).toContain('grade=F');
    });
  });

  it('expands a row to show claims', async () => {
    render(<HistoryView />);
    await waitFor(() => {
      expect(screen.getByText('What is the capital of France?')).toBeDefined();
    });

    fireEvent.click(screen.getByText('What is the capital of France?'));

    expect(screen.getByText('Paris is the capital of France.')).toBeDefined();
    expect(screen.getByText('Paris is the capital of France')).toBeDefined();
  });

  it('shows the flagged badge for hallucinated audits', async () => {
    vi.stubGlobal(
      'fetch',
      mockFetchResponse([
        makeAudit({ audit_id: 'bad', hallucination_detected: true, grade: 'F', faithfulness_score: 0 }),
      ])
    );

    render(<HistoryView />);

    await waitFor(() => {
      expect(screen.getByText(/HALLUCINATION/)).toBeDefined();
    });
  });

  it('shows an empty state when no audits match', async () => {
    vi.stubGlobal('fetch', mockFetchResponse([], 0));

    render(<HistoryView />);

    await waitFor(() => {
      expect(screen.getByText(/no audits match/i)).toBeDefined();
    });
  });

  it('surfaces the persistence-disabled error from a 503', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: false, status: 503, json: async () => ({}) })
    );

    render(<HistoryView />);

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toMatch(/not available/i);
    });
  });

  it('paginates with next/previous', async () => {
    const manyAudits = Array.from({ length: 20 }, (_, i) => makeAudit({ audit_id: `a-${i}` }));
    const fetchMock = mockFetchResponse(manyAudits, 45);
    vi.stubGlobal('fetch', fetchMock);

    render(<HistoryView />);
    await waitFor(() => expect(screen.getByText('Page 1 of 3')).toBeDefined());

    fireEvent.click(screen.getByText('Next →'));

    await waitFor(() => {
      const lastUrl = fetchMock.mock.calls.at(-1)?.[0] as string;
      expect(lastUrl).toContain('offset=20');
    });
  });
});

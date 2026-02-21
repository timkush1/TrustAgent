import { useState } from 'react';

interface QueryInputProps {
  apiBaseUrl?: string;
}

export function QueryInput({ apiBaseUrl = 'http://localhost:8080' }: QueryInputProps) {
  const [query, setQuery] = useState('');
  const [response, setResponse] = useState('');
  const [model, setModel] = useState('');
  const [loading, setLoading] = useState(false);
  const [feedback, setFeedback] = useState<{ type: 'success' | 'error'; message: string } | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!query.trim() || !response.trim()) return;

    setLoading(true);
    setFeedback(null);

    try {
      const res = await fetch(`${apiBaseUrl}/api/audit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: query.trim(),
          response: response.trim(),
          ...(model.trim() && { model: model.trim() }),
        }),
      });

      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Request failed' }));
        throw new Error(err.error || `HTTP ${res.status}`);
      }

      const data = await res.json();
      setFeedback({ type: 'success', message: `Audit submitted (${data.request_id})` });
      setQuery('');
      setResponse('');
      setModel('');
    } catch (err) {
      setFeedback({ type: 'error', message: err instanceof Error ? err.message : 'Unknown error' });
    } finally {
      setLoading(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="p-4 border-b border-gray-700">
      <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-3">
        Submit Audit
      </h3>

      <div className="space-y-2">
        <input
          type="text"
          placeholder="User query (e.g., 'What is the capital of France?')"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="w-full px-3 py-2 rounded-lg bg-gray-900 border border-gray-700 text-sm text-gray-200 placeholder-gray-600 focus:border-cyan-500 focus:outline-none"
          required
        />

        <textarea
          placeholder="LLM response to audit..."
          value={response}
          onChange={(e) => setResponse(e.target.value)}
          rows={3}
          className="w-full px-3 py-2 rounded-lg bg-gray-900 border border-gray-700 text-sm text-gray-200 placeholder-gray-600 focus:border-cyan-500 focus:outline-none resize-y"
          required
        />

        <input
          type="text"
          placeholder="Model (optional, e.g., 'gpt-4')"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          className="w-full px-3 py-2 rounded-lg bg-gray-900 border border-gray-700 text-sm text-gray-200 placeholder-gray-600 focus:border-cyan-500 focus:outline-none"
        />

        <button
          type="submit"
          disabled={loading || !query.trim() || !response.trim()}
          className="w-full py-2 rounded-lg text-sm font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          style={{
            backgroundColor: loading ? '#374151' : 'rgba(6, 182, 212, 0.15)',
            color: '#06b6d4',
            border: '1px solid rgba(6, 182, 212, 0.3)',
          }}
        >
          {loading ? 'Submitting...' : 'Run Audit'}
        </button>
      </div>

      {feedback && (
        <div
          className="mt-2 px-3 py-2 rounded-lg text-xs"
          style={{
            backgroundColor: feedback.type === 'success' ? 'rgba(0, 255, 136, 0.1)' : 'rgba(255, 51, 102, 0.1)',
            color: feedback.type === 'success' ? '#00ff88' : '#ff3366',
            border: `1px solid ${feedback.type === 'success' ? 'rgba(0, 255, 136, 0.2)' : 'rgba(255, 51, 102, 0.2)'}`,
          }}
        >
          {feedback.message}
        </div>
      )}
    </form>
  );
}

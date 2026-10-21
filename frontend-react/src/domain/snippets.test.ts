import { describe, expect, it } from 'vitest';
import { gatewayOrigin, shellQuote, SNIPPET_TABS, snippet } from './snippets';

describe('connect snippets', () => {
  it('has the five SDK tabs of SPEC §3.3', () => {
    expect(SNIPPET_TABS.map((t) => t.label)).toEqual(['OpenAI Python', 'OpenAI TS', 'Anthropic', 'curl', 'LangChain']);
  });

  it.each(SNIPPET_TABS.map((t) => t.id))('%s reads the key from the environment and attaches context', (id) => {
    const code = snippet(id, { origin: 'http://localhost:3000/' });
    expect(code).toContain('TRUSTAGENT_KEY');
    expect(code).toContain('X-TrustAgent-Key');
    expect(code).toContain('trustagent');
    expect(code).toContain('refund-policy.md');
    expect(code).toContain('http://localhost:3000');
    expect(code).not.toContain('localhost:3000//');
    expect(code).not.toMatch(/sk-[A-Za-z0-9]/);
  });

  it('points OpenAI clients at /v1 and Anthropic at the origin', () => {
    expect(snippet('openai-python', { origin: 'https://gw.example.com' })).toContain('base_url="https://gw.example.com/v1"');
    expect(snippet('anthropic', { origin: 'https://gw.example.com/v1' })).toContain('base_url="https://gw.example.com"');
  });

  it('adds the route header only when a route is given', () => {
    expect(snippet('curl', { origin: 'http://x', route: 'support-bot' })).toContain(`-H 'X-TrustAgent-Route: support-bot'`);
    expect(snippet('curl', { origin: 'http://x' })).not.toContain('X-TrustAgent-Route');
  });

  it('quotes shell strings safely', () => {
    expect(shellQuote(`it's`)).toBe(`'it'\\''s'`);
    expect(gatewayOrigin('http://a/v1/')).toBe('http://a');
  });
});

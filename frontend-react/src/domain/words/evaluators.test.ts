import { describe, expect, it } from 'vitest';
import { evalLabelerName, evalNameMap } from './evaluators';

describe('evalLabelerName', () => {
  it('names an environment admin key by its label, without the session fingerprint', () => {
    expect(evalLabelerName('env:admin#3fa9c1d2e4b5a6f7')).toBe('admin');
    expect(evalLabelerName('env:ops-lead#00ff00ff00ff00ff')).toBe('ops-lead');
  });

  it('never shows the id of a dashboard-minted key or the open-instance placeholder', () => {
    expect(evalLabelerName('key:k_7Hq2x9')).toBe('an API key');
    expect(evalLabelerName('public')).toBe('someone without a key');
  });

  it('keeps a key label sent with a key header as it is', () => {
    expect(evalLabelerName('ci-labeller')).toBe('ci-labeller');
  });
});

describe('evalNameMap', () => {
  it('keeps unique names, and puts the id beside a name two checks share', () => {
    const names = evalNameMap([
      { id: 'groundedness', name: 'Groundedness' },
      { id: 'competitors', name: 'Competitor mentions' },
      { id: 'proof_muree135', name: 'Competitor mentions' },
      { id: 'groundedness', name: 'Groundedness' },
    ]);
    expect([...names]).toEqual([
      ['groundedness', 'Groundedness'],
      ['competitors', 'Competitor mentions (competitors)'],
      ['proof_muree135', 'Competitor mentions (proof_muree135)'],
    ]);
  });
});

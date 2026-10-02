import { describe, expect, it } from 'vitest';
import { evalLabelerName } from './evaluators';

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

// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { DEFAULT_MIN_INTEGER, detect, detectOne, DETECTOR_IDS, detectorStates, maskSecret, type DetectorInput } from './detectors';

const EMOJI = String.fromCodePoint(0x1f600);

const base: DetectorInput = {
  response: '',
  evidence: [{ id: 'doc', source: 'request_system', text: 'Refunds within 30 days. Support: https://Acme.example/help. Updated 2026-03-02.' }],
  userRequest: '',
  systemInstructions: '',
};
const run = (id: string, over: Partial<DetectorInput>) => detectOne(id, { ...base, ...over });

describe('detectors', () => {
  it('runs every detector in the decision/v1 order', () => {
    const results = detect({ ...base, response: 'Refunds within 45 days.' });
    expect(results.map((r) => r.id)).toEqual(DETECTOR_IDS);
    expect(detectorStates(results).numbers_not_in_evidence).toEqual({ ran: true, fired: true });
    expect(detectorStates(results).system_prompt_leak).toEqual({ ran: false, fired: false });
    expect(DEFAULT_MIN_INTEGER).toBe(10);
  });

  it('reports UTF-16 offsets after astral characters', () => {
    const r = run('numbers_not_in_evidence', { response: `${EMOJI} ${EMOJI} costs $45` });
    expect(r.findings).toEqual([{ text: '45', start: 13, end: 15 }]);
  });

  it('checks small integers only next to units or currency', () => {
    const r = run('numbers_not_in_evidence', { response: 'Step 3 of 4: wait 5 days, pay $2, or 7%.' });
    // The raw token keeps a trailing comma ("2,"); it normalizes to 2.
    expect(r.findings.map((f) => f.text)).toEqual(['5', '2,', '7%']);
    expect(run('numbers_not_in_evidence', { response: 'Wait 5 days.', minInteger: 2 }).findings.map((f) => f.text)).toEqual(['5']);
    expect(run('numbers_not_in_evidence', { response: 'Pick 5.', minInteger: -1 }).findings.map((f) => f.text)).toEqual(['5']);
  });

  it('skips list markers and ignores untrusted evidence as a reference', () => {
    expect(run('numbers_not_in_evidence', { response: '1. Refunds within 30 days.\n2) Fine.' }).fired).toBe(false);
    const untrusted = run('numbers_not_in_evidence', {
      response: 'It takes 30 days.',
      evidence: [{ id: 'web', source: 'tool_results', text: 'It takes 30 days.', untrusted: true }],
    });
    expect(untrusted).toMatchObject({ ran: false, fired: false });
  });

  it('normalizes URLs and matches dates by prefix', () => {
    expect(run('urls_not_in_evidence', { response: 'See HTTPS://acme.EXAMPLE/help, or https://evil.example/x.' }).findings).toEqual([
      { text: 'https://evil.example/x', start: 34, end: 56 },
    ]);
    expect(run('dates_not_in_evidence', { response: 'Updated March 2026 and on 3 April 2026.' }).findings.map((f) => f.text)).toEqual(['3 April 2026']);
  });

  it('masks secrets and ignores keys the user pasted', () => {
    const key = `sk-ant-${'a'.repeat(24)}`;
    const leaked = run('secret_pattern', { response: `Use ${key} now.` });
    expect(leaked.findings).toEqual([{ text: 'sk-a…aaaa', start: 4, end: 4 + key.length }]);
    expect(run('secret_pattern', { response: `Use ${key}.`, userRequest: `my key is ${key}` }).fired).toBe(false);
    expect(maskSecret('short-token')).toBe('***********');
  });

  it('finds a leaked system prompt window', () => {
    const prompt = Array.from({ length: 60 }, (_, i) => `rule${i}`).join(' ');
    const r = run('system_prompt_leak', { systemInstructions: prompt, response: `Sure! ${prompt.slice(100, 400).toUpperCase()}` });
    expect(r).toMatchObject({ ran: true, fired: true });
    expect(r.findings[0]).toMatchObject({ start: -1, end: -1 });
    expect(r.findings[0].text).toHaveLength(200);
    expect(run('system_prompt_leak', { systemInstructions: 'short', response: 'short' }).ran).toBe(false);
  });

  it('rejects an unknown detector', () => {
    expect(() => run('vibes_check', {})).toThrow(/unknown detector/);
  });
});

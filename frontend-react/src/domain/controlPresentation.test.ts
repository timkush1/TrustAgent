import { describe, expect, it } from 'vitest';
import {
  actingAsText,
  backendLabel,
  calibrationStateView,
  downgradeText,
  evidenceGapView,
  modeView,
  policySourceLabel,
  READINESS_ITEMS,
  readinessWords,
  stageLabel,
  valueLabelView,
} from './controlPresentation';
import { AVOID_IN_PRIMARY_UI } from './presentation';

describe('control presentation', () => {
  it('writes "Block → currently acting as Flag" from the contract copy', () => {
    expect(actingAsText('block', 'flag')).toBe('Block → currently acting as Flag');
    expect(actingAsText('flag', 'flag')).toBeNull();
  });

  it('gives every mode a label, an icon and a glossary definition', () => {
    for (const m of ['observe', 'flag', 'block', 'fallback'] as const) {
      const v = modeView(m);
      expect(v.label).toMatch(/^[A-Z]/);
      expect(v.description).not.toBe('');
      expect(v.icon).toBeTruthy();
    }
  });

  it('prefers the contract copy for downgrades and falls back to the server message', () => {
    expect(downgradeText({ code: 'stream_not_buffered', message: 'x' })).toMatch(/^Streams pass straight through/);
    expect(downgradeText({ code: 'nope' as never, message: 'Server words.' })).toBe('Server words.');
  });

  it('names every block-readiness requirement', () => {
    for (const item of READINESS_ITEMS) {
      expect(readinessWords(item).label).not.toBe('');
      expect(readinessWords(item).missing).not.toBe('');
      // The next step is an instruction that keeps the mode names' capitals.
      expect(readinessWords(item).action).toMatch(/^[a-z]/);
    }
  });

  it('marks only a measured calibration as good enough to block', () => {
    expect(calibrationStateView('measured').measured).toBe(true);
    for (const s of ['provisional', 'proxy', 'mock', 'family', 'none'] as const) expect(calibrationStateView(s).measured).toBe(false);
  });

  it('uses the value-label copy', () => {
    expect(valueLabelView('third_party').label).toBe('Third-party');
    expect(valueLabelView('simulated').label).toBe('Simulated');
  });

  it('speaks plainly (no internal jargon in primary UI)', () => {
    const words = [
      ...(['observe', 'flag', 'block', 'fallback'] as const).map((m) => modeView(m).label),
      ...READINESS_ITEMS.flatMap((i) => [readinessWords(i).label, readinessWords(i).missing]),
      evidenceGapView('no_evidence').label,
      evidenceGapView('evidence_packed_away').hint,
      policySourceLabel('adopt_file'),
      stageLabel('shadow'),
      backendLabel('openrouter-systemone'),
    ].join(' ');
    for (const jargon of AVOID_IN_PRIMARY_UI) expect(words).not.toContain(jargon);
  });
});

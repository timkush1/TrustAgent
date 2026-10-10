// @vitest-environment node
/**
 * The static Studio's export (D107): the version a save would create, validated as evaluator.v1, in
 * the JSON tab's layout, and never with an OpenRouter key in it.
 */
import { describe, expect, it } from 'vitest';
import { BUILTIN_TEMPLATES } from '@/lib/decision';
import { exportSpec } from './exportSpec';
import { applySimpleForm, readSimpleForm } from './simpleForm';
import { specJson } from './specFields';
import { blankSpec } from './templates';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';

describe('exportSpec', () => {
  it('exports a new check as version 1, and an edit of a library id as its next version', () => {
    const fresh = exportSpec(blankSpec([]), 0);
    expect(fresh).toMatchObject({ ok: true, fileName: 'custom_check.v1.json' });
    if (!fresh.ok) return;
    expect(fresh.json).toBe(specJson(fresh.doc));
    expect(JSON.parse(fresh.json)).toEqual({ ...blankSpec([]), version: 1 });

    const edit = { ...structuredClone(BUILTIN_TEMPLATES['refusal@1']), name: 'Refusal, stricter' };
    delete edit.builtin;
    expect(exportSpec(edit, 1)).toMatchObject({ ok: true, fileName: 'refusal.v2.json', doc: { version: 2 } });
  });

  it('round-trips through the Simple form', () => {
    const out = exportSpec(blankSpec([]), 0);
    if (!out.ok) throw new Error('expected an export');
    const doc = JSON.parse(out.json);
    const read = readSimpleForm(doc);
    expect(read.ok).toBe(true);
    if (read.ok) expect(applySimpleForm(doc, read.form)).toEqual(doc);
  });

  it('refuses a check that breaks the format or a hard rule', () => {
    const spec = blankSpec([]);
    expect(exportSpec({ ...spec, category: 'misc' as never }, 0)).toEqual({ ok: false, reason: 'invalid' });
    expect(exportSpec({ ...spec, escalation: { ...spec.escalation, escalate_from: 0.9 } }, 0)).toEqual({ ok: false, reason: 'invalid' });
    const corroborated = { ...spec, outcomes: { ...spec.outcomes, requires_corroboration: ['nope'] } };
    expect(exportSpec(corroborated, 0, { registry: ['refusal'] })).toEqual({ ok: false, reason: 'invalid' });
    expect(exportSpec(corroborated, 0, { registry: ['nope'] }).ok).toBe(true);
  });

  it('refuses any text with the tab key or an OpenRouter key shape', () => {
    const spec = blankSpec([]);
    expect(exportSpec({ ...spec, description: `Flags ${KEY}` }, 0)).toEqual({ ok: false, reason: 'secret' });
    const odd = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';
    expect(exportSpec({ ...spec, description: `Flags ${odd}` }, 0, { secrets: [odd] })).toEqual({ ok: false, reason: 'secret' });
    expect(exportSpec(spec, 0, { secrets: [KEY, null, 'short'] }).ok).toBe(true);
  });
});

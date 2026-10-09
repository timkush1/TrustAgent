import { describe, expect, it } from 'vitest';
import { fieldWarningView, isDivergedWarning, policyWarningView } from './policyWarnings';

const ctx = {
  routes: ['support-bot', 'billing-bot'],
  names: new Map([
    ['commitments', 'Unapproved commitments'],
    ['contradiction', 'Contradiction'],
    ['groundedness', 'Groundedness'],
  ]),
};

describe('policy warnings in plain words', () => {
  it('names the route and the checks of a corroboration warning, and keeps the server text (D98)', () => {
    const raw =
      'routes[1].evaluators[0].ref (line 52, col 11): commitments@1 blocks only when one of contradiction, det:numbers_not_in_evidence, det:dates_not_in_evidence agrees, and none of them can run on this route';
    const view = policyWarningView(raw, ctx);
    expect(view.route).toBe('billing-bot');
    // Any one of the listed signals is enough, and the instant checks need documents.
    expect(view.text).toBe(
      'billing-bot: Unapproved commitments blocks only when another signal agrees (Contradiction, Number check or Date check), and none of them can run on this route, so it can flag answers but not block them. Give the route documents to check against so the instant checks can run.',
    );
    expect(view.detail).toBe(raw);
    expect(view.text).not.toMatch(/routes\[|@1|det:|corroborat/);
    // Only checks listed: attaching one is the fix.
    const checksOnly = policyWarningView('routes[0].evaluators[0].ref: groundedness@1 blocks only when one of contradiction agrees, and none of them can run on this route', ctx);
    expect(checksOnly.text).toBe(
      'support-bot: Groundedness blocks only when another signal agrees (Contradiction), and none of them can run on this route, so it can flag answers but not block them. Attach Contradiction to the route so it can confirm a block.',
    );
  });

  it('words an older gateway’s per-corroborator warning without claiming the check cannot block', () => {
    const view = policyWarningView('routes[1].evaluators[0].ref: commitments@1 is corroborated by contradiction, which this route does not attach', ctx);
    expect(view.text).toBe('billing-bot: Contradiction is one of the signals that can confirm Unapproved commitments before it blocks, and this route does not run it.');
    expect(view.text).not.toMatch(/Add Contradiction|can block\./);
  });

  it('rephrases every warning the gateway writes today', () => {
    expect(policyWarningView('routes[0].evaluators[1].ref: commitments@1 implies contradiction@1, which this route does not attach, so their disagreement is never checked', ctx).text).toBe(
      'support-bot: Unapproved commitments is cross-checked against Contradiction to catch the two disagreeing, but this route does not run Contradiction, so that cross-check never happens.',
    );
    expect(policyWarningView('routes[0].fail: fail: closed only matters in block and fallback modes (this route is flag)', ctx).text).toBe(
      'support-bot: Blocking an answer when a check cannot finish (fail closed) only matters on Block and Fallback routes; this route is set to Flag.',
    );
    expect(policyWarningView('defaults.sample_rate: sample_rate applies to observe routes only; this block route checks every request', ctx).text).toBe(
      'Default route: The sample rate only applies to Observe routes; this Block route checks every request.',
    );
    expect(policyWarningView('routes[1].evidence.sources: the block route trusts user-message context: an end user could paste text (drop request_context)', ctx).text).toBe(
      'billing-bot: This route treats text pasted by users as documents; a user could make a false answer look supported.',
    );
    const clientRules = policyWarningView('overrides.client_rules (line 4, col 3): client_rules lets end users rewrite rules on the enforcing route "billing-bot"', ctx);
    expect(clientRules.route).toBeNull();
    expect(clientRules.text).toBe(
      'Client rules are on, so each request can bring its own check rules, and billing-bot blocks answers: a caller could loosen the rules that decide what it blocks. Turn client rules off, or keep this route from blocking.',
    );
    expect(clientRules.text).not.toMatch(/client_rules|enforcing/);
  });

  it('keeps the server sentence for a warning it does not know, without the path', () => {
    const view = policyWarningView('routes[0].deadline_ms: deadline is close to the upstream timeout', ctx);
    expect(view.text).toBe('support-bot: Deadline is close to the upstream timeout');
    expect(policyWarningView('something new happened', ctx).text).toBe('Something new happened');
  });

  it('reads validate field warnings the same way', () => {
    const message = 'commitments@1 blocks only when one of contradiction, det:numbers_not_in_evidence agrees, and none of them can run on this route';
    const view = fieldWarningView({ path: 'routes[1].evaluators[0].ref', message }, ctx);
    expect(view.route).toBe('billing-bot');
    expect(view.text).toMatch(/^billing-bot: Unapproved commitments blocks only when another signal agrees \(Contradiction or Number check\)/);
    expect(view.detail).toBe(`routes[1].evaluators[0].ref: ${message}`);
  });

  it('recognises the diverged-file warning the banner already shows', () => {
    expect(isDivergedWarning('/etc/trustagent/policies.yaml changed after the newest dashboard edit and is not applied: adopt the file or keep the dashboard version')).toBe(true);
    expect(isDivergedWarning('anything else')).toBe(false);
  });
});

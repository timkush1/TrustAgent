/**
 * The built-in "Unapproved commitments" check as Write your own checks shows it: what a
 * plain-English rule becomes, attached the way contracts/policies/example.policies.yaml attaches it.
 */
import commitments from '@contracts/evaluators/commitments.v1.json';
import { examplePolicy } from 'virtual:trustagent/landing-data';

/** The built-in "Unapproved commitments" check: what a plain-English rule becomes. */
export const COMMITMENTS = {
  ref: `${commitments.id}@${commitments.version}`,
  /** The allowed_commitments the example policy gives support-bot. */
  exampleAllowed: /allowed_commitments:\s*"([^"]+)"/.exec(examplePolicy)?.[1] ?? commitments.params.allowed_commitments.default,
  name: commitments.name,
  instructions: commitments.question.instructions,
  options: Object.entries(commitments.question.criteria).map(([key, c]) => ({ key, what: c.what, risky: key in commitments.signal.risk_weights })),
};

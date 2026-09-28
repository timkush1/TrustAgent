/**
 * The Playground form and the request it sends (SPEC §2.4 #61 PlaygroundRequest): building the
 * request from what the viewer typed, checking it before it is sent, and the examples from the demo
 * scenarios (contracts/demo/scenarios.v1.json), so "Load an example" shows the same stories the
 * demo stack plays.
 */
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import type { ChatMessage, PlaygroundRequest, PolicyMode } from '@/types/api';

export type PlaygroundMode = PlaygroundRequest['mode'];

export interface EvidenceDoc {
  id: string;
  text: string;
}

export interface PlaygroundForm {
  mode: PlaygroundMode;
  /** "" lets the route rules decide. */
  route: string;
  model: string;
  messages: ChatMessage[];
  evidence: EvidenceDoc[];
  /** Audit mode: the answer to check. */
  response: string;
  stream: boolean;
  /** "" keeps the route's own mode. */
  modeOverride: PolicyMode | '';
  /**
   * The demo scenario an unedited example came from ("" otherwise). Sent only to a demo or
   * test-mode stack, whose mock checks answer it with the scenario's scripted answers.
   */
  scenario: string;
}

export const INITIAL_FORM: PlaygroundForm = {
  mode: 'chat',
  route: '',
  model: '',
  messages: [
    { role: 'system', content: '' },
    { role: 'user', content: '' },
  ],
  evidence: [],
  response: '',
  stream: false,
  modeOverride: '',
  scenario: '',
};

export interface BuildOptions {
  /** Send the example's scenario id (a demo or test-mode stack, where mock checks are keyed by it). */
  scenario?: boolean;
}

export function buildRequest(form: PlaygroundForm, options: BuildOptions = {}): PlaygroundRequest {
  const messages = form.messages
    .filter((m) => m.content.trim() !== '')
    .map((m) => ({ role: m.role, content: m.content }));
  const evidence = form.evidence
    .filter((e) => e.text.trim() !== '')
    .map((e) => (e.id.trim() ? { id: e.id.trim(), text: e.text } : { text: e.text }));
  const request: PlaygroundRequest = { mode: form.mode, messages };
  if (form.route) request.route = form.route;
  if (form.model.trim()) request.model = form.model.trim();
  if (evidence.length > 0) request.evidence = evidence;
  if (form.mode === 'audit') request.response = form.response;
  if (form.mode === 'chat' && form.stream) request.stream = true;
  if (form.modeOverride) request.mode_override = form.modeOverride;
  if (options.scenario && form.scenario) request.scenario = form.scenario;
  return request;
}

/** Characters the checks will read (messages, documents and the answer), for the size limit. */
export function textLength(form: PlaygroundForm): number {
  return (
    form.messages.reduce((n, m) => n + m.content.length, 0) +
    form.evidence.reduce((n, e) => n + e.text.length, 0) +
    (form.mode === 'audit' ? form.response.length : 0)
  );
}

/** Why the form cannot be sent yet, in words, or null. */
export function formProblem(form: PlaygroundForm, maxChars: number | null): string | null {
  if (!form.messages.some((m) => m.role === 'user' && m.content.trim() !== '')) return 'Write a user message first.';
  if (form.mode === 'audit' && form.response.trim() === '') return 'Paste the answer to check first.';
  if (maxChars !== null && textLength(form) > maxChars) return `The text is longer than this proxy checks (${maxChars.toLocaleString('en-US')} characters).`;
  return null;
}

interface ScenarioJson {
  id: string;
  title: string;
  route: string;
  request: { model: string; messages: ChatMessage[] };
  response: string;
}

export interface PlaygroundExample {
  id: string;
  title: string;
  form: PlaygroundForm;
}

/**
 * The demo scenarios as audit-mode examples: no upstream model is needed to try them. Each keeps
 * its scenario id until the viewer edits what the scenario scripts (the conversation, documents,
 * answer, mode or route), so a demo stack plays it exactly as trafficgen does.
 */
export const EXAMPLES: readonly PlaygroundExample[] = (scenarioPack as unknown as { scenarios: ScenarioJson[] }).scenarios.map((s) => ({
  id: s.id,
  title: s.title,
  form: {
    ...INITIAL_FORM,
    mode: 'audit',
    route: s.route,
    model: s.request.model,
    messages: s.request.messages.map((m) => ({ role: m.role, content: m.content })),
    response: s.response,
    scenario: s.id,
  },
}));

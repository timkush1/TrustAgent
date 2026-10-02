/**
 * The Playground form and the request it sends (SPEC §2.4 #61 PlaygroundRequest): building the
 * request from what the viewer typed, checking it before it is sent, and the examples from the demo
 * scenarios (contracts/demo/scenarios.v1.json), so "Load an example" shows the same stories the
 * demo stack plays.
 */
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import type { ChatMessage, GateAction, PlaygroundRequest, PlaygroundResult, PolicyMode } from '@/types/api';

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
  api_format: string;
  stream: boolean;
  sets: string[];
  request: { model: string; messages?: { role: string; content: unknown }[] } & Record<string, unknown>;
  response: string;
}

/** The roles the form's message picker offers. */
const FORM_ROLES: ReadonlySet<string> = new Set<ChatMessage['role']>(['system', 'user', 'assistant']);

/** Scenario phase tags after P4, the phase of the gateway this dashboard ships with: not served yet. */
const LATER_PHASES: ReadonlySet<string> = new Set(['p5']);

export interface ExampleOptions {
  /** The knowledge add-on is on (`status.knowledge.enabled`): its scenarios have documents to find. */
  knowledge?: boolean;
}

/**
 * Whether the form holds a scenario's request exactly (contracts/README.md: a consumer picks the
 * scenarios it can represent by `api_format`, `stream` and the phase and `knowledge` tags): a
 * non-streaming OpenAI chat body of only `model` and `messages`, each message plain text with a role
 * the form offers, on a route the running gateway already serves. That leaves out Anthropic system
 * blocks, a completions `prompt`, tool calls (`content: null`), `developer` messages, the
 * `trustagent` extension and any scenario tagged with a phase after P4. The `knowledge` scenarios
 * are examples only while the knowledge add-on is on, since their documents come from it.
 */
function formHolds(s: ScenarioJson, options: ExampleOptions): boolean {
  const { messages, ...rest } = s.request;
  return (
    s.api_format === 'openai_chat' &&
    !s.stream &&
    !s.sets.some((tag) => LATER_PHASES.has(tag)) &&
    (options.knowledge === true || !s.sets.includes('knowledge')) &&
    Object.keys(rest).every((k) => k === 'model') &&
    Array.isArray(messages) &&
    messages.every((m) => FORM_ROLES.has(m.role) && typeof m.content === 'string')
  );
}

export interface PlaygroundExample {
  id: string;
  title: string;
  form: PlaygroundForm;
}

const SCENARIOS = (scenarioPack as unknown as { scenarios: ScenarioJson[] }).scenarios;

function toExample(s: ScenarioJson): PlaygroundExample {
  return {
    id: s.id,
    title: s.title,
    form: {
      ...INITIAL_FORM,
      mode: 'audit',
      route: s.route,
      model: s.request.model,
      messages: (s.request.messages ?? []).map((m) => ({ role: m.role as ChatMessage['role'], content: m.content as string })),
      response: s.response,
      scenario: s.id,
    },
  };
}

const WITHOUT_KNOWLEDGE: readonly PlaygroundExample[] = SCENARIOS.filter((s) => formHolds(s, {})).map(toExample);
const WITH_KNOWLEDGE: readonly PlaygroundExample[] = SCENARIOS.filter((s) => formHolds(s, { knowledge: true })).map(toExample);

/**
 * The demo scenarios the form holds, as audit-mode examples: no upstream model is needed to try them. Each keeps
 * its scenario id until the viewer edits what the scenario scripts (the conversation, documents,
 * answer, mode or route), so a demo stack plays it exactly as trafficgen does.
 */
export function playgroundExamples(options: ExampleOptions = {}): readonly PlaygroundExample[] {
  return options.knowledge ? WITH_KNOWLEDGE : WITHOUT_KNOWLEDGE;
}

/** The examples of a stack without the knowledge add-on. */
export const EXAMPLES: readonly PlaygroundExample[] = WITHOUT_KNOWLEDGE;

/** A response header by name, case-insensitively. */
export function headerValue(headers: Record<string, string>, name: string): string | null {
  const key = Object.keys(headers).find((k) => k.toLowerCase() === name.toLowerCase());
  return key === undefined ? null : headers[key];
}

/**
 * What the app received: the model's answer, or in its place the route's block message or a
 * fallback answer (the delivered action is the X-TrustAgent-Action header the run returned).
 */
export function receivedCaption(result: PlaygroundResult): string | null {
  if (result.delivered_text === result.response_text) return null;
  const action = headerValue(result.headers, 'X-TrustAgent-Action') ?? result.decision.action;
  return action === 'block' ? 'Your app received the block message instead' : 'Your app received a fallback answer instead';
}

const ACTIONS: readonly GateAction[] = ['observe', 'pass', 'flag', 'block', 'fallback'];

/** The action the app received (X-TrustAgent-Action), which a failed fallback can turn into a block; null when absent. */
export function deliveredAction(result: PlaygroundResult | null): GateAction | null {
  const value = result ? headerValue(result.headers, 'X-TrustAgent-Action') : null;
  return ACTIONS.find((a) => a === value) ?? null;
}

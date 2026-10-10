/**
 * The Playground request as a command for the viewer's own terminal (SPEC §3.3 "a curl equivalent
 * with the key masked"), one that reproduces the chosen mode:
 *
 * - Chat mode is what an app does: POST /v1/chat/completions through the gateway, with the
 *   documents as the `trustagent.context` extension, the route and the stricter mode as headers,
 *   and an app key. The answer is checked exactly as the Playground's is.
 * - Audit mode checks an answer without calling a model, which no /v1 call does: it is the
 *   Playground's own endpoint, POST /api/playground/run (SPEC §2.4 #61), with the same body the
 *   page sends and an admin key. A deep audit is submitted as from the page.
 *
 * Keys are environment variables, so nothing secret is ever shown.
 */
import { gatewayOrigin, INGEST_KEY_ENV, shellQuote } from '@/domain/snippets';
import type { ChatMessage, PlaygroundRequest } from '@/types/api';

/** The environment variable the audit-mode command reads the admin key from. */
export const ADMIN_KEY_ENV = 'TRUSTAGENT_ADMIN_KEY';

export interface PlaygroundCommand {
  /** The command, ready to paste. */
  code: string;
  /** The endpoint it calls ("POST /v1/chat/completions"). */
  endpoint: string;
  /** Which key it needs, and where to get one. */
  keyNote: string;
}

function documents(request: PlaygroundRequest): { id?: string; text: string }[] {
  return (request.evidence ?? []).filter((e) => e.text.trim() !== '').map((e) => ({ ...(e.id ? { id: e.id } : {}), text: e.text }));
}

function chatCommand(request: PlaygroundRequest, base: string): PlaygroundCommand {
  const context = documents(request);
  const messages: ChatMessage[] = request.messages.filter((m) => m.content.trim() !== '');
  const body = {
    ...(request.model ? { model: request.model } : {}),
    messages,
    ...(request.stream ? { stream: true } : {}),
    ...(context.length > 0 ? { trustagent: { context } } : {}),
  };
  const code = [
    `curl -i${request.stream ? ' -N' : ''} ${base}/v1/chat/completions \\`,
    '  -H "Authorization: Bearer $OPENAI_API_KEY" \\',
    `  -H "X-TrustAgent-Key: $${INGEST_KEY_ENV}" \\`,
    ...(request.route ? [`  -H ${shellQuote(`X-TrustAgent-Route: ${request.route}`)} \\`] : []),
    ...(request.mode_override ? [`  -H ${shellQuote(`X-TrustAgent-Mode: ${request.mode_override}`)} \\`] : []),
    '  -H "Content-Type: application/json" \\',
    `  -d ${shellQuote(JSON.stringify(body, null, 2))}`,
  ].join('\n');
  return {
    code,
    endpoint: 'POST /v1/chat/completions',
    keyNote: `Your keys are not shown: set ${INGEST_KEY_ENV} to an app key from Settings → Access, and OPENAI_API_KEY to your model provider’s key unless the gateway adds it.`,
  };
}

function auditCommand(request: PlaygroundRequest, base: string): PlaygroundCommand {
  const context = documents(request);
  const body: PlaygroundRequest = {
    mode: 'audit',
    ...(request.route ? { route: request.route } : {}),
    ...(request.model ? { model: request.model } : {}),
    messages: request.messages.filter((m) => m.content.trim() !== ''),
    ...(context.length > 0 ? { evidence: context } : {}),
    response: request.response ?? '',
    ...(request.mode_override ? { mode_override: request.mode_override } : {}),
    ...(request.scenario ? { scenario: request.scenario } : {}),
  };
  const code = [
    `curl -i ${base}/api/playground/run \\`,
    `  -H "X-TrustAgent-Key: $${ADMIN_KEY_ENV}" \\`,
    '  -H "Content-Type: application/json" \\',
    `  -d ${shellQuote(JSON.stringify(body, null, 2))}`,
  ].join('\n');
  return {
    code,
    endpoint: 'POST /api/playground/run',
    keyNote: `Your key is not shown: set ${ADMIN_KEY_ENV} to an admin key from Settings → Access. Checking an answer without a model call is the Playground’s own endpoint; your app’s answers are checked as they pass through /v1.`,
  };
}

export function playgroundCommand(request: PlaygroundRequest, origin: string): PlaygroundCommand {
  const base = gatewayOrigin(origin);
  return request.mode === 'audit' ? auditCommand(request, base) : chatCommand(request, base);
}

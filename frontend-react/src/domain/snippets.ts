/**
 * Copy-paste snippets for "Connect your app" (SPEC §3.3 Overview), and the quoting helpers the
 * Playground's curl equivalent (components/playground/playgroundCurl.ts) shares: point an SDK at the gateway, send the app key in `X-TrustAgent-Key`, and attach
 * the documents the answer should be checked against with the `trustagent.context` extension
 * (SPEC §2.12; the gateway strips it before the request goes upstream).
 *
 * Keys are never written into a snippet: they read environment variables (`$TRUSTAGENT_KEY`,
 * `$OPENAI_API_KEY`), so what the page shows and what the viewer copies are both safe to share.
 */

export type SnippetId = 'openai-python' | 'openai-ts' | 'anthropic' | 'curl' | 'langchain';

export interface SnippetTab {
  id: SnippetId;
  label: string;
  /** Language, for the code block caption. */
  language: string;
}

export const SNIPPET_TABS: readonly SnippetTab[] = [
  { id: 'openai-python', label: 'OpenAI Python', language: 'Python' },
  { id: 'openai-ts', label: 'OpenAI TS', language: 'TypeScript' },
  { id: 'anthropic', label: 'Anthropic', language: 'Python' },
  { id: 'curl', label: 'curl', language: 'Shell' },
  { id: 'langchain', label: 'LangChain', language: 'Python' },
];

/** The environment variable every snippet reads the app key from. */
export const INGEST_KEY_ENV = 'TRUSTAGENT_KEY';

export interface SnippetInput {
  /** Gateway origin, e.g. "http://localhost:3000" (no trailing slash, no /v1). */
  origin: string;
  /** X-TrustAgent-Route value, when the app should pick a route explicitly. */
  route?: string | null;
  model?: string;
  question?: string;
  document?: { id: string; text: string };
}

const DEFAULTS = {
  model: 'gpt-4o-mini',
  question: 'Can I still get a refund on my annual plan?',
  document: { id: 'refund-policy.md', text: 'Annual plans can be refunded within 30 days of purchase.' },
};

/** "http://localhost:3000/" or "http://localhost:3000/v1" → "http://localhost:3000". */
export function gatewayOrigin(url: string): string {
  return url.replace(/\/+$/, '').replace(/\/v1$/, '');
}

function py(value: string): string {
  return JSON.stringify(value);
}

function sq(value: string): string {
  // Single-quoted JS string.
  return `'${value.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
}

/** Shell single-quoting: close, escape, reopen. */
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

export function snippet(id: SnippetId, input: SnippetInput): string {
  const origin = gatewayOrigin(input.origin);
  const model = input.model || DEFAULTS.model;
  const question = input.question || DEFAULTS.question;
  const doc = input.document ?? DEFAULTS.document;
  const route = input.route || null;

  switch (id) {
    case 'openai-python': {
      const headers = [`"X-TrustAgent-Key": os.environ["${INGEST_KEY_ENV}"]`, ...(route ? [`"X-TrustAgent-Route": ${py(route)}`] : [])];
      return [
        'import os',
        'from openai import OpenAI',
        '',
        'client = OpenAI(',
        `    base_url="${origin}/v1",`,
        '    api_key=os.environ["OPENAI_API_KEY"],  # your provider key; the gateway forwards it',
        `    default_headers={${headers.join(', ')}},`,
        ')',
        '',
        'response = client.chat.completions.create(',
        `    model=${py(model)},`,
        `    messages=[{"role": "user", "content": ${py(question)}}],`,
        '    # Documents to check the answer against (removed before the request goes upstream).',
        `    extra_body={"trustagent": {"context": [{"id": ${py(doc.id)}, "text": ${py(doc.text)}}]}},`,
        ')',
        'print(response.choices[0].message.content)',
      ].join('\n');
    }
    case 'openai-ts': {
      const headers = [`'X-TrustAgent-Key': process.env.${INGEST_KEY_ENV}`, ...(route ? [`'X-TrustAgent-Route': ${sq(route)}`] : [])];
      return [
        "import OpenAI from 'openai';",
        '',
        'const client = new OpenAI({',
        `  baseURL: '${origin}/v1',`,
        '  apiKey: process.env.OPENAI_API_KEY, // your provider key; the gateway forwards it',
        `  defaultHeaders: { ${headers.join(', ')} },`,
        '});',
        '',
        'const response = await client.chat.completions.create({',
        `  model: ${sq(model)},`,
        `  messages: [{ role: 'user', content: ${sq(question)} }],`,
        '  // Documents to check the answer against (removed before the request goes upstream).',
        '  // @ts-expect-error trustagent is a gateway extension, not an OpenAI parameter',
        `  trustagent: { context: [{ id: ${sq(doc.id)}, text: ${sq(doc.text)} }] },`,
        '});',
        'console.log(response.choices[0].message.content);',
      ].join('\n');
    }
    case 'anthropic': {
      const headers = [`"X-TrustAgent-Key": os.environ["${INGEST_KEY_ENV}"]`, ...(route ? [`"X-TrustAgent-Route": ${py(route)}`] : [])];
      return [
        'import os',
        'from anthropic import Anthropic',
        '',
        'client = Anthropic(',
        `    base_url="${origin}",`,
        '    api_key=os.environ["ANTHROPIC_API_KEY"],  # your provider key; the gateway forwards it',
        `    default_headers={${headers.join(', ')}},`,
        ')',
        '',
        'message = client.messages.create(',
        '    model="claude-haiku-4-5",',
        '    max_tokens=512,',
        `    messages=[{"role": "user", "content": ${py(question)}}],`,
        '    # Documents to check the answer against (removed before the request goes upstream).',
        `    extra_body={"trustagent": {"context": [{"id": ${py(doc.id)}, "text": ${py(doc.text)}}]}},`,
        ')',
        'print(message.content[0].text)',
      ].join('\n');
    }
    case 'curl': {
      const body = {
        model,
        messages: [{ role: 'user', content: question }],
        trustagent: { context: [{ id: doc.id, text: doc.text }] },
      };
      return [
        `curl -i ${origin}/v1/chat/completions \\`,
        '  -H "Authorization: Bearer $OPENAI_API_KEY" \\',
        `  -H "X-TrustAgent-Key: $${INGEST_KEY_ENV}" \\`,
        ...(route ? [`  -H ${shellQuote(`X-TrustAgent-Route: ${route}`)} \\`] : []),
        '  -H "Content-Type: application/json" \\',
        `  -d ${shellQuote(JSON.stringify(body, null, 2))}`,
      ].join('\n');
    }
    case 'langchain': {
      const headers = [`"X-TrustAgent-Key": os.environ["${INGEST_KEY_ENV}"]`, ...(route ? [`"X-TrustAgent-Route": ${py(route)}`] : [])];
      return [
        'import os',
        'from langchain_openai import ChatOpenAI',
        '',
        'llm = ChatOpenAI(',
        `    model=${py(model)},`,
        `    base_url="${origin}/v1",`,
        '    api_key=os.environ["OPENAI_API_KEY"],  # your provider key; the gateway forwards it',
        `    default_headers={${headers.join(', ')}},`,
        '    # Documents to check the answer against (removed before the request goes upstream).',
        `    extra_body={"trustagent": {"context": [{"id": ${py(doc.id)}, "text": ${py(doc.text)}}]}},`,
        ')',
        `print(llm.invoke(${py(question)}).content)`,
      ].join('\n');
    }
    default: {
      const unhandled: never = id;
      return unhandled;
    }
  }
}

/** The extension on its own, for the "Why not?" panel: attach documents to any request. */
export function contextExtensionSnippet(doc: { id: string; text: string } = DEFAULTS.document): string {
  return [
    '# Python (OpenAI SDK): pass the documents your model used',
    `extra_body={"trustagent": {"context": [{"id": ${py(doc.id)}, "text": ${py(doc.text)}}]}}`,
    '',
    '# Any client: add this field to the JSON body',
    JSON.stringify({ trustagent: { context: [{ id: doc.id, text: doc.text }] } }),
  ].join('\n');
}


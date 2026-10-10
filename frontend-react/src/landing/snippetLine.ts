/**
 * The SDK line Run it yourself shows: the dashboard's own "Connect your app" snippet
 * (src/domain/snippets.ts), cut to the one line an app changes.
 */
import { snippet } from '@/domain/snippets';

/** The gateway a local stack publishes (PROXY_HOST_PORT, default 8080). */
const LOCAL_GATEWAY = 'http://localhost:8080';

/** The one line an OpenAI Python app changes: the base URL of the dashboard's snippet. */
export function oneLineSnippet(): string {
  const full = snippet('openai-python', { origin: LOCAL_GATEWAY });
  const baseUrl = /base_url="([^"]+)"/.exec(full)?.[1];
  if (!baseUrl) throw new Error('src/domain/snippets.ts no longer sets base_url in the OpenAI Python snippet');
  return `client = OpenAI(base_url="${baseUrl}")`;
}

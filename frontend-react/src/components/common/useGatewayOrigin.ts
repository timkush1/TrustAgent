/**
 * Where apps reach the gateway, for snippets and curl commands: the configured API base when it is
 * an absolute URL (a cross-origin dashboard), else this page's origin plus the base path (the
 * same-origin nginx image serves /v1 next to the dashboard).
 */
import { useRuntimeConfig } from '@/app/services';

export function useGatewayOrigin(): string {
  const config = useRuntimeConfig();
  if (/^https?:\/\//i.test(config.apiBase)) return config.apiBase.replace(/\/+$/, '');
  const origin = typeof window === 'undefined' ? 'http://localhost:3000' : window.location.origin;
  return `${origin}${config.apiBase}`;
}

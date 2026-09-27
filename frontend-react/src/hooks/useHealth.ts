import { useEffect, useState } from 'react';
import { getApiBase, fetchWithAuth } from '../api/client';

export interface HealthStatus {
  proxyUp: boolean;
  auditEngine: boolean;
  version?: string;
}

const POLL_MS = 10000;

/**
 * Polls the proxy /health endpoint so the sidebar shows REAL service status
 * (proxy reachable + whether the Python audit engine gRPC channel is up),
 * instead of hardcoded "running" labels.
 */
export function useHealth(): HealthStatus {
  const [health, setHealth] = useState<HealthStatus>({ proxyUp: false, auditEngine: false });

  useEffect(() => {
    let active = true;
    const check = async () => {
      try {
        const res = await fetchWithAuth(`${getApiBase()}/health`);
        if (!res.ok) throw new Error(String(res.status));
        const data = await res.json();
        if (active) {
          setHealth({ proxyUp: true, auditEngine: !!data.audit_engine, version: data.version });
        }
      } catch {
        if (active) setHealth({ proxyUp: false, auditEngine: false });
      }
    };
    check();
    const id = setInterval(check, POLL_MS);
    return () => {
      active = false;
      clearInterval(id);
    };
  }, []);

  return health;
}

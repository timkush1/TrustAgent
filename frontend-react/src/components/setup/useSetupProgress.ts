/**
 * Where the first-run wizard stands (SPEC §3.3 setup wizard; P4-PLAN ON-7, D39, D87):
 *
 * - secure: this browser is signed in with the admin scope;
 * - connect: a key runs the fast check (bound in the dashboard, or set in .env), or simulated
 *   checks are on;
 * - try: open once the fast check can run, real or simulated.
 *
 * The step to open is the first one not done, unless the address names one (`?step=`).
 */
import { useQuery } from '@tanstack/react-query';
import { useDataSource } from '@/app/services';
import { sessionQuery, settingsQuery, statusQuery } from '@/data/queries';
import type { SessionInfo, SettingsView, SystemStatus } from '@/types/api';

export const SETUP_STEPS = ['secure', 'connect', 'try'] as const;
export type SetupStep = (typeof SETUP_STEPS)[number];

export interface SetupProgress {
  /** Still reading who is signed in, or (for an admin) the settings that decide steps 2 and 3. */
  pending: boolean;
  /** Still reading who is signed in: all step 1 needs. */
  sessionPending: boolean;
  session: SessionInfo | null;
  admin: boolean;
  secured: boolean;
  /** A key runs the fast check. */
  connected: boolean;
  /** Simulated checks are on (D39). */
  simulated: boolean;
  /** Step 3 is open: connected or simulated (D87). */
  canTry: boolean;
  settings: SettingsView | undefined;
  status: SystemStatus | undefined;
  /** The first step not done. */
  next: SetupStep;
}

export function setupProgress(input: { session: SessionInfo | null; settings: SettingsView | undefined; status: SystemStatus | undefined }): Omit<SetupProgress, 'pending' | 'sessionPending'> {
  const { session, settings, status } = input;
  const admin = session?.scopes.includes('admin') ?? false;
  const runtime = settings?.runtime;
  const envKey = status?.decision_model.credential.source === 'env';
  const backend = runtime?.decision_model.backend;
  const connected = backend !== 'off' && backend !== 'mock' && (Boolean(runtime?.bindings.decision_model) || envKey);
  const simulated = backend === 'mock' && status?.demo !== true;
  const canTry = connected || simulated;
  const next: SetupStep = !admin ? 'secure' : !canTry ? 'connect' : 'try';
  return { session, admin, secured: admin, connected, simulated, canTry, settings, status, next };
}

export function useSetupProgress(): SetupProgress {
  const ds = useDataSource();
  const session = useQuery(sessionQuery(ds));
  const signedIn = session.isError ? null : (session.data ?? null);
  const admin = signedIn?.scopes.includes('admin') ?? false;
  const settings = useQuery({ ...settingsQuery(ds), enabled: admin });
  const status = useQuery(statusQuery(ds));
  const progress = setupProgress({ session: signedIn, settings: settings.data, status: status.data });
  const pending = session.isPending || (admin && settings.isPending && !settings.isError);
  return { ...progress, pending, sessionPending: session.isPending };
}

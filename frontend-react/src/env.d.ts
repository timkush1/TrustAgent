/// <reference types="vite/client" />

// Build-time keys (contracts/config/keys.v1.json, group "deploy"). None of them is a secret.
interface ImportMetaEnv {
  readonly VITE_APP_MODE?: 'full' | 'static';
  readonly VITE_BASE_PATH?: string;
  readonly VITE_REPO_URL?: string;
  /** Static build: which recording kind it may present (recorded | mock | auto). */
  readonly VITE_DEMO_DATASET?: string;
  /** Static build: recording time per real time of the replay (default 20). */
  readonly VITE_REPLAY_SPEED?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

/** One scenario of contracts/demo/scenarios.v1.json, served by vite.site.ts scenarioModules(). */
declare module 'virtual:trustagent/scenario/*' {
  const scenario: {
    id: string;
    title: string;
    route: string;
    request: { model: string; messages: { role: string; content: string }[] };
    response: string;
    judge: { claims: { claim: string; quote: string; evidence: string[]; verdict: string }[] } | null;
    expect: { reasons: string[] };
  };
  export default scenario;
}

/** Parts of contracts/ui/copy.v1.json, served by vite.site.ts copyModules(); the importer types them. */
declare module 'virtual:trustagent/copy/*' {
  export const copy: unknown;
}

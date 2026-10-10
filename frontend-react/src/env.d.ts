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
  /** Static live mode: billable calls to OpenRouter per tab, 1-100 (D101; read through config/runtime.ts). */
  readonly VITE_PLAYGROUND_MAX_CALLS?: string;
  /** Static live mode: token cap per call, at most 8,000 (D101; read through config/runtime.ts). */
  readonly VITE_PLAYGROUND_MAX_TOKENS?: string;
  /** Static live mode: the decision model, `typesafe/jev-…` (D137). */
  readonly VITE_JEV_MODEL?: string;
  /** Static live mode: the judge and drafter model, `anthropic/claude-haiku…` (D137). */
  readonly VITE_OPENROUTER_JUDGE_MODEL?: string;
  /** Absolute URL of the published static site: canonical and og:* URLs only, never requests. */
  readonly VITE_SITE_URL?: string;
  /** Build the launch-only routes (/og); only `npm run og` sets it (D115). */
  readonly VITE_LAUNCH_ROUTES?: string;
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

/**
 * config/policies.yaml (the policy the recording and the live Playground run), parsed at build
 * time by vite/plugins/policy.ts, so no YAML parser ships to the browser (D104).
 */
declare module 'virtual:trustagent/policy' {
  export const policy: import('./lib/gate/types').PolicyInput;
}

/**
 * The replay dataset the build copied to dist/demo/ (vite/plugins/dataset.ts, D110): the manifest
 * kind the replay must find there.
 */
declare module 'virtual:trustagent/dataset' {
  export const expected: 'mock' | 'recorded';
}

/** The landing's tape card, built from the chosen manifest by vite/plugins/digest.ts (LD-5). */
declare module 'virtual:trustagent/tape-digest' {
  export interface TapeDigestTrace {
    trace_id: string;
    created_at: string;
    gate_status: string;
    action: string | null;
    would_action: string | null;
    answer_source: string | null;
    question_preview: string | null;
    response_preview: string | null;
  }
  export interface TapeDigest {
    kind: 'mock' | 'recorded';
    label: string;
    recorded_at: string;
    /** The decision model snapshot the tape ran on, when it names one. */
    snapshot: string | null;
    /** The judge as `provider/model`, when the tape names one. */
    judge: string | null;
    /** Traces per outcome id of contracts/ui/outcomes.v1.json. */
    outcomes: Record<string, number>;
    /** At most five example traces, oldest first. */
    examples: TapeDigestTrace[];
  }
  export const digest: TapeDigest;
}

/** Landing-only data (vite/plugins/landingData.ts): no ?raw import, only the parts the page shows. */
declare module 'virtual:trustagent/landing-data' {
  /** contracts/policies/example.policies.yaml, as one string. */
  export const examplePolicy: string;
  /** The parts of contracts/ui/outcomes.v1.json the landing reads. */
  export const outcomes: {
    outcomes: { id: string; label: string; tone: string; headline: string }[];
    reasons: { match: string; copy: string }[];
    rules: { when: Record<string, string[]>; outcome: string }[];
  };
}

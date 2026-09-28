/// <reference types="vite/client" />

// Build-time keys (contracts/config/keys.v1.json, group "deploy"). None of them is a secret.
interface ImportMetaEnv {
  readonly VITE_APP_MODE?: 'full' | 'static';
  readonly VITE_BASE_PATH?: string;
  readonly VITE_REPO_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

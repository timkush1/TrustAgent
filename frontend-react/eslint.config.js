import js from '@eslint/js'
import pluginQuery from '@tanstack/eslint-plugin-query'
import pluginRouter from '@tanstack/eslint-plugin-router'
import jsxA11y from 'eslint-plugin-jsx-a11y'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

/**
 * Fields whose values are server enums (SPEC §2.6). Components never render them raw: words,
 * icons and tones come from src/domain/presentation.ts (SPEC §3.3, fixes UI #6).
 */
const ENUM_FIELDS = [
  'action',
  'would_action',
  'gate_status',
  'status',
  'verdict',
  'audit_verdict',
  'level',
  'skip_reason',
  'degraded',
  'mode',
  'effective_mode',
  'deep_audit',
  'selection',
  'lane',
  'calibration',
  'calibration_state',
  'backend',
  'state',
  'outcome',
  'tone',
  'grade',
].join('|')
const UPPER_VERDICTS =
  'SUPPORTED|PARTIALLY_SUPPORTED|CONTRADICTED|UNSUPPORTED|NOT_CHECKED|GROUNDED|HALLUCINATED|UNVERIFIABLE|NO_CLAIMS|NOT_ASSESSED'
const rawEnumMessage =
  'Render server enums through src/domain/presentation.ts (outcomeView, levelWord, claimVerdictView, …), never as raw strings.'
const member = `MemberExpression[computed=false][property.name=/^(${ENUM_FIELDS})$/]`
const rawEnumSelectors = [
  // <span>{trace.action}</span>, <>{t?.verdict}</>
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > ${member}`,
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > ChainExpression > ${member}`,
  // <span>{`${trace.action} ...`}</span>
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > TemplateLiteral > ${member}`,
  // <span>{trace.action ?? '—'}</span>
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > LogicalExpression > ${member}`,
  // Upper-case verdict tokens typed into markup.
  `JSXText[value=/\\b(${UPPER_VERDICTS})\\b/]`,
  `:matches(JSXElement, JSXFragment) > JSXExpressionContainer > Literal[value=/^(${UPPER_VERDICTS})$/]`,
].map((selector) => ({ selector, message: rawEnumMessage }))

/**
 * D102: the key store's sessionStorage names (`ta.openrouter.*`, `ta.pkce.*`) appear only in
 * src/lib/openrouter/keyStore.ts, so no other module can read or write the key directly.
 */
const secretKeyMessage = 'The key store names (ta.openrouter.*, ta.pkce.*) live only in src/lib/openrouter/keyStore.ts (D102).'
const secretKeySelectors = [
  'Literal[value=/^ta\\.(openrouter|pkce)\\./]',
  'TemplateElement[value.raw=/^ta\\.(openrouter|pkce)\\./]',
].map((selector) => ({ selector, message: secretKeyMessage }))

/** D102: a link that opens a new tab never hands it this tab (sessionStorage, window.opener). */
const linkSelectors = [
  {
    selector:
      "JSXOpeningElement:has(> JSXAttribute[name.name='target'][value.value='_blank']):not(:has(> JSXAttribute[name.name='rel'][value.value=/noopener/][value.value=/noreferrer/]))",
    message: 'A link with target="_blank" carries rel="noopener noreferrer" (D102).',
  },
]

/**
 * D138: the only modules that may reach the network. Everything else goes through them (the
 * DataSource, the OpenRouter transport), so a new request path cannot appear unreviewed.
 * src/landing/TryIt.tsx leaves this list when LD-5 removes its fetch (I-3).
 */
const NETWORK_ALLOWED = [
  'src/config/runtime.ts',
  'src/data/http.ts',
  'src/data/live/**',
  'src/data/replay/replayDataSource.ts',
  'src/data/replay/recording.ts',
  'src/lib/openrouter/http.ts',
  'src/landing/TryIt.tsx',
]
const networkMessage = 'Network access goes through the D138 allow-list (data/http.ts, data/live, the replay loader, lib/openrouter/http.ts).'
const networkGlobals = ['fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource'].map((name) => ({ name, message: networkMessage }))
const networkProperties = [
  ...['window', 'globalThis', 'self'].flatMap((object) =>
    ['fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource'].map((property) => ({ object, property, message: networkMessage })),
  ),
  { object: 'navigator', property: 'sendBeacon', message: networkMessage },
]

/** D102: live mode stores nothing outside sessionStorage (no localStorage, no cookies). */
const LIVE_FILES = [
  'src/lib/openrouter/**',
  'src/lib/gate/**',
  'src/components/replay/live/**',
  'src/components/setup/StaticConnectCallback.tsx',
  'src/data/replay/replayStudioPlane.ts',
]
const storageMessage = 'Live mode keeps the key in sessionStorage only: no localStorage, no cookies (D102).'
const storageGlobals = [{ name: 'localStorage', message: storageMessage }]
const storageProperties = [
  ...['window', 'globalThis', 'self'].map((object) => ({ object, property: 'localStorage', message: storageMessage })),
  { object: 'document', property: 'cookie', message: storageMessage },
]

const TESTS = ['**/*.test.ts', '**/*.test.tsx', 'src/test/**']

export default defineConfig([
  globalIgnores(['dist', 'src/routeTree.gen.ts', 'playwright-report', 'test-results']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
      jsxA11y.flatConfigs.recommended,
      pluginQuery.configs['flat/recommended'],
      pluginRouter.configs['flat/recommended'],
    ],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    rules: {
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
  {
    // File routes export only `Route` and keep their page component private. The router plugin
    // (autoCodeSplitting) moves each component into its own module, which is what Fast Refresh sees.
    files: ['src/routes/**/*.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  // In flat config a later object's options REPLACE an earlier one's, so each object carries its
  // whole merged list (D102): never split one rule's list across objects.
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: TESTS,
    rules: {
      'no-restricted-syntax': ['error', ...secretKeySelectors, ...linkSelectors],
    },
  },
  {
    files: ['src/components/**/*.tsx', 'src/routes/**/*.tsx'],
    ignores: TESTS,
    rules: {
      'no-restricted-syntax': ['error', ...rawEnumSelectors, ...secretKeySelectors, ...linkSelectors],
    },
  },
  {
    // The key store is the one module that names the storage keys.
    files: ['src/lib/openrouter/keyStore.ts'],
    rules: {
      'no-restricted-syntax': ['error', ...linkSelectors],
    },
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: [...TESTS, ...NETWORK_ALLOWED],
    rules: {
      'no-restricted-globals': ['error', ...networkGlobals],
      'no-restricted-properties': ['error', ...networkProperties],
    },
  },
  {
    files: LIVE_FILES,
    ignores: [...TESTS, ...NETWORK_ALLOWED],
    rules: {
      'no-restricted-globals': ['error', ...networkGlobals, ...storageGlobals],
      'no-restricted-properties': ['error', ...networkProperties, ...storageProperties],
    },
  },
  {
    // A live module that may reach the network still stores nothing outside sessionStorage.
    files: NETWORK_ALLOWED.filter((f) => LIVE_FILES.some((l) => f.startsWith(l.replace('/**', '/')))),
    ignores: TESTS,
    rules: {
      'no-restricted-globals': ['error', ...storageGlobals],
      'no-restricted-properties': ['error', ...storageProperties],
    },
  },
  {
    files: ['vite.config.ts', 'vite.site.ts', 'vite/**/*.ts', 'playwright.config.ts', 'e2e/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
])

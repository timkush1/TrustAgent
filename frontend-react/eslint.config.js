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
  {
    files: ['src/components/**/*.tsx', 'src/routes/**/*.tsx'],
    ignores: ['**/*.test.tsx'],
    rules: {
      'no-restricted-syntax': ['error', ...rawEnumSelectors],
    },
  },
  {
    files: ['vite.config.ts', 'playwright.config.ts', 'e2e/**/*.ts'],
    languageOptions: { globals: globals.node },
  },
])

/** Settings sections (SPEC §3.3), in navigation order. */
export const SETTINGS_SECTIONS = [
  { id: 'connections', label: 'Connections', planned: 'OpenRouter, TypeSafe, Anthropic and OpenAI keys: connect, test, remove' },
  { id: 'models', label: 'Models', planned: 'The fast-check backend and the deep-audit judge' },
  { id: 'budgets', label: 'Budgets', planned: "Daily spend caps with today's spend" },
  { id: 'access', label: 'Access', planned: 'API keys with scopes, and dashboard sessions' },
  { id: 'addons', label: 'Add-ons', planned: 'The knowledge base and a local LLM' },
  { id: 'all', label: 'All configuration', planned: 'Every setting, where it comes from, read-only' },
] as const;

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]['id'];

export function isSettingsSection(s: string): s is SettingsSection {
  return SETTINGS_SECTIONS.some((x) => x.id === s);
}

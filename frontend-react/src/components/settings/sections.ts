/** Settings sections (SPEC §3.3), in navigation order. */
export const SETTINGS_SECTIONS = [
  { id: 'connections', label: 'Connections' },
  { id: 'models', label: 'Models' },
  { id: 'budgets', label: 'Budgets' },
  { id: 'access', label: 'Access' },
  { id: 'addons', label: 'Add-ons' },
  { id: 'all', label: 'All configuration' },
] as const;

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]['id'];

export function isSettingsSection(s: string): s is SettingsSection {
  return SETTINGS_SECTIONS.some((x) => x.id === s);
}

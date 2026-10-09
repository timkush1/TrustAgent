/**
 * The component of each Settings section (SPEC §3.3), rendered by routes/app/settings/$section.tsx.
 * Lead-owned: each section's owner edits only its own file
 * (BudgetsSection.tsx is WEB-SV's; the other five are WEB-ON's).
 */
import type { ComponentType } from 'react';
import { AccessSection } from './AccessSection';
import { AddonsSection } from './AddonsSection';
import { AllConfigSection } from './AllConfigSection';
import { BudgetsSection } from './BudgetsSection';
import { ConnectionsSection } from './ConnectionsSection';
import { ModelsSection } from './ModelsSection';
import type { SettingsSection } from './sections';

export const SECTION_COMPONENTS: Record<SettingsSection, ComponentType> = {
  connections: ConnectionsSection,
  models: ModelsSection,
  budgets: BudgetsSection,
  access: AccessSection,
  addons: AddonsSection,
  all: AllConfigSection,
};

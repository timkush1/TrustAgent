/**
 * The six navigation items (SPEC §3.3 information architecture). Knowledge appears only when the
 * add-on is enabled (P3). `phase` is when the full page arrives; until then the route renders a
 * designed placeholder that says so.
 */
import { BookOpen, FlaskConical, LayoutDashboard, ListChecks, Rows3, Settings, ShieldCheck, type LucideIcon } from 'lucide-react';

export interface NavItem {
  label: string;
  to: '/app/overview' | '/app/traffic' | '/app/evaluators' | '/app/policies' | '/app/playground' | '/app/knowledge' | '/app/settings';
  icon: LucideIcon;
  /** The question the page answers (SPEC §3.3 IA table). */
  question: string;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { label: 'Overview', to: '/app/overview', icon: LayoutDashboard, question: 'Is my AI answering safely, and what does checking cost?' },
  { label: 'Traffic', to: '/app/traffic', icon: Rows3, question: 'What happened to this request?' },
  { label: 'Evaluators', to: '/app/evaluators', icon: ListChecks, question: 'What do we check, and how well?' },
  { label: 'Policies', to: '/app/policies', icon: ShieldCheck, question: 'What does each route do with a risky answer?' },
  { label: 'Playground', to: '/app/playground', icon: FlaskConical, question: 'Try it now' },
  { label: 'Settings', to: '/app/settings', icon: Settings, question: 'Connected, and within budget?' },
];

/** Knowledge (SPEC §3.3): listed after Playground only while the knowledge add-on is enabled. */
export const KNOWLEDGE_NAV_ITEM: NavItem = {
  label: 'Knowledge',
  to: '/app/knowledge',
  icon: BookOpen,
  question: 'What do the uploaded documents say?',
};

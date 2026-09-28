/**
 * Typed views of the UI contract files (contracts/ui/*.v1.json; schemas in contracts/schema/ui-*.json).
 *
 * JSON imports widen every string, so the shapes are declared here and the files are cast once.
 * contracts.test.ts proves the declared unions and the files agree, and `make contracts-check`
 * validates the files against their schemas.
 */
import type { GateAction, GateStatus, WouldAction } from '@/types/api';
import commandsJson from '@contracts/ui/commands.v1.json';
import copyJson from '@contracts/ui/copy.v1.json';
import glossaryJson from '@contracts/ui/glossary.v1.json';
import outcomesJson from '@contracts/ui/outcomes.v1.json';

export const OUTCOME_IDS = [
  'checking',
  'blocked',
  'replaced',
  'flagged',
  'would_block',
  'would_flag',
  'error',
  'not_checked',
  'passed',
] as const;
export type OutcomeId = (typeof OUTCOME_IDS)[number];

export type Tone = 'neutral' | 'success' | 'info' | 'warning' | 'danger';

export interface OutcomeEntry {
  id: OutcomeId;
  label: string;
  tone: Tone;
  headline: string;
  next_step: string | null;
}

export interface OutcomeRule {
  when: {
    gate_status?: (GateStatus | 'pending')[];
    action?: GateAction[];
    would_action?: WouldAction[];
  };
  outcome: OutcomeId;
}

export interface OutcomesContract {
  version: '1';
  outcomes: OutcomeEntry[];
  rules: OutcomeRule[];
  levels: Record<'ok' | 'warn' | 'critical', { label: string; tone: Tone }>;
  reasons: { match: string; copy: string }[];
  status_reasons: Record<string, string>;
}

export interface GlossaryTerm {
  id: string;
  term: string;
  short: string;
  aliases?: string[];
  see_also?: string[];
}

export interface GlossaryContract {
  version: '1';
  terms: GlossaryTerm[];
  avoid_in_primary_ui: string[];
}

export interface CommandEntry {
  id: string;
  command: string;
  description: string;
  services?: string[];
}

export interface CommandsContract {
  version: '1';
  commands: CommandEntry[];
}

export const outcomesContract = outcomesJson as OutcomesContract;
export const glossaryContract = glossaryJson as GlossaryContract;
export const commandsContract = commandsJson as CommandsContract;
/** The UI copy tree (contracts/ui/copy.v1.json). Read it through the helpers in presentation.ts. */
export const copy = copyJson.copy;

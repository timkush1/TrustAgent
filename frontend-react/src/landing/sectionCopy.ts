/**
 * The contract words of the landing's lazy sections (Calculator, Write your own checks, Try it,
 * Compare, Run it yourself), in their own module so they load with those sections, after the first
 * paint, instead of counting against the 110 KB first load. Keep the list and SectionCopy in step;
 * landing.test.tsx checks them against the contract.
 */
import type { copy as contractCopy } from '@contracts/ui/copy.v1.json';
import { copy as words } from 'virtual:trustagent/copy/limits,playground_static,savings,setup.step_connect.privacy,compare';

type ContractCopy = typeof contractCopy;
/** The parts of contracts/ui/copy.v1.json the lazy sections read. */
export type SectionCopy = Pick<ContractCopy, 'limits' | 'playground_static' | 'savings' | 'compare'> & {
  setup: { step_connect: Pick<ContractCopy['setup']['step_connect'], 'privacy'> };
};
export const sectionCopy = words as SectionCopy;

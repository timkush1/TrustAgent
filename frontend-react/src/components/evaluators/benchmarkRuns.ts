/**
 * The committed benchmark runs (docs/benchmarks/runs/*.json, SPEC §2.13), bundled at build time so
 * the static build has them too (P4-PLAN D72).
 */
import type { BenchmarkRun } from '@/types/api';

const files = import.meta.glob<BenchmarkRun>('@bench/runs/*.json', { eager: true, import: 'default' });

/** Committed runs that are measured (never mock output), newest first. */
export function committedRuns(all: readonly unknown[] = Object.values(files)): BenchmarkRun[] {
  return all
    .filter((r): r is BenchmarkRun => typeof r === 'object' && r !== null && (r as BenchmarkRun).measured === true && (r as BenchmarkRun).mock === false)
    .sort((a, b) => b.created_at.localeCompare(a.created_at));
}


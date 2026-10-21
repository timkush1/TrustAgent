/**
 * The committed benchmark runs (docs/benchmarks/results/<run_id>.json, SPEC §2.13, D111), bundled
 * at build time. Any run that is not measured is left out. The
 * static build hides the Benchmarks page (D111, SPEC Appendix G); load runs
 * (docs/benchmarks/load/) are never read here.
 */
import type { BenchmarkRun } from '@/types/api';

const files = import.meta.glob<BenchmarkRun>('@bench/results/*.json', { eager: true, import: 'default' });

/** Committed runs that are measured (never mock output), newest first. */
export function committedRuns(all: readonly unknown[] = Object.values(files)): BenchmarkRun[] {
  return all
    .filter((r): r is BenchmarkRun => typeof r === 'object' && r !== null && (r as BenchmarkRun).measured === true && (r as BenchmarkRun).mock === false)
    .sort((a, b) => b.created_at.localeCompare(a.created_at));
}


// Pure helpers of scripts/load/gateway-overhead.js, kept apart from the k6 modules so that
// scripts/tests/test_load.py can run them under node: the Prometheus text parser, histogram deltas
// and percentiles, and the target form of a k6 threshold.

// parseMetrics reads the Prometheus text format: [{name, labels, value}] (comments skipped).
export function parseMetrics(text) {
  const out = [];
  for (const raw of String(text).split('\n')) {
    const line = raw.trim();
    if (line === '' || line.startsWith('#')) continue;
    const m = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})?\s+(\S+)/);
    if (!m) continue;
    const labels = {};
    if (m[3]) {
      const re = /([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"/g;
      let l;
      while ((l = re.exec(m[3])) !== null) labels[l[1]] = l[2];
    }
    out.push({ name: m[1], labels, value: Number(m[4]) });
  }
  return out;
}

// sumOf adds up the samples of name whose labels include want.
export function sumOf(samples, name, want) {
  let total = 0;
  for (const s of samples) {
    if (s.name !== name) continue;
    if (Object.entries(want || {}).every(([k, v]) => s.labels[k] === v)) total += s.value;
  }
  return total;
}

// buckets is a histogram's cumulative counts by upper bound (le), for the samples matching want.
export function buckets(samples, name, want) {
  const by = {};
  for (const s of samples) {
    if (s.name !== `${name}_bucket`) continue;
    if (!Object.entries(want || {}).every(([k, v]) => s.labels[k] === v)) continue;
    const le = s.labels.le === '+Inf' ? Infinity : Number(s.labels.le);
    by[le] = (by[le] || 0) + s.value;
  }
  return by;
}

// histogramStats are the n and percentiles (ms) of the observations between two readings of a
// histogram (cumulative counts by le, in seconds), interpolated within buckets as Prometheus's
// histogram_quantile does; max is the upper bound of the highest bucket that grew (null for +Inf).
export function histogramStats(before, after) {
  const les = Object.keys(after)
    .map(Number)
    .sort((x, y) => x - y);
  const counts = les.map((le) => (after[le] || 0) - (before[le] || 0));
  const n = counts.length ? counts[counts.length - 1] : 0;
  if (n <= 0) return { n: 0, p50: null, p95: null, p99: null, max: null };
  const q = (p) => {
    const rank = p * n;
    for (let i = 0; i < les.length; i++) {
      if (counts[i] >= rank) {
        if (les[i] === Infinity) return i > 0 ? les[i - 1] * 1000 : null;
        const lower = i > 0 ? les[i - 1] : 0;
        const below = i > 0 ? counts[i - 1] : 0;
        const inBucket = counts[i] - below;
        const frac = inBucket > 0 ? (rank - below) / inBucket : 1;
        return (lower + (les[i] - lower) * frac) * 1000;
      }
    }
    return null;
  };
  let max = null;
  for (let i = 0; i < les.length; i++) {
    const grew = counts[i] - (i > 0 ? counts[i - 1] : 0);
    if (grew > 0) max = les[i] === Infinity ? null : les[i] * 1000;
  }
  return { n, p50: q(0.5), p95: q(0.95), p99: q(0.99), max };
}

// target is a threshold as the run file states it: {stat, max_ms}.
export function target(threshold) {
  if (!threshold) return null;
  const m = threshold.match(/^p\((50|95|99)\)<=?([0-9.]+)$/);
  if (!m) throw new Error(`threshold ${threshold} has no target form`);
  return { stat: `p${m[1]}_ms`, max_ms: Number(m[2]), inclusive: threshold.includes('<=') };
}

// decisionRequests counts the decision requests among a mock's request totals (GET /_mock/requests
// totals, by "<METHOD> <path>"): every decision endpoint mock-upstream serves.
export function decisionRequests(totals) {
  let n = 0;
  for (const [key, value] of Object.entries(totals || {})) {
    if (/^POST \/(v1\/systemone|api\/v1\/systemone|api\/alpha\/decisions)$/.test(key)) n += value;
  }
  return n;
}

#!/usr/bin/env node
/**
 * check-claims.mjs - keep every figure in the README and the docs honest.
 *
 * contracts/claims/value-claims.v1.json is the one list of public figures, each with its label
 * (measured, estimate, third-party, vendor or simulated). The landing page renders from it; prose
 * cites it with an HTML comment in front of the figure:
 *
 *   - Fast check on every answer: <!-- claim:fast_check_cost_per_1k --> ~$0.21 / 1,000 answers [estimate]
 *
 * This script reads README.md and docs/**\/*.md (docs/launch included) and fails when the prose
 * drifts from the contract.
 *
 * Rules (rule ids in brackets, as printed)
 *   [unknown-claim]       the marker's id is not in value-claims.v1.json
 *   [wrong-surface]       the claim is not written for the readme or docs surface
 *   [display-missing]     the text after the marker does not hold the claim's display (for a measured
 *                         claim: its value from measured.json, or its empty_display while that is null)
 *   [chip-missing]        no label chip ([estimate], [vendor], [third-party], [measured], [simulated])
 *                         follows the display
 *   [chip-mismatch]       the first chip after the display is not the claim's own label
 *   [caveat-missing]      a claim with a caveat has neither the caveat text nor a link to an anchor
 *                         that holds it within 3 lines after the marker
 *   [measured-mock]       measured.json holds a value but is not a measured run with mock:false
 *   [stray-measured-chip] a [measured] chip outside the text of a measured claim
 *   [measured-placeholder] a {measured:KEY} placeholder names no value in measured.json
 *   [launch-figure]       a $, %, ms or multiplier (27×) figure in docs/launch without a claim marker
 *   [pkce-wording]        prose says PKCE is verified while SPEC Appendix D says it is not
 *   [block-stale]         a <!-- measured:start --> ... <!-- measured:end --> block differs from what
 *                         --write generates (proxy overhead in a table of its own, D118);
 *                         [block-unpaired] when the markers do not pair up
 *   [load-run-missing]    a measured overhead value names a run with no docs/benchmarks/load file
 *   [latency-conditions]  a measured gate latency has no sources.gate_latency.conditions in
 *                         measured.json (D205): the figure would show without how it was taken
 *   [cost-conditions]     a measured cost (or the same-checks ratio) has no sources.<key>.conditions
 *                         in measured.json (D229): the figure would show without the dataset and the
 *                         input tokens per answer it paid for
 *   [empty-measured-chip] a measured claim with no value carries the [measured] chip: an absent
 *                         value is not a measurement, so "not yet measured" goes without it
 *   [pair-unknown]        an estimate's measured_pair names no measured claim
 *   [pair-divergence]     an estimate and its measured pair differ by more than 2× (a warning;
 *                         an error with --launch until the estimate's figure or wording is updated)
 *   [agreement-alone]     our RAGTruth agreement or accuracy is shown in a section without the
 *                         like-for-like third-party figure (accuracy at a 0.5 threshold); a warning
 *                         once the value is measured, an error with --launch (D212)
 *   [third-party-stale]   (warning) a third-party claim's as_of is more than 120 days old
 *   [reverify-open]       (warning) a claim still says what must be re-checked before launch
 *                         (an error with --launch)
 *   [launch-*]            with --launch: the recorded dataset (its vendor hosts, version and
 *                         snapshot), a measured run (its failure rate and one snapshot), a reviewed
 *                         comparison table, the og.png card, no open reverify, no licence
 *                         placeholder (see launchChecks)
 *
 * Markers, chips and figures inside code (fenced blocks and inline code spans) are not read, so a
 * doc can show the syntax. A display, chip, caveat or link inside an HTML comment does not count
 * either: nobody sees it. The prose rules (stray chips, placeholders, launch figures, PKCE wording)
 * apply to the published docs: README.md and docs/ outside docs/v2 (design records) and
 * docs/archive (historical logs).
 *
 * Usage
 *   node scripts/check-claims.mjs                check the repository
 *   node scripts/check-claims.mjs --write        regenerate the measured blocks, then check
 *   node scripts/check-claims.mjs --launch       also run the launch checks
 *   node scripts/check-claims.mjs --self-test    run the built-in fixture tests
 *   options: --root <dir> (another checkout), --today YYYY-MM-DD (the date the age rule uses)
 *
 * Exit codes: 0 no errors (warnings allowed); 1 at least one error, or a self-test failure;
 * 2 usage or configuration error (a missing or unreadable contract file, a bad option).
 */
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, posix, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

export const CLAIMS_PATH = 'contracts/claims/value-claims.v1.json';
export const MEASURED_PATH = 'contracts/claims/measured.json';
export const COMPARE_PATH = 'contracts/claims/compare.v1.json';
export const SPEC_PATH = 'docs/v2/SPEC.md';
export const RECORDED_MANIFEST_PATH = 'frontend-react/demo-data/recorded/manifest.json';
export const OG_PATHS = ['docs/launch/og.png', 'frontend-react/public/og.png'];
export const LOAD_RUNS_DIR = 'docs/benchmarks/load';
export const RESULTS_DIR = 'docs/benchmarks/results';
export const VERSION_PATH = 'VERSION';
/** The schema whose VendorHost enum names the hosts a recorded tape may name (D203). */
export const COMMON_SCHEMA_PATH = 'contracts/schema/common.v1.schema.json';
export const OG_WIDTH = 1280;
export const OG_HEIGHT = 640;
export const OG_MAX_BYTES = 1_000_000;
export const THIRD_PARTY_MAX_AGE_DAYS = 120;
export const CAVEAT_WINDOW_LINES = 3;
/** D212: an estimate and its measured pair may differ by at most this factor before launch. */
export const PAIR_MAX_RATIO = 2;
/** D204: a published run fails at most this share of its examples. */
export const MAX_FAILURE_RATE = 0.05;
/** The measured values that are our agreement with RAGTruth's labels (D212). */
export const AGREEMENT_REFS = ['ragtruth_qa_agreement', 'ragtruth_qa_accuracy'];
/** The like-for-like third-party figure every agreement is shown beside (D212, H-9). */
export const LIKE_FOR_LIKE_ID = 'arize_ragtruth_accuracy_at_default_threshold';
/** The files whose holder or author stays a placeholder until the owner names it (D216, S1). */
export const OWNER_PLACEHOLDER = '{{OWNER_LEGAL_NAME}}';
export const OWNER_FILES = ['LICENSE', 'frontend-react/package.json', 'backend-python/pyproject.toml'];

/** The chip each label is written with in prose. */
export const LABEL_CHIPS = {
  measured: 'measured',
  estimate: 'estimate',
  third_party: 'third-party',
  vendor: 'vendor',
  simulated: 'simulated',
};

const SURFACES = ['readme', 'docs'];
const WRITE_HINT = 'run `node scripts/check-claims.mjs --write` (make claims-write)';
const BLOCK_HEADER =
  '<!-- Generated by `node scripts/check-claims.mjs --write` from contracts/claims/measured.json; do not edit by hand. -->';

const MARKER_RE = /<!--\s*claim:([^>]*?)\s*-->/g;
const BLOCK_RE = /<!--\s*measured:(start|end)\s*-->/;
const CHIP_RE = /\[(estimate|vendor|third[- ]party|measured|simulated)\]/gi;
const MEASURED_CHIP_RE = /\[measured\]/gi;
const PLACEHOLDER_RE = /\{measured:([^{}\s]*)\}/g;
// A money figure, or a number with %, ms or a multiplier (27×, 30x; a 1280×640 size is not one).
const FIGURE_RE = /\$\s?\d[\d,]*(?:\.\d+)?|(?<![\w.$])\d[\d,]*(?:\.\d+)?\s?(?:%|ms\b|(?:×|x\b)(?!\s?\d))/g;
const LINK_RE = /\]\(([^)\s]*#[^)\s]+)\)/g;
const FENCE_RE = /^ {0,3}(`{3,}|~{3,})/;

/** A contract file that cannot be read or parsed: exit 2, not a finding. */
export class ConfigError extends Error {}

// ---------------------------------------------------------------------------------------------
// Markdown helpers

/**
 * Replace fenced code blocks and inline code spans with spaces, keeping every column. With
 * `inline: false` inline code keeps its text (a caveat may name `mock-upstream` in backticks).
 */
export function maskCode(lines, { inline = true } = {}) {
  const out = [];
  let fence = null;
  for (const line of lines) {
    const m = FENCE_RE.exec(line);
    if (fence) {
      out.push(' '.repeat(line.length));
      const rest = m ? line.slice(m.index + m[0].length) : '';
      if (m && m[1][0] === fence[0] && m[1].length >= fence.length && rest.trim() === '') fence = null;
      continue;
    }
    if (m) {
      fence = m[1];
      out.push(' '.repeat(line.length));
      continue;
    }
    out.push(inline ? maskInlineCode(line) : line);
  }
  return out;
}

function maskInlineCode(line) {
  let out = '';
  let i = 0;
  while (i < line.length) {
    if (line[i] !== '`') {
      out += line[i];
      i += 1;
      continue;
    }
    let n = 0;
    while (line[i + n] === '`') n += 1;
    const ticks = '`'.repeat(n);
    let close = -1;
    for (let j = i + n; j < line.length; ) {
      const k = line.indexOf(ticks, j);
      if (k < 0) break;
      let run = 0;
      while (line[k + run] === '`') run += 1;
      if (run === n) {
        close = k;
        break;
      }
      j = k + run;
    }
    if (close < 0) {
      out += ticks;
      i += n;
      continue;
    }
    out += ' '.repeat(close + n - i);
    i = close + n;
  }
  return out;
}

/**
 * Per line, which columns sit inside an HTML comment (a comment may span lines). Run it on
 * code-masked lines, so a `<!--` inside code never opens one.
 */
function commentColumns(lines) {
  const out = [];
  let open = false;
  for (const line of lines) {
    const cols = new Uint8Array(line.length);
    let i = 0;
    while (i < line.length) {
      if (open) {
        const end = line.indexOf('-->', i);
        const stop = end < 0 ? line.length : end + 3;
        cols.fill(1, i, stop);
        i = stop;
        if (end >= 0) open = false;
      } else {
        const start = line.indexOf('<!--', i);
        if (start < 0) break;
        i = start;
        open = true;
      }
    }
    out.push(cols);
  }
  return out;
}

function blankColumns(lines, cols) {
  return lines.map((line, k) => {
    let s = '';
    for (let c = 0; c < line.length; c += 1) s += cols[k][c] ? ' ' : line[c];
    return s;
  });
}

/** Also blank every HTML comment (claim markers included), for the rules that read prose. */
export function maskComments(lines) {
  return blankColumns(lines, commentColumns(lines));
}

/**
 * The text a reader sees, column for column: fenced code and HTML comments blanked, inline code
 * kept (a caveat may name `mock-upstream` in backticks). A chip or a caveat hidden in a comment
 * is not next to anything.
 */
export function visibleText(lines) {
  return blankColumns(maskCode(lines, { inline: false }), commentColumns(maskCode(lines)));
}

/** Whitespace, typographic quotes and emphasis do not change what a sentence says. */
export function normalize(text) {
  return text
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\*\*|__|[*`]/g, '')
    .replace(/[\s   ]+/g, ' ')
    .trim();
}

/** A line that starts a new Markdown block, which ends a claim's text. */
function startsBlock(maskedLine) {
  return (
    maskedLine.trim() === '' ||
    /^ {0,3}#{1,6}(\s|$)/.test(maskedLine) ||
    /^\s*([-*+]|\d{1,9}[.)])\s/.test(maskedLine) ||
    /^\s*\|/.test(maskedLine) ||
    /^\s*>/.test(maskedLine) ||
    FENCE_RE.test(maskedLine) ||
    BLOCK_RE.test(maskedLine)
  );
}

/** The line ranges [from, to) between headings, on code-masked lines (a heading in code is not one). */
function sections(maskedLines) {
  const out = [];
  let from = 0;
  maskedLines.forEach((line, i) => {
    if (i > from && /^ {0,3}#{1,6}(\s|$)/.test(line)) {
      out.push({ from, to: i });
      from = i;
    }
  });
  out.push({ from, to: maskedLines.length });
  return out;
}

function isTableRow(maskedLine) {
  return /^\s*\|/.test(maskedLine);
}

/** The column of the next unescaped `|` at or after `from`, or -1. */
function nextCellBreak(maskedLine, from) {
  for (let i = from; i < maskedLine.length; i += 1) {
    if (maskedLine[i] === '|' && maskedLine[i - 1] !== '\\') return i;
  }
  return -1;
}

/** GitHub's heading anchor: lowercase, punctuation dropped, spaces to hyphens. */
export function slug(heading) {
  return normalize(heading)
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/ /g, '-');
}

function anchorLine(lines, anchor) {
  const escaped = anchor.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const tag = new RegExp(`<a\\s+(?:[^>]*\\s)?(?:id|name)=["']${escaped}["']`);
  const masked = maskCode(lines);
  for (let i = 0; i < lines.length; i += 1) {
    if (tag.test(masked[i])) return i;
    // A heading outside a fence; its anchor keeps the text of inline code, as GitHub's does.
    const h = /^ {0,3}#{1,6}\s/.test(masked[i]) && /^ {0,3}#{1,6}\s+(.*?)\s*#*\s*$/.exec(lines[i]);
    if (h && slug(h[1]) === anchor) return i;
  }
  return -1;
}

/**
 * A regex for a caveat, case-insensitive so it can sit mid-sentence; `{name}` placeholders (the
 * run's machine, say) match any text.
 */
function caveatPattern(caveat) {
  const parts = normalize(caveat)
    .split(/\{[a-z_]+\}/)
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  return new RegExp(parts.join('[\\s\\S]*?'), 'i');
}

// ---------------------------------------------------------------------------------------------
// Claims and measured values

/** Same formatting as the landing page (frontend-react/src/landing/claims.ts). */
export function formatValue(value, unit) {
  if (unit === 'fraction') return `${Math.round(value * 1000) / 10}%`;
  if (unit === 'usd_per_1k') return value >= 1 ? value.toFixed(2) : value.toFixed(3);
  return String(Math.round(value * 100) / 100);
}

function isMeasuredRun(measured) {
  return Boolean(measured) && measured.mock === false && measured.status === 'measured';
}

/**
 * What the prose must say for a claim: its display, or for a measured claim the published value
 * (only from a measured run with mock:false) or its empty display.
 */
export function expectedDisplay(claim, measured) {
  if (claim.label !== 'measured') return { text: claim.display, problem: null, empty: false };
  const value = measuredValue(claim, measured);
  const empty = claim.empty_display ?? 'not yet measured';
  if (value === null) return { text: empty, problem: null, empty: true };
  if (!isMeasuredRun(measured)) {
    return {
      text: empty,
      empty: true,
      problem:
        `${MEASURED_PATH} holds a value for ${claim.measured_ref} but is not a measured run ` +
        `(status ${measured?.status}, mock ${measured?.mock}); a measured figure comes only from a measured run with mock:false`,
    };
  }
  return { text: claim.display.replace('{value}', formatValue(value, claim.unit)), problem: null, empty: false };
}

/** The value measured.json holds for a measured claim, or null (absent, or not yet measured). */
function measuredValue(claim, measured) {
  const value = measured?.values?.[claim.measured_ref]?.value;
  return value === null || value === undefined ? null : value;
}

/** A measured value that may be shown: from a measured run with mock:false, else null. */
function shownValue(claim, measured) {
  return isMeasuredRun(measured) ? measuredValue(claim, measured) : null;
}

/** Says where the latest load run is when a measured run did not adopt one. */
export const LOAD_RUN_NOT_ADOPTED = 'not part of this run; the latest load run against simulated vendors is in GATEWAY.md, section "Load test"';

/**
 * The run machine for a caveat's {machine}, from the load run that measured the claim. When the
 * measured run did not adopt one, it says where a published load run is (loadRuns, the files of
 * docs/benchmarks/load), or that none is published.
 */
function machineText(claim, measured, read, loadRuns = []) {
  const runId = measured?.values?.[claim.measured_ref]?.run_id;
  if (!isMeasuredRun(measured) || !runId) {
    return { text: loadRuns.length > 0 ? LOAD_RUN_NOT_ADOPTED : 'no load run is published yet', problem: null };
  }
  const path = `${LOAD_RUNS_DIR}/${runId}.json`;
  const raw = read(path);
  if (raw === null) return { text: 'machine unknown', problem: `${path} (the run behind ${claim.id}) is missing` };
  try {
    const env = JSON.parse(raw.toString('utf8')).environment;
    return { text: `measured on ${env.cpu_model}, ${env.cores} logical CPUs and ${env.mem_gb} GB RAM visible to ${env.os}`, problem: null };
  } catch (err) {
    return { text: 'machine unknown', problem: `${path}: ${err.message}` };
  }
}

/**
 * The words for a latency caveat's {gate_conditions} (D205, D225): measured.json
 * sources.gate_latency.conditions, as bench publish wrote it (connection, requests at a time,
 * where the run really ran, snapshot, n). Nothing for a value not measured yet: the caveat is
 * then dropped, never shown with a raw placeholder.
 */
export function gateConditions(claim, measured, empty) {
  if (empty) return { text: null, problem: null };
  const text = measured?.sources?.gate_latency?.conditions;
  if (typeof text === 'string' && text.trim()) return { text: text.trim(), problem: null };
  return {
    text: 'conditions not recorded',
    problem: `${MEASURED_PATH} publishes ${claim.measured_ref} without sources.gate_latency.conditions (D205): rerun make bench-publish`,
  };
}

/**
 * The measured.json sources key whose conditions fill a caveat's {cost_conditions} (D229): a cost
 * per 1,000 answers its own (not the proxy's), the same-checks ratio the LLM judge's cost, which
 * names the judge. Mirrors contracts/validate.mjs costConditionsKey; null when none applies.
 */
export function costConditionsKey(ref, entry) {
  if (ref === 'same_checks_cost_ratio') return 'llm_same_checks_usd_per_1k';
  if (entry?.unit === 'usd_per_1k' && entry.system !== 'proxy') return ref;
  return null;
}

/**
 * The words for a cost caveat's {cost_conditions} (D229): measured.json sources.<key>.conditions,
 * as bench publish wrote it (the dataset, n, the input tokens per answer, the judge model). A cost
 * per answer grows with the documents' length, so it is never shown without them. Nothing for a
 * value not measured yet: the caveat is then dropped, never shown with a raw placeholder.
 */
export function costConditions(claim, measured, empty) {
  if (empty) return { text: null, problem: null };
  const key = costConditionsKey(claim.measured_ref, measured?.values?.[claim.measured_ref]);
  const text = key ? measured?.sources?.[key]?.conditions : null;
  if (typeof text === 'string' && text.trim()) return { text: text.trim(), problem: null };
  return {
    text: 'conditions not recorded',
    problem: `${MEASURED_PATH} publishes ${claim.measured_ref} without sources.${key ?? claim.measured_ref}.conditions (D229): rerun make bench-publish`,
  };
}

function inMeasuredBlock(claim) {
  return claim.label === 'measured' && claim.surfaces.some((s) => SURFACES.includes(s));
}

/** A measured claim about the gateway's own time (measured.json system "proxy"), not the fast check. */
function isProxyOverhead(claim, measured) {
  return measured?.values?.[claim.measured_ref]?.system === 'proxy';
}

export const OVERHEAD_INTRO = "The gateway's own overhead, measured separately by a load test against simulated vendors:";

/** The block before any measured run is published: one sentence, no empty tables. */
export const NOT_MEASURED_TEXT =
  'No benchmark run of our own is published yet, so there are no measured results here: every other figure is ' +
  'labelled as an estimate, a vendor price or a third-party result.';
export const LOAD_RUN_PUBLISHED_TEXT =
  'The gateway\'s own overhead, from a load test against simulated vendors, is in GATEWAY.md, section "Load test".';

const INTERVAL_METHODS = { wilson: 'Wilson', bootstrap: 'bootstrap' };

/** The snapshot words of a measured run: one snapshot, or both when the run is split (D207). */
export function snapshotText(measured) {
  const snap = measured?.snapshot;
  if (Array.isArray(snap)) return `decision-model snapshots ${snap.join(' and ')}`;
  return snap ? `decision-model snapshot ${snap}` : '';
}

/**
 * What a measured row's note says before its caveat: where a cost comes from (D204), the 95%
 * interval with n per class (D212), the value before calibration beside an "after" one.
 */
function measuredNote(claim, measured) {
  const ref = claim.measured_ref;
  const parts = [];
  const source = measured?.sources?.[ref]?.cost_source;
  if (source === 'reported') parts.push('Cost as the vendor reported it.');
  else if (source === 'computed') parts.push('Measured tokens × vendor price.');
  const iv = measured?.intervals?.[ref];
  if (iv && typeof iv.lo === 'number' && typeof iv.hi === 'number') {
    const method = INTERVAL_METHODS[iv.method] ?? iv.method;
    const how = iv.method === 'bootstrap' && iv.resamples ? `${method}, ${iv.resamples} resamples` : method;
    let text = `95% interval ${formatValue(iv.lo, claim.unit)} to ${formatValue(iv.hi, claim.unit)} (${how})`;
    if (Number.isInteger(iv.n_pos) && Number.isInteger(iv.n_neg)) {
      text += `; ${iv.n_pos} answers with a hallucination and ${iv.n_neg} without`;
    }
    parts.push(`${text}.`);
  }
  if (ref.endsWith('_after')) {
    const before = measured?.values?.[`${ref.slice(0, -'_after'.length)}_before`]?.value;
    if (typeof before === 'number') parts.push(`Before calibration: ${formatValue(before, claim.unit)}.`);
  }
  return parts.join(' ');
}

/**
 * The like-for-like third-party figure, written after a measured agreement (D212): with its
 * marker where the claim is written for both the readme and the docs, as plain text otherwise.
 */
function likeForLikeLine(claims) {
  const c = claims.claims.find((x) => x.id === LIKE_FOR_LIKE_ID);
  if (!c) return null;
  const marked = SURFACES.every((s) => c.surfaces.includes(s));
  const figure = `${marked ? `<!-- claim:${c.id} --> ` : ''}${c.display} [${LABEL_CHIPS[c.label]}]`;
  return `Like for like (default thresholds): ${c.text.replace(/:\s*/g, ', ')}: ${figure}. ${c.caveat ?? ''}`.trimEnd();
}

/**
 * The lines between <!-- measured:start --> and <!-- measured:end -->, as --write writes them.
 * Before a measured run is published, one sentence. After it, proxy overhead gets a table
 * of its own after the fast-check results, so it never sits next to the fast check's latency
 * (D118); a value the run did not cover says "not yet measured" without the [measured] chip; an
 * agreement row is followed by the like-for-like third-party figure (D212).
 */
export function renderMeasuredBlock(claims, measured, read, loadRuns = []) {
  const problems = [];
  const lines = [BLOCK_HEADER, ''];
  if (!isMeasuredRun(measured)) {
    lines.push(loadRuns.length ? `${NOT_MEASURED_TEXT} ${LOAD_RUN_PUBLISHED_TEXT}` : NOT_MEASURED_TEXT);
    return { lines, problems };
  }
  const cell = (s) => s.replace(/\|/g, '\\|');
  const row = (c) => {
    const shown = expectedDisplay(c, measured);
    let caveat = c.caveat ?? '';
    if (caveat.includes('{machine}')) {
      const machine = machineText(c, measured, read, loadRuns);
      if (machine.problem) problems.push(machine.problem);
      caveat = caveat.replace('{machine}', machine.text);
    }
    if (caveat.includes('{gate_conditions}')) {
      const filled = gateConditions(c, measured, shown.empty);
      if (filled.problem) problems.push(filled.problem);
      caveat = filled.text === null ? '' : caveat.replace('{gate_conditions}', filled.text);
    }
    if (caveat.includes('{cost_conditions}')) {
      const filled = costConditions(c, measured, shown.empty);
      if (filled.problem) problems.push(filled.problem);
      caveat = filled.text === null ? '' : caveat.replace('{cost_conditions}', filled.text);
    }
    const note = [shown.empty ? '' : measuredNote(c, measured), caveat].filter(Boolean).join(' ');
    const result = shown.empty ? shown.text : `${shown.text} [${LABEL_CHIPS.measured}]`;
    return `| ${cell(c.text)} | <!-- claim:${c.id} --> ${cell(result)} |${note ? ` ${cell(note)} ` : ' '}|`;
  };
  const table = (rows) => ['| What we measure | Our result | Note |', '|---|---|---|', ...rows];
  const listed = claims.claims.filter(inMeasuredBlock);
  const resultClaims = listed.filter((c) => !isProxyOverhead(c, measured));
  const results = resultClaims.map(row);
  const overhead = listed.filter((c) => isProxyOverhead(c, measured)).map(row);
  if (results.length) lines.push(...table(results), '');
  const agreement = resultClaims.some((c) => AGREEMENT_REFS.includes(c.measured_ref) && shownValue(c, measured) !== null);
  const like = agreement ? likeForLikeLine(claims) : null;
  if (like) lines.push(like, '');
  if (overhead.length) lines.push(OVERHEAD_INTRO, '', ...table(overhead), '');
  const details = [measured.generated_by, measured.generated_at?.slice(0, 10), snapshotText(measured)].filter(Boolean).join(', ');
  lines.push(
    `From ${measured.runs.length === 1 ? 'the measured run' : 'the measured runs'} ${measured.runs.join(', ')}` +
      `${details ? ` (${details})` : ''}. A row that says "not yet measured" was not part of ${measured.runs.length === 1 ? 'that run' : 'those runs'}.`,
  );
  return { lines, problems };
}

// ---------------------------------------------------------------------------------------------
// PKCE wording (SPEC Appendix D)

/** Which OpenRouter PKCE callbacks SPEC Appendix D records as verified, with a date. */
export function pkceStatus(specText) {
  const status = { localhost: false, pages: false, found: false };
  if (!specText) return status;
  let inAppendix = false;
  for (const line of specText.split(/\r?\n/)) {
    if (/^## /.test(line)) inAppendix = /^## Appendix D\b/.test(line);
    if (!inAppendix || !/^\s*\|/.test(line)) continue;
    const cells = line.split(/(?<!\\)\|/).slice(1, -1).map((c) => c.trim());
    if (cells.length < 2 || !/\bPKCE\b/.test(cells[0])) continue;
    const verified = /\bVerified\s+(?:on\s+)?\d{4}-\d{2}-\d{2}\b/i.test(cells[1]) && !/\bunverified\b/i.test(cells[1]);
    if (/localhost/i.test(cells[0])) {
      status.localhost = verified;
      status.found = true;
    } else if (/\bPages\b/i.test(cells[0])) {
      status.pages = verified;
      status.found = true;
    }
  }
  return status;
}

// "Verified" is not a claim after a negation earlier in the sentence ("not yet verified", "no doc
// may call PKCE verified"), after a condition in the same clause ("until PKCE is verified"), or in
// a sentence about the mock OpenRouter. "Unverified" never matches \bverified\b. Words after
// "verified" ("verified only on localhost", "verified after a real round trip") hedge nothing.
const PKCE_NEGATION_RE = /\b(?:not|never|no|yet|without|pending)\b|n't\b/i;
const PKCE_CONDITION_RE = /\b(?:until|unless|once|after|before|when|if|whether)\b/i;
const PKCE_MOCK_RE = /\bmock/i;
const CLAUSE_BREAK_RE = /[,;:()\u2013\u2014]/;

/** True when a sentence calls PKCE verified without a hedge. */
export function pkceCalledVerified(sentence) {
  if (!/\bPKCE\b/i.test(sentence) || PKCE_MOCK_RE.test(sentence)) return false;
  for (const m of sentence.matchAll(/\bverified\b/gi)) {
    const before = sentence.slice(0, m.index);
    const clause = before.split(CLAUSE_BREAK_RE).pop();
    if (!PKCE_NEGATION_RE.test(before) && !PKCE_CONDITION_RE.test(clause)) return true;
  }
  return false;
}

/**
 * Sentences that call PKCE verified, unhedged, where Appendix D says that callback is not. A
 * paragraph is read as sentences across its wrapped lines; a list item or a quote starts a new
 * unit, and a heading or a table row is a unit of its own (a row's cells are clauses of one unit).
 */
export function pkceWordingHits(proseLines, status) {
  const hits = [];
  if (status.localhost && status.pages) return hits;
  let block = [];
  const flush = () => {
    if (!block.length) return;
    let text = '';
    const lineAt = [];
    for (const { line, text: t } of block) {
      for (let k = 0; k <= t.length; k += 1) lineAt.push(line);
      text += `${t} `;
    }
    const re = /[^.!?]+[.!?]*/g;
    for (let m = re.exec(text); m; m = re.exec(text)) {
      const sentence = m[0];
      if (!pkceCalledVerified(sentence)) continue;
      const localhost = /localhost|self-hosted|self hosted/i.test(sentence);
      const pages = /\bpages\b|github\.io|static/i.test(sentence);
      const unverified =
        (localhost && !status.localhost) ||
        (pages && !status.pages) ||
        (!localhost && !pages && !(status.localhost && status.pages));
      if (unverified) hits.push({ line: lineAt[m.index] ?? block[0].line, sentence: normalize(sentence) });
    }
    block = [];
  };
  proseLines.forEach((t, i) => {
    if (t.trim() === '') {
      flush();
    } else if (isTableRow(t) || /^ {0,3}#{1,6}(\s|$)/.test(t)) {
      flush();
      block.push({ line: i + 1, text: t.replace(/(?<!\\)\|/g, ';') });
      flush();
    } else {
      if (/^\s*([-*+]|\d{1,9}[.)])\s/.test(t) || /^\s*>/.test(t)) flush();
      block.push({ line: i + 1, text: t });
    }
  });
  flush();
  return hits;
}

// ---------------------------------------------------------------------------------------------
// The check

function isPublished(path) {
  return path === 'README.md' || (path.startsWith('docs/') && !/^docs\/(v2|archive)\//.test(path));
}

/**
 * Every claim marker of a file, with the text that follows it (the claim's span). Markers are
 * found in `masked` (code blanked); the span's text is read from `prose` (comments blanked too), so
 * a display or chip inside an HTML comment, which nobody sees, does not count.
 */
function findMarkers(masked, prose) {
  const markers = [];
  masked.forEach((line, i) => {
    for (const m of line.matchAll(MARKER_RE)) {
      markers.push({ line: i, start: m.index, end: m.index + m[0].length, id: m[1].trim() });
    }
  });
  for (const mk of markers) {
    const segments = [];
    let ln = mk.line;
    let from = mk.end;
    for (;;) {
      const text = masked[ln];
      const next = markers.find((o) => o.line === ln && o.start >= from);
      let to = next ? next.start : text.length;
      if (!next && isTableRow(masked[ln])) {
        const cell = nextCellBreak(text, from);
        if (cell >= 0) to = cell;
      }
      segments.push({ line: ln, start: from, end: to });
      if (next || isTableRow(masked[mk.line]) || to < text.length) break;
      ln += 1;
      if (ln >= masked.length || startsBlock(masked[ln])) break;
      from = 0;
    }
    mk.segments = segments;
    mk.text = segments.map((s) => prose[s.line].slice(s.start, s.end)).join(' ');
  }
  return markers;
}

function insideSegments(segments, line, col) {
  return segments.some((s) => s.line === line && col >= s.start && col < s.end);
}

/**
 * The caveat, or a link to an anchor whose next lines hold it, within the window after the marker.
 * Both are read as a reader sees them: a caveat or a link inside an HTML comment does not count.
 */
function checkCaveat(claim, mk, file, lines, visible, prose, files, read) {
  const pattern = caveatPattern(claim.caveat);
  const last = Math.min(lines.length - 1, mk.line + CAVEAT_WINDOW_LINES);
  const windowText = [visible[mk.line].slice(mk.end), ...visible.slice(mk.line + 1, last + 1)].join(' ');
  if (pattern.test(normalize(windowText))) return true;
  for (let ln = mk.line; ln <= last; ln += 1) {
    const from = ln === mk.line ? mk.end : 0;
    for (const m of prose[ln].slice(from).matchAll(LINK_RE)) {
      const [target, anchor] = m[1].split('#');
      const path = target ? posix.normalize(posix.join(posix.dirname(file), target)) : file;
      let targetLines = path === file ? lines : null;
      if (!targetLines) {
        const raw = files.has(path) ? files.get(path) : read(path)?.toString('utf8');
        if (raw === undefined || raw === null) continue;
        targetLines = raw.split(/\r?\n/);
      }
      let name;
      try {
        name = decodeURIComponent(anchor);
      } catch {
        continue;
      }
      const at = anchorLine(targetLines, name);
      if (at < 0) continue;
      const near = visibleText(targetLines).slice(at, at + CAVEAT_WINDOW_LINES + 1).join(' ');
      if (pattern.test(normalize(near))) return true;
    }
  }
  return false;
}

/**
 * Check the scanned files against the contracts. Pure: every input is passed in.
 *
 *   files     Map of repo-relative path -> text (README.md and docs/**\/*.md)
 *   claims    parsed value-claims.v1.json;  measured  parsed measured.json
 *   compare   parsed compare.v1.json (launch only); specText  docs/v2/SPEC.md text or null
 *   read      (path) => Buffer | null, for anchors in other files, load runs and launch files
 *   today     YYYY-MM-DD;  launch  run the launch checks
 *
 * Returns { problems, markers, blocks, files, rewrites } where rewrites maps each file whose measured
 * blocks are stale to its regenerated text.
 */
export function checkClaims({ files, claims, measured, compare = null, specText = null, read = () => null, loadRuns = [], today, launch = false }) {
  const problems = [];
  const report = (level, rule, file, line, message) => problems.push({ level, rule, file, line, message });
  const byId = new Map(claims.claims.map((c) => [c.id, c]));
  const like = byId.get(LIKE_FOR_LIKE_ID) ?? null;
  const block = renderMeasuredBlock(claims, measured, read, loadRuns);
  const blockRule = (p) => {
    if (p.includes('sources.gate_latency')) return 'latency-conditions';
    return p.includes('(D229)') ? 'cost-conditions' : 'load-run-missing';
  };
  for (const p of block.problems) report('error', blockRule(p), MEASURED_PATH, null, p);
  const pkce = pkceStatus(specText);
  const rewrites = new Map();
  let markerCount = 0;
  let blockCount = 0;

  for (const [file, text] of files) {
    const lines = text.split(/\r?\n/);
    const masked = maskCode(lines);
    const prose = maskComments(masked);
    const visible = visibleText(lines);
    const markers = findMarkers(masked, prose);
    markerCount += markers.length;
    const measuredSegments = [];

    for (const mk of markers) {
      const line = mk.line + 1;
      const claim = byId.get(mk.id);
      if (!claim) {
        report('error', 'unknown-claim', file, line, `claim "${mk.id}" is not in ${CLAIMS_PATH}`);
        continue;
      }
      if (!claim.surfaces.some((s) => SURFACES.includes(s))) {
        report('error', 'wrong-surface', file, line,
          `claim ${claim.id} is written for ${claim.surfaces.join(', ')}, not the readme or docs; add the surface in ${CLAIMS_PATH} first`);
      }
      if (claim.label === 'measured') measuredSegments.push(...mk.segments);
      const expected = expectedDisplay(claim, measured);
      if (expected.problem) report('error', 'measured-mock', file, line, expected.problem);
      const said = normalize(mk.text);
      mk.expected = normalize(expected.text);
      const at = said.indexOf(mk.expected);
      if (at < 0) {
        report('error', 'display-missing', file, line,
          `claim ${claim.id}: the text after the marker must say "${expected.text}"` +
            (claim.label === 'measured' ? ` (from ${MEASURED_PATH}; measured blocks: ${WRITE_HINT})` : ''));
      } else if (expected.empty) {
        // An absent value is not a measurement: "not yet measured" goes without the [measured] chip.
        const after = said.slice(at + mk.expected.length);
        const chip = [...after.matchAll(CHIP_RE)][0];
        if (chip && chip[1].toLowerCase() === LABEL_CHIPS.measured) {
          report('error', 'empty-measured-chip', file, line,
            `claim ${claim.id} has no measured value: "${expected.text}" goes without the [measured] chip (${WRITE_HINT} for a measured block)`);
        }
      } else {
        const want = LABEL_CHIPS[claim.label];
        const after = said.slice(at + mk.expected.length);
        const chip = [...after.matchAll(CHIP_RE)][0];
        if (!chip) {
          report('error', 'chip-missing', file, line, `claim ${claim.id}: "${expected.text}" needs its label chip [${want}] after it`);
        } else if (chip[1].toLowerCase().replace(' ', '-') !== want) {
          report('error', 'chip-mismatch', file, line,
            `claim ${claim.id} is labelled ${claim.label}: its chip is [${want}], not [${chip[1]}]`);
        }
      }
      // A caveat filled from the run ({gate_conditions}, {cost_conditions}) exists only once the
      // value is measured.
      const runCaveat = /\{(gate|cost)_conditions\}/.test(claim.caveat ?? '') && expected.empty;
      if (claim.caveat && !runCaveat && !checkCaveat(claim, mk, file, lines, visible, prose, files, read)) {
        report('error', 'caveat-missing', file, line,
          `claim ${claim.id}: its caveat ("${claim.caveat}") or a link to an anchor holding it must follow within ${CAVEAT_WINDOW_LINES} lines`);
      }
    }

    // Measured blocks.
    const starts = [];
    const ends = [];
    masked.forEach((l, i) => {
      const m = BLOCK_RE.exec(l);
      if (m) (m[1] === 'start' ? starts : ends).push(i);
    });
    let out = null;
    let paired = starts.length === ends.length;
    for (let k = 0; paired && k < starts.length; k += 1) {
      if (!(starts[k] < ends[k] && (k === 0 || ends[k - 1] < starts[k]))) paired = false;
    }
    // A generated block's figures come from the contract and are held by [block-stale].
    const inBlock = (i) => paired && starts.some((s, k) => i > s && i < ends[k]);
    if (!paired) {
      report('error', 'block-unpaired', file, (starts[0] ?? ends[0]) + 1,
        'every <!-- measured:start --> needs its own <!-- measured:end --> after it, with no nesting');
    } else {
      blockCount += starts.length;
      out = [...lines];
      for (let k = starts.length - 1; k >= 0; k -= 1) {
        const current = lines.slice(starts[k] + 1, ends[k]);
        if (current.join('\n') !== block.lines.join('\n')) {
          report('error', 'block-stale', file, starts[k] + 1, `the measured block differs from ${MEASURED_PATH}: ${WRITE_HINT}`);
          out.splice(starts[k] + 1, ends[k] - starts[k] - 1, ...block.lines);
        }
      }
      const eol = text.includes('\r\n') ? '\r\n' : '\n';
      const rewritten = out.join(eol);
      if (rewritten !== text) rewrites.set(file, rewritten);
    }

    if (!isPublished(file)) continue;

    // A [measured] chip belongs to a measured claim's text only.
    prose.forEach((l, i) => {
      for (const m of l.matchAll(MEASURED_CHIP_RE)) {
        if (!insideSegments(measuredSegments, i, m.index)) {
          report('error', 'stray-measured-chip', file, i + 1,
            'a [measured] chip outside a measured claim: measured figures come only from contracts/claims/measured.json through a <!-- claim:ID --> marker');
        }
      }
      for (const m of l.matchAll(PLACEHOLDER_RE)) {
        if (!measured?.values || !Object.hasOwn(measured.values, m[1])) {
          report('error', 'measured-placeholder', file, i + 1, `{measured:${m[1]}} names no value in ${MEASURED_PATH}`);
        }
      }
    });

    // Every figure in a launch draft carries a claim marker: it sits in the text after a marker and
    // is part of that claim's display, so a second, unmarked number in the same sentence still fails.
    if (file.startsWith('docs/launch/')) {
      prose.forEach((l, i) => {
        if (inBlock(i)) return;
        // Only a {measured:KEY} placeholder stands in for a figure; any other braces hide nothing.
        const placeholders = [...l.matchAll(PLACEHOLDER_RE)].map((m) => [m.index, m.index + m[0].length]);
        for (const m of l.matchAll(FIGURE_RE)) {
          const figure = normalize(m[0]);
          if (markers.some((mk) => mk.expected?.includes(figure) && insideSegments(mk.segments, i, m.index))) continue;
          if (placeholders.some(([a, b]) => m.index >= a && m.index < b)) continue;
          report('error', 'launch-figure', file, i + 1,
            `"${m[0].trim()}" needs a <!-- claim:ID --> marker in front of it (or a {measured:KEY} placeholder): every figure in a launch draft is a contract claim`);
        }
      });
    }

    for (const hit of pkceWordingHits(prose, pkce)) {
      report('error', 'pkce-wording', file, hit.line,
        `"${hit.sentence.slice(0, 120)}" calls OpenRouter PKCE verified, but ${SPEC_PATH} Appendix D records no dated verification for it`);
    }

    // D212: our agreement is read beside the like-for-like third-party figure (accuracy at a 0.5
    // threshold), never alone and never only beside the tuned-threshold one.
    if (like) {
      for (const section of sections(masked)) {
        const agreed = markers.filter((mk) => mk.line >= section.from && mk.line < section.to)
          .map((mk) => byId.get(mk.id))
          .filter((c) => c && c.label === 'measured' && AGREEMENT_REFS.includes(c.measured_ref));
        if (!agreed.length) continue;
        const text = normalize(visible.slice(section.from, section.to).join(' '));
        if (text.includes(normalize(like.display))) continue;
        const shown = agreed.filter((c) => shownValue(c, measured) !== null);
        if (!launch && !shown.length) continue;
        report(launch ? 'error' : 'warn', 'agreement-alone', file, section.from + 1,
          `${agreed.map((c) => c.id).join(', ')} must be shown beside ${like.id} ("${like.display}"), the like-for-like ` +
            'figure at default thresholds, in the same section (D212)');
      }
    }
  }

  // D212: an estimate that a measured value contradicts is updated before launch.
  for (const c of claims.claims) {
    if (!c.measured_pair) continue;
    const pair = byId.get(c.measured_pair);
    if (!pair || pair.label !== 'measured') {
      report('error', 'pair-unknown', CLAIMS_PATH, null, `${c.id}: measured_pair ${c.measured_pair} is not a measured claim`);
      continue;
    }
    const value = shownValue(pair, measured);
    if (value === null || typeof c.value !== 'number' || c.value <= 0 || value <= 0) continue;
    const ratio = Math.max(c.value, value) / Math.min(c.value, value);
    if (ratio > PAIR_MAX_RATIO) {
      report(launch ? 'error' : 'warn', 'pair-divergence', CLAIMS_PATH, null,
        `${c.id} (${c.display}) and its measured pair ${pair.id} (${formatValue(value, pair.unit)}) differ by ` +
          `${Math.round(ratio * 10) / 10}×, over ${PAIR_MAX_RATIO}×: update the estimate's figure or wording, or take it off the surfaces that show the measured value`);
    }
  }

  for (const c of claims.claims) {
    if (c.reverify && !launch) report('warn', 'reverify-open', CLAIMS_PATH, null, `${c.id}: re-check before launch: ${c.reverify}`);
  }

  // Third-party figures go stale.
  const now = Date.parse(`${today}T00:00:00Z`);
  for (const c of claims.claims) {
    if (c.label !== 'third_party') continue;
    if (!c.as_of) {
      report('warn', 'third-party-stale', CLAIMS_PATH, null, `${c.id}: a third-party claim without as_of cannot be dated`);
      continue;
    }
    const age = Math.floor((now - Date.parse(`${c.as_of}T00:00:00Z`)) / 86_400_000);
    if (age > THIRD_PARTY_MAX_AGE_DAYS) {
      report('warn', 'third-party-stale', CLAIMS_PATH, null,
        `${c.id}: as_of ${c.as_of} is ${age} days old (over ${THIRD_PARTY_MAX_AGE_DAYS}); re-check ${c.source_url} and update the claim`);
    }
  }

  if (launch) problems.push(...launchChecks({ claims, measured, compare, read }));
  return { problems, markers: markerCount, blocks: blockCount, files: files.size, rewrites };
}

/** PNG width and height from the IHDR chunk, or null when the bytes are not a PNG. */
export function pngSize(buf) {
  const sig = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  if (!buf || buf.length < 24 || !buf.subarray(0, 8).equals(sig) || buf.toString('latin1', 12, 16) !== 'IHDR') return null;
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

/** The vendor hosts of the contract (common.v1 VendorHost), or null when the schema cannot be read. */
export function vendorHosts(read) {
  const raw = read(COMMON_SCHEMA_PATH);
  if (raw === null) return null;
  const hosts = parseJson(raw).value?.$defs?.VendorHost?.enum;
  return Array.isArray(hosts) && hosts.length && hosts.every((h) => typeof h === 'string') ? hosts : null;
}

/**
 * D203: a tape is recorded only when the answers came from the vendors. Its kind alone is a word
 * the recorder wrote; the hosts it names are the evidence. The fast check's and the judge's host
 * must be vendor hosts, and so must the upstream that wrote any generated answers. A tape that
 * names no host, or mock-upstream, a proxy or a gateway, is simulated whatever its kind says.
 */
export function tapeHostProblems(tape, hosts) {
  if (!hosts) return [`${COMMON_SCHEMA_PATH} gives no VendorHost list, so the tape's hosts cannot be checked (D203)`];
  const out = [];
  const need = (what, host) => {
    if (typeof host !== 'string' || !hosts.includes(host)) {
      out.push(`the tape names ${what} host ${host ? `"${host}"` : 'none'}, not a vendor host (${hosts.join(', ')}): ` +
        'a tape from mock-upstream or a non-vendor host is simulated, never launched as a recording (D203)');
    }
  };
  need('the fast check', tape?.decision_model?.host);
  need('the judge', tape?.judge?.host);
  const generated = tape?.answer_sources?.generated;
  if (typeof generated === 'number' && generated > 0) need('the generated answers\'', tape?.answer_sources?.generated_host);
  return out;
}

/** The snapshots a measured.json names: [] when none, one, or both of a split run (D207). */
function measuredSnapshots(measured) {
  const snap = measured?.snapshot;
  if (Array.isArray(snap)) return snap.filter((s) => typeof s === 'string' && s);
  return typeof snap === 'string' && snap ? [snap] : [];
}

function parseJson(buf) {
  try {
    return { value: JSON.parse(buf.toString('utf8')), error: null };
  } catch (err) {
    return { value: undefined, error: err.message };
  }
}

/**
 * --launch: nothing goes public on simulated or unreviewed material. Refuses:
 *   - a missing or mock recorded dataset, one whose fast-check, judge or generated-answer host is
 *     not a vendor host (D203), or one stamped with another version than VERSION, or recorded on
 *     another snapshot than the measured run (D207, D216);
 *   - a not_measured measured.json, a bench run of it that failed more than 5% of its examples or
 *     whose file is missing, no snapshot, or two snapshots without snapshot_split (D204, D207);
 *   - a measured.json without the held-out ECE before and after calibration, unless the tape says
 *     it was recorded without a measured calibration (D206);
 *   - any comparison cell that is unverified or not reviewed by the owner;
 *   - a missing or malformed og.png;
 *   - a claim that still says what must be re-checked (reverify, D212);
 *   - the owner's name still a placeholder in the licence or the package metadata (D216).
 */
export function launchChecks({ claims = { claims: [] }, measured, compare, read }) {
  const problems = [];
  const report = (rule, file, message) => problems.push({ level: 'error', rule, file, line: null, message });

  let recorded = null;
  const manifest = read(RECORDED_MANIFEST_PATH);
  if (manifest === null) {
    report('launch-recorded', RECORDED_MANIFEST_PATH, 'no recorded dataset: run `make record-demo` (the site would replay simulated traffic)');
  } else {
    const parsed = parseJson(manifest);
    if (parsed.error) report('launch-recorded', RECORDED_MANIFEST_PATH, `not valid JSON: ${parsed.error}`);
    // Only an explicit kind "recorded" launches; a manifest without one is not evidence of a recording.
    else if (parsed.value?.kind !== 'recorded') {
      const v = parsed.value;
      const kind = v !== null && typeof v === 'object' && 'kind' in v ? `"${v.kind}"` : 'missing';
      report('launch-recorded', RECORDED_MANIFEST_PATH, `kind is ${kind}, not "recorded": a simulated tape is never launched as a recording`);
    } else {
      recorded = parsed.value;
    }
  }

  if (recorded) {
    const version = read(VERSION_PATH)?.toString('utf8').trim() ?? null;
    if (!version) report('launch-version', VERSION_PATH, 'missing: the recording cannot be checked against the version');
    else if (recorded.trustagent_version !== version) {
      report('launch-version', RECORDED_MANIFEST_PATH,
        `the tape says trustagent_version ${recorded.trustagent_version ?? 'missing'}, VERSION says ${version}: record after the version bump (D216)`);
    }
    for (const message of tapeHostProblems(recorded, vendorHosts(read))) report('launch-recorded', RECORDED_MANIFEST_PATH, message);
  }

  if (!isMeasuredRun(measured)) {
    report('launch-not-measured', MEASURED_PATH,
      `status is ${measured?.status ?? 'missing'}: publish a measured run first (make bench-lite, then make bench-publish)`);
  } else {
    // D204: every bench run behind the figures failed at most 5% of its examples.
    for (const runId of measured.runs ?? []) {
      const bench = read(`${RESULTS_DIR}/${runId}.json`);
      if (bench === null) {
        if (read(`${LOAD_RUNS_DIR}/${runId}.json`) === null) {
          report('launch-run-missing', MEASURED_PATH, `run ${runId} has no file in ${RESULTS_DIR} or ${LOAD_RUNS_DIR}`);
        }
        continue;
      }
      const path = `${RESULTS_DIR}/${runId}.json`;
      const run = parseJson(bench);
      const q = run.value?.quality;
      if (run.error) report('launch-quality', path, `not valid JSON: ${run.error}`);
      else if (!q || typeof q.failure_rate !== 'number' || typeof q.n !== 'number' || typeof q.scored !== 'number') {
        report('launch-quality', path, 'no quality {n, scored, failure_rate}: a published run states how many examples failed (D204)');
      } else {
        if (q.failure_rate > MAX_FAILURE_RATE) {
          report('launch-quality', path, `failure_rate ${q.failure_rate} is over ${MAX_FAILURE_RATE} (D204)`);
        }
        if (q.scored < 0.95 * q.n) report('launch-quality', path, `scored ${q.scored} of ${q.n}, under 95% (D204)`);
      }
    }
    // D207: one snapshot, or two with snapshot_split, shared with the recording.
    const snaps = measuredSnapshots(measured);
    const split = measured.sources?.snapshot_split === true;
    if (!snaps.length) report('launch-snapshot', MEASURED_PATH, 'names no decision-model snapshot (D207)');
    else if (snaps.length > 1 && !split) {
      report('launch-snapshot', MEASURED_PATH, `names two snapshots (${snaps.join(', ')}) without sources.snapshot_split: true (D207)`);
    } else if (snaps.length === 1 && split) {
      report('launch-snapshot', MEASURED_PATH, 'sources.snapshot_split is true but only one snapshot is named: name both (D207)');
    }
    const tape = recorded?.decision_model?.snapshot;
    if (recorded && snaps.length && !snaps.includes(tape)) {
      report('launch-snapshot', RECORDED_MANIFEST_PATH,
        `the tape was recorded on snapshot ${tape ?? 'missing'}, the measured run on ${snaps.join(' and ')}: they must match (D207)`);
    }
    // D206: the held-out ECE before and after calibration is published with its method, unless
    // the tape says it was recorded without a measured calibration (ALLOW_UNCALIBRATED=1), when
    // there is no measured calibration to hold out.
    const ece = (key) => measured.values?.[key]?.value;
    const heldOut = typeof ece('groundedness_ece_before') === 'number' && typeof ece('groundedness_ece_after') === 'number' &&
      measured.sources?.groundedness_ece?.method === 'holdout';
    const quality = recorded?.calibration?.quality;
    if (!heldOut && !(typeof quality === 'string' && quality !== 'measured')) {
      report('launch-ece', MEASURED_PATH,
        'the held-out ECE before and after calibration is not published: run `make calib-holdout RUN=<abs run.json>`, ' +
          'then `make bench-publish RUN=<abs run.json> HOLDOUT=<the holdout-groundedness.json beside it>` (D206)');
    }
  }

  for (const c of claims.claims ?? []) {
    if (c.reverify) report('launch-reverify', CLAIMS_PATH, `${c.id} must be re-checked first: ${c.reverify}`);
  }

  for (const path of OWNER_FILES) {
    if (read(path)?.toString('utf8').includes(OWNER_PLACEHOLDER)) {
      report('launch-license', path, `still says ${OWNER_PLACEHOLDER}: write the owner's legal name (D216)`);
    }
  }

  if (!compare) {
    report('launch-compare', COMPARE_PATH, 'the comparison table is missing');
  } else {
    const open = [];
    let unverified = 0;
    let unreviewed = 0;
    let total = 0;
    for (const row of compare.rows ?? []) {
      for (const [col, cell] of Object.entries(row.cells ?? {})) {
        total += 1;
        const bad = cell.value === 'unverified' || cell.reviewed !== true;
        if (cell.value === 'unverified') unverified += 1;
        else if (cell.reviewed !== true) unreviewed += 1;
        if (bad) open.push(`${row.id}.${col}`);
      }
    }
    if (open.length) {
      const more = open.length > 8 ? `, and ${open.length - 8} more` : '';
      report('launch-compare', COMPARE_PATH,
        `${open.length} of ${total} cells are not ready (${unverified} unverified, ${unreviewed} not reviewed by the owner): ` +
          `${open.slice(0, 8).join(', ')}${more}`);
    }
  }

  for (const path of OG_PATHS) {
    const buf = read(path);
    if (buf === null) {
      report('launch-og', path, 'missing: run `make og` on a recorded dataset');
      continue;
    }
    const size = pngSize(buf);
    if (!size) report('launch-og', path, 'not a PNG');
    else if (size.width !== OG_WIDTH || size.height !== OG_HEIGHT) {
      report('launch-og', path, `${size.width}×${size.height}, not ${OG_WIDTH}×${OG_HEIGHT}`);
    }
    if (buf.length >= OG_MAX_BYTES) report('launch-og', path, `${buf.length} bytes, not under ${OG_MAX_BYTES}`);
  }
  return problems;
}

// ---------------------------------------------------------------------------------------------
// Files

/** README.md and every docs/**\/*.md under `root`, sorted, as repo-relative "/" paths. */
export function scanList(root) {
  const out = [];
  if (existsSync(join(root, 'README.md'))) out.push('README.md');
  const walk = (rel) => {
    let entries;
    try {
      entries = readdirSync(join(root, rel), { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of entries) {
      const path = `${rel}/${e.name}`;
      if (e.isDirectory()) walk(path);
      else if (e.isFile() && e.name.endsWith('.md')) out.push(path);
    }
  };
  walk('docs');
  return out.sort();
}

function readJson(root, rel, required) {
  const full = join(root, rel);
  if (!existsSync(full)) {
    if (required) throw new ConfigError(`${rel}: not found`);
    return null;
  }
  try {
    return JSON.parse(readFileSync(full, 'utf8'));
  } catch (err) {
    throw new ConfigError(`${rel}: invalid JSON (${err.message})`);
  }
}

function formatProblem(p) {
  return `${p.file}${p.line ? `:${p.line}` : ''}: ${p.level} [${p.rule}] ${p.message}`;
}

function annotation(p) {
  const esc = (s) => String(s).replace(/%/g, '%25').replace(/\r/g, '%0D').replace(/\n/g, '%0A');
  const where = `file=${p.file}${p.line ? `,line=${p.line}` : ''}`;
  return `::${p.level === 'warn' ? 'warning' : 'error'} ${where}::${esc(`[${p.rule}] ${p.message}`)}`;
}

/** Load everything from `root`, check (and with `write`, regenerate blocks first), print, exit code. */
export function run({ root, write = false, launch = false, today, log = console.log, env = process.env }) {
  const claims = readJson(root, CLAIMS_PATH, true);
  const measured = readJson(root, MEASURED_PATH, true);
  const compare = launch ? readJson(root, COMPARE_PATH, false) : null;
  const specFile = join(root, SPEC_PATH);
  const specText = existsSync(specFile) ? readFileSync(specFile, 'utf8') : null;
  const read = (rel) => {
    const full = join(root, rel);
    if (!full.startsWith(join(root, '/')) || !existsSync(full)) return null;
    try {
      return readFileSync(full);
    } catch {
      return null;
    }
  };
  const load = () => new Map(scanList(root).map((p) => [p, readFileSync(join(root, p), 'utf8')]));
  const loadDir = join(root, LOAD_RUNS_DIR);
  const loadRuns = existsSync(loadDir) ? readdirSync(loadDir).filter((f) => f.endsWith('.json')).sort() : [];
  const check = () => checkClaims({ files: load(), claims, measured, compare, specText, read, loadRuns, today, launch });

  let result = check();
  if (write && result.rewrites.size) {
    for (const [path, text] of result.rewrites) {
      writeFileSync(join(root, path), text);
      log(`check-claims: regenerated the measured block(s) in ${path}`);
    }
    result = check();
  }
  const errors = result.problems.filter((p) => p.level === 'error');
  const warnings = result.problems.length - errors.length;
  for (const p of result.problems) {
    log(formatProblem(p));
    if (env.GITHUB_ACTIONS === 'true') log(annotation(p));
  }
  if (!specText) log(`check-claims: ${SPEC_PATH} not found; PKCE wording is held to "unverified".`);
  log(
    `check-claims: ${result.markers} claim marker(s) and ${result.blocks} measured block(s) in ${result.files} file(s)` +
      `${launch ? ', launch checks on' : ''}: ${errors.length} error(s), ${warnings} warning(s).`,
  );
  return errors.length ? 1 : 0;
}

// ---------------------------------------------------------------------------------------------
// Self-test (--self-test): one fixture per rule against checkClaims, then an end-to-end run()
// over a throwaway directory (loading, --write, --launch and the exit codes).

const TODAY = '2026-10-09';

const FIXTURE_CLAIMS = {
  version: '1',
  as_of: TODAY,
  claims: [
    { id: 'est_cost', text: 'Fast check', value: 0.21, unit: 'usd_per_1k', display: '~$0.21 / 1,000 answers',
      label: 'estimate', basis: 'b', source_url: null, as_of: TODAY, caveat: null, measured_pair: 'meas_cost',
      surfaces: ['landing', 'readme', 'docs'] },
    { id: 'vendor_price', text: 'Jev price', value: 0.042, unit: 'usd_per_mtok', display: '$0.042 / M input tokens',
      label: 'vendor', basis: 'b', source_url: 'https://example.com/p', as_of: TODAY, caveat: null, surfaces: ['readme'] },
    { id: 'tp_latency', text: 'Jev p50', value: 0.23, unit: 's', display: 'P50 0.23 s on OpenRouter', label: 'third_party',
      basis: 'b', source_url: 'https://example.com/l', as_of: '2026-03-01', caveat: 'Latency depends on region and load.',
      surfaces: ['readme', 'docs'] },
    { id: 'tp_fresh', text: 'Arize', value: 0.87, unit: 'fraction', display: '87%', label: 'third_party', basis: 'b',
      source_url: 'https://example.com/a', as_of: '2026-09-27', caveat: null, surfaces: ['docs'] },
    { id: 'landing_only', text: 'Hero', value: 212, unit: 'ms', display: '212 ms', label: 'simulated', basis: 'b',
      source_url: null, as_of: null, caveat: 'Replayed illustration.', surfaces: ['landing'] },
    { id: 'meas_cost', text: 'Our fast-check cost per 1,000 answers', value: null, unit: 'usd_per_1k',
      display: '${value} / 1,000 answers', empty_display: 'not yet measured', label: 'measured', basis: 'b',
      source_url: null, as_of: null, caveat: 'The fast check alone, on {cost_conditions}.', measured_ref: 'fast_check_usd_per_1k', surfaces: ['readme'] },
    { id: 'meas_overhead', text: 'Gateway overhead, p50', value: null, unit: 'ms', display: '{value} ms p50',
      empty_display: 'not yet measured', label: 'measured', basis: 'b', source_url: null, as_of: null,
      caveat: "Gateway's own time with simulated vendors; {machine}", measured_ref: 'proxy_overhead_ms_p50',
      surfaces: ['readme', 'docs'] },
    { id: 'meas_latency', text: 'Fast-check latency, p50', value: null, unit: 'ms', display: '{value} ms p50',
      empty_display: 'not yet measured', label: 'measured', basis: 'b', source_url: null, as_of: null,
      caveat: 'The fast check\'s own time per answer: {gate_conditions}.', measured_ref: 'gate_latency_ms_p50', surfaces: ['readme'] },
    { id: 'meas_agree', text: 'Our RAGTruth-QA agreement', value: null, unit: 'fraction', display: '{value}',
      empty_display: 'not yet measured', label: 'measured', basis: 'b', source_url: null, as_of: null, caveat: null,
      measured_ref: 'ragtruth_qa_agreement', surfaces: ['readme', 'docs'] },
    { id: LIKE_FOR_LIKE_ID, text: 'Arize RAGTruth test: Jev accuracy at a 0.5 threshold', value: 0.76, unit: 'fraction',
      display: '76% at 0.5', label: 'third_party', basis: 'b', source_url: 'https://example.com/a', as_of: TODAY,
      caveat: 'Thresholds need calibrating.', surfaces: ['landing', 'readme', 'docs'] },
  ],
};

const NOT_MEASURED = {
  schema: 'trustagent.measured/v1', status: 'not_measured', tier: null, mock: false, generated_by: null, generated_at: null,
  runs: [],
  values: {
    fast_check_usd_per_1k: { value: null, unit: 'usd_per_1k', description: 'd', run_id: null, system: 'gate' },
    proxy_overhead_ms_p50: { value: null, unit: 'ms', description: 'd', run_id: null, system: 'proxy' },
    gate_latency_ms_p50: { value: null, unit: 'ms', description: 'd', run_id: null, system: 'gate' },
    ragtruth_qa_agreement: { value: null, unit: 'fraction', description: 'd', run_id: null, system: 'gate' },
  },
};

const SNAPSHOT = 'jev-1.13-20260917';

const MEASURED = {
  ...NOT_MEASURED,
  status: 'measured',
  tier: 'lite',
  generated_by: 'bench-publish',
  generated_at: '2026-11-02T10:00:00Z',
  runs: ['bench-1', 'load-1'],
  snapshot: SNAPSHOT,
  values: {
    fast_check_usd_per_1k: { ...NOT_MEASURED.values.fast_check_usd_per_1k, value: 0.2134, run_id: 'bench-1' },
    proxy_overhead_ms_p50: { ...NOT_MEASURED.values.proxy_overhead_ms_p50, value: 1.234, run_id: 'load-1' },
    gate_latency_ms_p50: { ...NOT_MEASURED.values.gate_latency_ms_p50, value: 212.4, run_id: 'bench-1' },
    ragtruth_qa_agreement: { ...NOT_MEASURED.values.ragtruth_qa_agreement, value: 0.8125, run_id: 'bench-1' },
    groundedness_ece_before: { value: 0.21, unit: 'fraction', description: 'd', run_id: 'bench-1', system: 'gate' },
    groundedness_ece_after: { value: 0.04, unit: 'fraction', description: 'd', run_id: 'bench-1', system: 'gate' },
  },
  intervals: {
    ragtruth_qa_agreement: { lo: 0.75, hi: 0.86, method: 'bootstrap', resamples: 2000, seed: 13, n_pos: 40, n_neg: 160 },
  },
  sources: {
    fast_check_usd_per_1k: {
      cost_source: 'reported', dataset: 'RAGTruth-QA test split', n: 200, input_tokens_per_answer: 640,
      conditions: 'RAGTruth-QA test split, n=200, about 640 input tokens per answer',
    },
    groundedness_ece: {
      method: 'holdout', calibration_file: `config/calibration/${SNAPSHOT}.json`, calibration_sha256: 'a'.repeat(64), dataset: 'ragtruth:qa-train@c103204',
    },
    gate_latency: {
      connection: 'warm', gate_concurrency: 1, platform: 'Linux', region: 'cloud us-east', n: 199,
      conditions: `warm connection, one request at a time, run on Linux, region cloud us-east, snapshot ${'jev-1.13-20260917'}, n=199`,
    },
  },
};

const LOAD_RUN = { environment: { cpu_model: 'Test CPU', cores: 10, mem_gb: 8, os: 'Linux' } };
const BENCH_RUN = (quality) => JSON.stringify({ run_id: 'bench-1', quality });
const GOOD_QUALITY = { n: 200, scored: 198, failed: 2, excluded: 0, failure_rate: 0.01 };

const SPEC_UNVERIFIED = [
  '## Appendix D: assumptions to verify (gate)',
  '',
  '| Assumption | Verified at |',
  '|---|---|',
  '| OpenRouter PKCE accepts `localhost` callbacks | still unverified against the real openrouter.ai |',
  '| OpenRouter PKCE accepts the Pages callback | the owner, when Pages exists |',
  '',
  '## Appendix E: errata',
].join('\n');

const SPEC_VERIFIED = SPEC_UNVERIFIED
  .replace('still unverified against the real openrouter.ai', 'Verified 2026-11-01 against openrouter.ai')
  .replace('the owner, when Pages exists', 'Verified 2026-11-03 from the Pages origin');

const REVIEWED_CELL = { value: 'yes', text: 't', source_url: 'https://example.com/c', as_of: TODAY, reviewed: true };
const COMPARE = (cell) => ({
  schema: 'trustagent.compare/v1',
  columns: [{ id: 'inline', label: 'Inline' }, { id: 'cost', label: 'Cost' }],
  rows: [{ id: 'judges', name: 'Judges', examples: 'x', cells: { inline: REVIEWED_CELL, cost: cell } }],
});

function png(width, height, extra = 0) {
  const buf = Buffer.alloc(33 + extra);
  Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]).copy(buf, 0);
  buf.writeUInt32BE(13, 8);
  buf.write('IHDR', 12, 'latin1');
  buf.writeUInt32BE(width, 16);
  buf.writeUInt32BE(height, 20);
  return buf;
}

const RECORDED_TAPE = {
  schema: 'trustagent.recording/v1', kind: 'recorded', trustagent_version: '2.0.0',
  decision_model: { backend: 'openrouter-decisions', snapshot: SNAPSHOT, host: 'openrouter.ai' },
  judge: { provider: 'openrouter', model: 'anthropic/claude-haiku', fallback_active: false, host: 'openrouter.ai' },
  answer_sources: { scripted: 10, generated: 2, generated_host: 'openrouter.ai' },
};
const RECORDED = Buffer.from(JSON.stringify(RECORDED_TAPE));
const VENDOR_HOSTS = ['openrouter.ai', 'api.typesafe.ai', 'api.anthropic.com'];
const COMMON_SCHEMA = JSON.stringify({ $defs: { VendorHost: { enum: VENDOR_HOSTS } } });
/** Every file a launch-ready repository holds besides the docs (measured: MEASURED). */
const LAUNCH_READY = {
  [COMMON_SCHEMA_PATH]: COMMON_SCHEMA,
  'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN),
  'docs/benchmarks/results/bench-1.json': BENCH_RUN(GOOD_QUALITY),
  [RECORDED_MANIFEST_PATH]: RECORDED,
  VERSION: '2.0.0\n',
  LICENSE: 'MIT License\n\nCopyright (c) 2026 Jane Doe\n',
  'docs/launch/og.png': png(OG_WIDTH, OG_HEIGHT),
  'frontend-react/public/og.png': png(OG_WIDTH, OG_HEIGHT),
};
const MOCK_RECORDING = Buffer.from(JSON.stringify({ schema: 'trustagent.recording/v1', kind: 'mock' }));

/** Each case: files, optional overrides, the exact sorted set of rule ids it must report. */
const CASES = [
  {
    name: 'valid markers pass (estimate, vendor, third-party with its caveat)',
    files: {
      'README.md': [
        '- Fast check: <!-- claim:est_cost --> **~$0.21 / 1,000 answers** [estimate]',
        '- Price: <!-- claim:vendor_price --> $0.042 / M input tokens [vendor]',
        '- Latency: <!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party].',
        '  Latency depends on region and load.',
      ].join('\n'),
    },
    expect: [],
  },
  {
    name: 'a fenced block and inline code are not read',
    files: {
      'docs/GUIDE.md': ['Write `<!-- claim:nope -->` before a figure; `[measured]` is a chip.', '```', '<!-- claim:nope --> x', '```'].join('\n'),
    },
    expect: [],
  },
  { name: 'unknown id', files: { 'README.md': '<!-- claim:nope --> 12 [estimate]' }, expect: ['unknown-claim'] },
  {
    name: 'a claim for the landing only',
    files: { 'README.md': 'Hero: <!-- claim:landing_only --> 212 ms [simulated]. Replayed illustration.' },
    expect: ['wrong-surface'],
  },
  { name: 'display drifted', files: { 'README.md': '<!-- claim:est_cost --> ~$0.25 / 1,000 answers [estimate]' }, expect: ['display-missing'] },
  { name: 'no chip', files: { 'README.md': '<!-- claim:est_cost --> ~$0.21 / 1,000 answers' }, expect: ['chip-missing'] },
  { name: 'chip before the display does not count', files: { 'README.md': '<!-- claim:est_cost --> [estimate] ~$0.21 / 1,000 answers' }, expect: ['chip-missing'] },
  { name: 'wrong chip', files: { 'README.md': '<!-- claim:est_cost --> ~$0.21 / 1,000 answers [vendor] [estimate]' }, expect: ['chip-mismatch'] },
  {
    name: 'a second marker on the line ends the first claim',
    files: { 'README.md': '<!-- claim:est_cost --> about a fifth of a dollar, <!-- claim:vendor_price --> $0.042 / M input tokens [vendor] ~$0.21 / 1,000 answers [estimate]' },
    expect: ['display-missing'],
  },
  {
    name: 'a table cell ends the claim',
    files: { 'README.md': '| Fast check | <!-- claim:est_cost --> ~$0.21 / 1,000 answers | [estimate] |' },
    expect: ['chip-missing'],
  },
  {
    name: 'a wrapped list item continues the claim; a new item ends it',
    files: { 'README.md': ['- Fast check: <!-- claim:est_cost --> ~$0.21 / 1,000', '  answers [estimate]', '- <!-- claim:vendor_price --> $0.042 / M input tokens', '- [vendor]'].join('\n') },
    expect: ['chip-missing'],
  },
  {
    name: 'caveat missing',
    files: { 'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party]' },
    expect: ['caveat-missing'],
  },
  {
    name: 'caveat four lines later is too far',
    files: { 'README.md': ['<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party]', '', 'a', 'b', 'Latency depends on region and load.'].join('\n') },
    expect: ['caveat-missing'],
  },
  {
    name: 'caveat within three lines, wrapped, passes',
    files: { 'README.md': ['<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party]', '', 'Latency depends on', 'region and load.'].join('\n') },
    expect: [],
  },
  {
    name: 'a caveat may sit mid-sentence, lowercase',
    files: { 'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party], though latency depends on region and load.' },
    expect: [],
  },
  {
    name: 'caveat through a link to an anchor in another doc',
    files: {
      'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party] ([why](docs/NOTES.md#latency-caveat))',
      'docs/NOTES.md': ['# Notes', '', '## Latency `caveat`', '', 'Latency depends on region and load.'].join('\n'),
    },
    expect: [],
  },
  {
    name: 'a link to an anchor without the caveat does not count',
    files: {
      'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party] ([why](#notes))',
      'docs/X.md': '',
    },
    extraReadme: ['', '<a id="notes"></a>', 'Something else.'],
    expect: ['caveat-missing'],
  },
  {
    name: 'a chip inside an HTML comment is not a chip (the reader sees an unlabelled figure)',
    files: { 'README.md': '<!-- claim:est_cost --> ~$0.21 / 1,000 answers <!-- [estimate] -->' },
    expect: ['chip-missing'],
  },
  {
    name: 'a display inside an HTML comment is not a display',
    files: { 'README.md': 'Fast check: <!-- claim:est_cost --> cheap <!-- ~$0.21 / 1,000 answers [estimate] -->' },
    expect: ['display-missing'],
  },
  {
    name: 'a caveat inside an HTML comment is not next to the chip',
    files: { 'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party] <!-- Latency depends on region and load. -->' },
    expect: ['caveat-missing'],
  },
  {
    name: 'a link inside an HTML comment is not a link to the caveat',
    files: {
      'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party] <!-- [why](docs/NOTES.md#latency) -->',
      'docs/NOTES.md': ['## Latency', '', 'Latency depends on region and load.'].join('\n'),
    },
    expect: ['caveat-missing'],
  },
  {
    name: 'an anchor whose caveat sits in a comment does not count',
    files: {
      'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party] ([why](docs/NOTES.md#latency))',
      'docs/NOTES.md': ['## Latency', '', '<!-- Latency depends on region and load. -->'].join('\n'),
    },
    expect: ['caveat-missing'],
  },
  {
    name: 'a caveat may set words in inline code',
    files: { 'README.md': '<!-- claim:tp_latency --> P50 0.23 s on OpenRouter [third-party]; latency depends on `region` and load.' },
    expect: [],
  },
  {
    name: 'a measured claim with no value says its empty display, without a chip',
    files: { 'README.md': "Ours: <!-- claim:meas_overhead --> not yet measured. Gateway's own time with simulated vendors; no run yet" },
    expect: [],
  },
  {
    name: 'an absent value is not tagged [measured]',
    files: { 'README.md': "Ours: <!-- claim:meas_overhead --> not yet measured [measured]. Gateway's own time with simulated vendors; no run yet" },
    expect: ['empty-measured-chip'],
  },
  {
    name: 'another label after an absent value is not this claim\'s chip',
    files: { 'README.md': "Ours: <!-- claim:meas_overhead --> not yet measured, against ~$0.21 [estimate]. Gateway's own time with simulated vendors; no run yet" },
    expect: [],
  },
  {
    name: 'a measured claim may not show a number nobody measured',
    files: { 'README.md': "Ours: <!-- claim:meas_overhead --> 1.2 ms p50 [measured]. Gateway's own time with simulated vendors; laptop" },
    expect: ['display-missing'],
  },
  {
    name: 'a measured value is read from a measured run',
    measured: MEASURED,
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: {
      'README.md': 'Ours: <!-- claim:meas_cost --> $0.213 / 1,000 answers [measured]. The fast check alone, on ' +
        'RAGTruth-QA test split, n=200, about 640 input tokens per answer.',
    },
    expect: [],
  },
  {
    name: 'a measured cost outside the block carries its conditions (D229)',
    measured: MEASURED,
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: { 'README.md': 'Ours: <!-- claim:meas_cost --> $0.213 / 1,000 answers [measured]' },
    expect: ['caveat-missing'],
  },
  {
    name: 'a value in a file that is not a measured run is refused',
    measured: { ...MEASURED, status: 'not_measured' },
    files: { 'README.md': 'Ours: <!-- claim:meas_cost --> $0.213 / 1,000 answers [measured]' },
    expect: ['display-missing', 'measured-mock'],
  },
  {
    name: 'a measured value from a mock run is refused',
    measured: { ...MEASURED, mock: true },
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: { 'README.md': 'Ours: <!-- claim:meas_cost --> not yet measured' },
    expect: ['measured-mock'],
  },
  {
    name: 'a measured overhead value needs its load run file',
    measured: MEASURED,
    files: {},
    expect: ['load-run-missing'],
  },
  {
    name: 'a [measured] chip without a measured claim',
    files: { 'docs/GUIDE.md': 'Our gateway adds 1 ms [measured].' },
    expect: ['stray-measured-chip'],
  },
  {
    name: 'a [measured] chip in a design record is not prose we publish',
    files: { 'docs/v2/NOTES.md': 'Our gateway adds 1 ms [measured].' },
    expect: [],
  },
  {
    name: 'a {measured:KEY} placeholder must name a measured.json value',
    files: { 'docs/launch/post.md': 'Our fast check costs {measured:fast_check_usd_per_1k}; overhead {measured:nope}.' },
    expect: ['measured-placeholder'],
  },
  {
    name: 'every figure in a launch draft carries a marker',
    files: {
      'docs/launch/post.md': [
        'The fast check costs <!-- claim:est_cost --> ~$0.21 / 1,000 answers [estimate].',
        'It answers in 212 ms and saves 90%, about $5.',
        '`CAP=$5` in a command is not a figure.',
      ].join('\n'),
    },
    expect: ['launch-figure', 'launch-figure', 'launch-figure'],
  },
  {
    name: 'a multiplier is a launch figure; an image size is not',
    files: { 'docs/launch/post.md': 'It is 27× cheaper, or 30x. The card is 1280×640 (or 1200x630).' },
    expect: ['launch-figure', 'launch-figure'],
  },
  {
    name: 'braces do not hide a launch figure; only a {measured:KEY} placeholder stands in for one',
    files: { 'docs/launch/post.md': 'We save {about $5 a day}, and the check costs {measured:fast_check_usd_per_1k}.' },
    expect: ['launch-figure'],
  },
  {
    name: 'PKCE called verified while Appendix D says unverified',
    files: { 'docs/SECURITY.md': 'Connect OpenRouter uses PKCE, verified against openrouter.ai.\n\nThe PKCE verifier is kept server-side.' },
    expect: ['pkce-wording'],
  },
  {
    name: 'PKCE wording that is hedged passes',
    files: { 'docs/SECURITY.md': 'PKCE is not yet verified against openrouter.ai; it was verified only against the mock OpenRouter. Connect becomes primary once PKCE is verified.' },
    expect: [],
  },
  {
    name: 'words after "verified" do not hedge it; a condition in another clause does not either',
    files: {
      'docs/SECURITY.md': [
        'Connect OpenRouter (PKCE) is verified only on localhost.',
        '',
        'PKCE sign-in was verified after a real round trip.',
        '',
        'When you click Connect, PKCE is verified end to end.',
      ].join('\n'),
    },
    expect: ['pkce-wording', 'pkce-wording', 'pkce-wording'],
  },
  {
    name: 'a table row is read on its own: a verified row fails, a hedged row passes',
    files: {
      'docs/SECURITY.md': [
        '| Check | Status |',
        '|---|---|',
        '| OpenRouter PKCE, Pages | not verified yet |',
        '| OpenRouter PKCE, localhost | Verified |',
      ].join('\n'),
    },
    expect: ['pkce-wording'],
  },
  {
    name: 'a list item without a full stop does not borrow the previous item\'s hedge',
    files: { 'docs/SECURITY.md': ['- Paste stays visible until the owner checks it', '- PKCE from the browser is verified'].join('\n') },
    expect: ['pkce-wording'],
  },
  {
    name: 'PKCE verified wording passes once Appendix D has dated results',
    spec: SPEC_VERIFIED,
    files: { 'docs/SECURITY.md': 'Connect OpenRouter uses PKCE, verified against openrouter.ai.' },
    expect: [],
  },
  {
    name: 'a stale measured block',
    files: { 'README.md': ['<!-- measured:start -->', '| old | table |', '<!-- measured:end -->'].join('\n') },
    expect: ['block-stale'],
  },
  {
    name: 'unpaired measured markers',
    files: { 'README.md': ['<!-- measured:end -->', '<!-- measured:start -->'].join('\n') },
    expect: ['block-unpaired'],
  },
  {
    name: 'launch: everything ready passes',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: LAUNCH_READY,
    files: {},
    expect: [],
  },
  {
    name: 'launch: a run over 5% failures, under 95% scored, or without quality; a run with no file',
    launch: true,
    measured: { ...MEASURED, runs: ['bench-1', 'bench-2', 'bench-3', 'load-1', 'gone-1'] },
    compare: COMPARE(REVIEWED_CELL),
    read: {
      ...LAUNCH_READY,
      'docs/benchmarks/results/bench-1.json': BENCH_RUN({ ...GOOD_QUALITY, failed: 12, failure_rate: 0.06 }),
      'docs/benchmarks/results/bench-2.json': BENCH_RUN({ ...GOOD_QUALITY, scored: 180 }),
      'docs/benchmarks/results/bench-3.json': JSON.stringify({ run_id: 'bench-3' }),
    },
    files: {},
    expect: ['launch-quality', 'launch-quality', 'launch-quality', 'launch-run-missing'],
  },
  {
    name: 'launch: no snapshot; two without snapshot_split; a split with one',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: LAUNCH_READY,
    files: {},
    variants: [
      { measured: { ...MEASURED, snapshot: null }, expect: ['launch-snapshot'] },
      { measured: { ...MEASURED, snapshot: [SNAPSHOT, 'jev-1.14-20261101'] }, expect: ['launch-snapshot'] },
      { measured: { ...MEASURED, snapshot: [SNAPSHOT, 'jev-1.14-20261101'], sources: { ...MEASURED.sources, snapshot_split: true } }, expect: [] },
      { measured: { ...MEASURED, sources: { ...MEASURED.sources, snapshot_split: true } }, expect: ['launch-snapshot'] },
      { measured: { ...MEASURED, sources: { fast_check_usd_per_1k: MEASURED.sources.fast_check_usd_per_1k, groundedness_ece: MEASURED.sources.groundedness_ece } }, expect: ['latency-conditions'] },
      { measured: { ...MEASURED, sources: { ...MEASURED.sources, fast_check_usd_per_1k: { cost_source: 'reported' } } }, expect: ['cost-conditions'] },
    ],
    expect: [],
  },
  {
    name: 'launch: the held-out ECE is published with its method, unless the tape is uncalibrated (D206)',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: LAUNCH_READY,
    files: {},
    variants: [
      // The C1 card once passed the wrong make variable: measured.json came out without the ECE.
      { measured: { ...MEASURED, values: { ...MEASURED.values, groundedness_ece_after: { ...MEASURED.values.groundedness_ece_after, value: null } } }, expect: ['launch-ece'] },
      { measured: { ...MEASURED, sources: { fast_check_usd_per_1k: MEASURED.sources.fast_check_usd_per_1k, gate_latency: MEASURED.sources.gate_latency } }, expect: ['launch-ece'] },
      {
        measured: { ...MEASURED, values: { ...MEASURED.values, groundedness_ece_after: { ...MEASURED.values.groundedness_ece_after, value: null } } },
        read: { ...LAUNCH_READY, [RECORDED_MANIFEST_PATH]: JSON.stringify({ ...RECORDED_TAPE, calibration: { snapshot: null, quality: 'none' } }) },
        expect: [],
      },
      {
        measured: { ...MEASURED, values: { ...MEASURED.values, groundedness_ece_after: { ...MEASURED.values.groundedness_ece_after, value: null } } },
        read: { ...LAUNCH_READY, [RECORDED_MANIFEST_PATH]: JSON.stringify({ ...RECORDED_TAPE, calibration: { snapshot: SNAPSHOT, quality: 'measured' } }) },
        expect: ['launch-ece'],
      },
    ],
    expect: [],
  },
  {
    name: 'launch: the tape was recorded on another snapshot and another version',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: {
      ...LAUNCH_READY,
      [RECORDED_MANIFEST_PATH]: JSON.stringify({
        ...RECORDED_TAPE, trustagent_version: '1.0.0', decision_model: { ...RECORDED_TAPE.decision_model, snapshot: 'jev-1.12' },
      }),
    },
    files: {},
    expect: ['launch-snapshot', 'launch-version'],
  },
  {
    name: 'launch: an open reverify and a licence placeholder',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    claims: { ...FIXTURE_CLAIMS, claims: FIXTURE_CLAIMS.claims.map((c) => (c.id === 'vendor_price' ? { ...c, reverify: 'Re-read the page.' } : c)) },
    read: {
      ...LAUNCH_READY,
      LICENSE: `MIT License\n\nCopyright (c) 2026 ${OWNER_PLACEHOLDER}\n`,
      'frontend-react/package.json': `{"author": "${OWNER_PLACEHOLDER}"}`,
    },
    files: {},
    expect: ['launch-license', 'launch-license', 'launch-reverify'],
  },
  {
    name: 'an open reverify warns without --launch',
    claims: { ...FIXTURE_CLAIMS, claims: FIXTURE_CLAIMS.claims.map((c) => (c.id === 'vendor_price' ? { ...c, reverify: 'Re-read the page.' } : c)) },
    files: {},
    expect: [],
    warnings: ['reverify-open', 'third-party-stale'],
  },
  {
    name: 'an estimate twice off its measured pair warns; --launch refuses it',
    measured: { ...MEASURED, values: { ...MEASURED.values, fast_check_usd_per_1k: { ...MEASURED.values.fast_check_usd_per_1k, value: 0.9 } } },
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: {},
    expect: [],
    warnings: ['pair-divergence', 'third-party-stale'],
  },
  {
    name: 'launch: a divergent pair is an error',
    launch: true,
    measured: { ...MEASURED, values: { ...MEASURED.values, fast_check_usd_per_1k: { ...MEASURED.values.fast_check_usd_per_1k, value: 0.05 } } },
    compare: COMPARE(REVIEWED_CELL),
    read: LAUNCH_READY,
    files: {},
    expect: ['pair-divergence'],
  },
  {
    name: 'a measured_pair that names no measured claim',
    claims: { ...FIXTURE_CLAIMS, claims: FIXTURE_CLAIMS.claims.map((c) => (c.id === 'est_cost' ? { ...c, measured_pair: 'vendor_price' } : c)) },
    files: {},
    expect: ['pair-unknown'],
  },
  {
    name: 'a measured agreement beside the like-for-like figure passes',
    measured: MEASURED,
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: {
      'README.md': [
        '## Accuracy',
        'Ours: <!-- claim:meas_agree --> 81.3% [measured].',
        '',
        'Like for like: <!-- claim:arize_ragtruth_accuracy_at_default_threshold --> 76% at 0.5 [third-party]. Thresholds need calibrating.',
      ].join('\n'),
    },
    expect: [],
  },
  {
    name: 'a measured agreement alone, or beside the tuned figure only, warns; --launch refuses it',
    measured: MEASURED,
    read: { 'docs/benchmarks/load/load-1.json': JSON.stringify(LOAD_RUN) },
    files: {
      'README.md': [
        '## Accuracy',
        'Ours: <!-- claim:meas_agree --> 81.3% [measured], against <!-- claim:tp_fresh --> 87% [third-party].',
        '',
        '## Elsewhere',
        'Like for like: <!-- claim:arize_ragtruth_accuracy_at_default_threshold --> 76% at 0.5 [third-party]. Thresholds need calibrating.',
      ].join('\n'),
    },
    expect: [],
    warnings: ['agreement-alone', 'third-party-stale'],
    variants: [{ launch: true, compare: COMPARE(REVIEWED_CELL), read: LAUNCH_READY, expect: ['agreement-alone'], warnings: ['third-party-stale'] }],
  },
  {
    name: 'an agreement not measured yet does not need the comparison until --launch',
    files: { 'docs/GUIDE.md': 'Ours: <!-- claim:meas_agree --> not yet measured.' },
    expect: [],
  },
  {
    name: 'launch: no recorded dataset, not measured, an unverified cell, no og.png',
    launch: true,
    compare: COMPARE({ value: 'unverified', text: '', source_url: null, as_of: null, reviewed: false }),
    files: {},
    expect: ['launch-compare', 'launch-not-measured', 'launch-og', 'launch-og', 'launch-recorded'],
  },
  {
    name: 'launch: a manifest that does not say kind "recorded" is not a recording',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: { ...LAUNCH_READY, [RECORDED_MANIFEST_PATH]: JSON.stringify({ schema: 'trustagent.recording/v1' }) },
    files: {},
    expect: ['launch-recorded'],
  },
  {
    name: 'launch: a tape that says recorded but names mock-upstream, or no host, is not a recording (D203)',
    launch: true,
    measured: MEASURED,
    compare: COMPARE(REVIEWED_CELL),
    read: {
      ...LAUNCH_READY,
      [RECORDED_MANIFEST_PATH]: JSON.stringify({
        ...RECORDED_TAPE,
        decision_model: { ...RECORDED_TAPE.decision_model, host: 'mock-upstream' },
        judge: { ...RECORDED_TAPE.judge, host: undefined },
        answer_sources: { scripted: 10, generated: 2, generated_host: 'mock-upstream' },
      }),
    },
    files: {},
    expect: ['launch-recorded', 'launch-recorded', 'launch-recorded'],
    variants: [
      {
        // Scripted answers only: no generated host is needed; the two model hosts still are.
        read: {
          ...LAUNCH_READY,
          [RECORDED_MANIFEST_PATH]: JSON.stringify({ ...RECORDED_TAPE, answer_sources: { scripted: 12, generated: 0 } }),
        },
        expect: [],
      },
      {
        read: {
          ...LAUNCH_READY,
          [RECORDED_MANIFEST_PATH]: JSON.stringify({ ...RECORDED_TAPE, judge: { ...RECORDED_TAPE.judge, host: 'api.openai.com' } }),
        },
        expect: ['launch-recorded'],
      },
      // Without the contract's host list nothing can vouch for the tape.
      { read: { ...LAUNCH_READY, [COMMON_SCHEMA_PATH]: '{}' }, expect: ['launch-recorded'] },
    ],
  },
  {
    name: 'launch: a mock recording, a cell the owner has not reviewed, a wrong-size og.png',
    launch: true,
    measured: MEASURED,
    compare: COMPARE({ ...REVIEWED_CELL, reviewed: false }),
    read: {
      ...LAUNCH_READY,
      [RECORDED_MANIFEST_PATH]: MOCK_RECORDING,
      'docs/launch/og.png': png(1200, 630),
      'frontend-react/public/og.png': png(OG_WIDTH, OG_HEIGHT, OG_MAX_BYTES),
    },
    files: {},
    expect: ['launch-compare', 'launch-og', 'launch-og', 'launch-recorded'],
  },
];

function runCase(c) {
  const files = new Map(Object.entries(c.files));
  if (c.extraReadme) files.set('README.md', `${files.get('README.md')}\n${c.extraReadme.join('\n')}`);
  const extra = c.read ?? {};
  const read = (p) => (p in extra ? Buffer.from(extra[p]) : files.has(p) ? Buffer.from(files.get(p)) : null);
  return checkClaims({
    files,
    claims: c.claims ?? FIXTURE_CLAIMS,
    measured: c.measured ?? NOT_MEASURED,
    compare: c.compare ?? null,
    specText: c.spec ?? SPEC_UNVERIFIED,
    read,
    today: TODAY,
    launch: Boolean(c.launch),
  });
}

function selfTestRun(check) {
  const dir = mkdtempSync(join(tmpdir(), 'check-claims-'));
  try {
    const write = (path, content) => {
      mkdirSync(dirname(join(dir, path)), { recursive: true });
      writeFileSync(join(dir, path), content);
    };
    write(CLAIMS_PATH, JSON.stringify(FIXTURE_CLAIMS));
    write(MEASURED_PATH, JSON.stringify(NOT_MEASURED));
    write(SPEC_PATH, SPEC_UNVERIFIED);
    const readme = [
      '# Fixture',
      '',
      '<!-- measured:start -->',
      '<!-- measured:end -->',
      '',
      '- <!-- claim:est_cost --> ~$0.21 / 1,000 answers [estimate]',
      '',
    ].join('\n');
    write('README.md', readme);
    write('docs/archive/old.md', 'Ours: 1 ms [measured]\n');
    const scan = (opts = {}) => {
      const out = [];
      const code = run({ root: dir, today: TODAY, log: (l) => out.push(l), env: {}, ...opts });
      return { code, out };
    };

    const stale = scan();
    check('run: a stale block exits 1', stale.code === 1, JSON.stringify(stale.out));
    check('run: the stale block is named', stale.out.some((l) => l.startsWith('README.md:3: error [block-stale]')), JSON.stringify(stale.out));
    check('run: the archive is not prose we publish', !stale.out.some((l) => l.startsWith('docs/archive')), JSON.stringify(stale.out));

    const written = scan({ write: true });
    check('run: --write fixes the block and exits 0', written.code === 0, JSON.stringify(written.out));
    const text = readFileSync(join(dir, 'README.md'), 'utf8');
    check('run: --write keeps the prose around the block', text.startsWith('# Fixture\n\n<!-- measured:start -->\n') && text.endsWith('[estimate]\n'));
    check('run: before a measured run the block is one sentence, without a [measured] chip',
      text.includes(`<!-- measured:start -->\n${BLOCK_HEADER}\n\n${NOT_MEASURED_TEXT}\n<!-- measured:end -->`) && !text.includes('[measured]'), text);
    const again = scan({ write: true });
    check('run: a second --write changes nothing', again.code === 0 && !again.out.some((l) => l.includes('regenerated')), JSON.stringify(again.out));

    // A load run published before a measured run adopts it: the note says where it is.
    write(`${LOAD_RUNS_DIR}/load-1.json`, JSON.stringify(LOAD_RUN));
    const published = scan();
    check('run: a published load run makes the block stale', published.code === 1 && published.out.some((l) => l.includes('[block-stale]')), JSON.stringify(published.out));
    scan({ write: true });
    check('run: the block says where the published load run is',
      readFileSync(join(dir, 'README.md'), 'utf8').includes(`${NOT_MEASURED_TEXT} ${LOAD_RUN_PUBLISHED_TEXT}`));

    write(MEASURED_PATH, JSON.stringify(MEASURED));
    write(`${LOAD_RUNS_DIR}/load-1.json`, JSON.stringify(LOAD_RUN));
    const afterRun = scan();
    check('run: a new measured run makes the block stale', afterRun.code === 1 && afterRun.out.some((l) => l.includes('[block-stale]')), JSON.stringify(afterRun.out));
    scan({ write: true });
    const measuredText = readFileSync(join(dir, 'README.md'), 'utf8');
    check('run: the block shows the measured values and the run machine',
      measuredText.includes('<!-- claim:meas_cost --> $0.213 / 1,000 answers [measured]') &&
        measuredText.includes('<!-- claim:meas_overhead --> 1.23 ms p50 [measured]') &&
        measuredText.includes('<!-- claim:meas_latency --> 212.4 ms p50 [measured]') &&
        measuredText.includes('measured on Test CPU, 10 logical CPUs and 8 GB RAM visible to Linux') &&
        measuredText.includes(`From the measured runs bench-1, load-1 (bench-publish, 2026-11-02, decision-model snapshot ${SNAPSHOT}).`) &&
        measuredText.includes('<!-- claim:meas_agree --> 81.3% [measured]') &&
        measuredText.includes(`<!-- claim:${LIKE_FOR_LIKE_ID} --> 76% at 0.5 [third-party]`),
      measuredText);

    const launch = scan({ launch: true });
    check('run: --launch refuses a missing recording, comparison and og.png', launch.code === 1 &&
      ['launch-recorded', 'launch-compare', 'launch-og'].every((r) => launch.out.some((l) => l.includes(`[${r}]`))), JSON.stringify(launch.out));
    const annotated = scan({ launch: true, env: { GITHUB_ACTIONS: 'true' } });
    check('run: GitHub annotations in Actions', annotated.out.some((l) => l.startsWith('::error file=frontend-react/demo-data/recorded/manifest.json::')),
      JSON.stringify(annotated.out));

    rmSync(join(dir, MEASURED_PATH));
    let error;
    try {
      scan();
    } catch (err) {
      error = err;
    }
    check('run: a missing measured.json is a ConfigError', error instanceof ConfigError && error.message.includes(MEASURED_PATH),
      error ? error.message : 'no error');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

export function selfTest() {
  let passed = 0;
  const failures = [];
  const check = (name, ok, detail = '') => {
    if (ok) passed += 1;
    else failures.push(detail ? `${name}: ${detail}` : name);
  };

  // A case may list variants: the same case with some fields replaced (its own expect and warnings).
  const runs = CASES.flatMap((c) => [c, ...(c.variants ?? []).map((v, k) => ({ ...c, variants: undefined, ...v, name: `${c.name} [${k + 1}]` }))]);
  for (const c of runs) {
    let result;
    try {
      result = runCase(c);
    } catch (err) {
      check(c.name, false, `threw ${err.stack ?? err}`);
      continue;
    }
    const errors = result.problems.filter((p) => p.level === 'error').map((p) => p.rule).sort();
    const warnings = result.problems.filter((p) => p.level === 'warn').map((p) => p.rule).sort();
    check(c.name, JSON.stringify(errors) === JSON.stringify([...c.expect].sort()),
      `expected ${JSON.stringify(c.expect)}, got ${JSON.stringify(result.problems.map(formatProblem))}`);
    // The fixture's one stale third-party claim warns in every case; the age rule has its own case.
    const wantWarnings = [...(c.warnings ?? ['third-party-stale'])].sort();
    check(`${c.name} (warnings)`, JSON.stringify(warnings) === JSON.stringify(wantWarnings), `got ${JSON.stringify(result.problems.map(formatProblem))}`);
  }

  // The age rule: only the claim older than 120 days warns, and it names the age.
  const aged = runCase({ files: {} }).problems.filter((p) => p.rule === 'third-party-stale');
  check('age: one warning, for tp_latency at 222 days', aged.length === 1 && aged[0].message.startsWith('tp_latency: as_of 2026-03-01 is 222 days old'),
    JSON.stringify(aged));

  // Generated blocks pass their own rules, measured or not, in the README and in a launch draft
  // (whose figures need markers: the block's interval and notes are exempt, being generated).
  const PARTIAL = { ...MEASURED, values: { ...MEASURED.values, gate_latency_ms_p50: { ...MEASURED.values.gate_latency_ms_p50, value: null } } };
  for (const [name, measured, markers] of [['not measured', NOT_MEASURED, 0], ['measured', MEASURED, 5], ['partly measured', PARTIAL, 5]]) {
    const read = (p) => (p === `${LOAD_RUNS_DIR}/load-1.json` ? Buffer.from(JSON.stringify(LOAD_RUN)) : null);
    const block = renderMeasuredBlock(FIXTURE_CLAIMS, measured, read);
    const text = ['<!-- measured:start -->', ...block.lines, '<!-- measured:end -->'].join('\n');
    for (const file of ['README.md', 'docs/launch/post.md']) {
      const result = checkClaims({ files: new Map([[file, text]]), claims: FIXTURE_CLAIMS, measured, specText: SPEC_UNVERIFIED, read, today: TODAY });
      const problems = result.problems.filter((p) => p.rule !== 'third-party-stale');
      check(`generated block (${name}) passes in ${file}`, problems.length === 0 && result.markers === markers && result.blocks === 1,
        JSON.stringify(result.problems.map(formatProblem)));
    }
  }
  const notMeasured = renderMeasuredBlock(FIXTURE_CLAIMS, NOT_MEASURED, () => null);
  check('generated block (not measured): one sentence, no table and no [measured] chip',
    JSON.stringify(notMeasured.lines) === JSON.stringify([BLOCK_HEADER, '', NOT_MEASURED_TEXT]), JSON.stringify(notMeasured.lines));
  check('generated block (not measured, a load run published): says where it is',
    renderMeasuredBlock(FIXTURE_CLAIMS, NOT_MEASURED, () => null, ['load-1.json']).lines[2] === `${NOT_MEASURED_TEXT} ${LOAD_RUN_PUBLISHED_TEXT}`);
  {
    const read = (p) => (p === `${LOAD_RUNS_DIR}/load-1.json` ? Buffer.from(JSON.stringify(LOAD_RUN)) : null);
    const lines = renderMeasuredBlock(FIXTURE_CLAIMS, MEASURED, read).lines;
    // D118: the overhead row has a table of its own, after the fast-check results and their latency.
    const at = (id) => lines.findIndex((l) => l.includes(`<!-- claim:${id} -->`));
    const intro = lines.indexOf(OVERHEAD_INTRO);
    check('generated block (measured): proxy overhead is a separate table after the latency',
      at('meas_latency') >= 0 && at('meas_latency') < intro && intro < at('meas_overhead') &&
        lines[at('meas_overhead') - 1] === '|---|---|---|', JSON.stringify(lines));
    const agree = lines[at('meas_agree')] ?? '';
    check('generated block (measured): the agreement row has its interval and n per class (D212)',
      agree.includes('81.3% [measured]') && agree.includes('95% interval 75% to 86% (bootstrap, 2000 resamples); 40 answers with a hallucination and 160 without.'),
      agree);
    check('generated block (measured): the like-for-like figure follows the results (D212)',
      at(LIKE_FOR_LIKE_ID) > at('meas_agree') && at(LIKE_FOR_LIKE_ID) < intro &&
        lines[at(LIKE_FOR_LIKE_ID)].startsWith('Like for like (default thresholds): Arize RAGTruth test, Jev accuracy at a 0.5 threshold:'),
      JSON.stringify(lines));
    check('generated block (measured): a reported cost says so', (lines[at('meas_cost')] ?? '').includes('Cost as the vendor reported it.'));
    check('generated block (measured): the footer names the snapshot',
      lines[lines.length - 1].includes(`decision-model snapshot ${SNAPSHOT}`), lines[lines.length - 1]);
    check('generated block (measured): the latency names its conditions, where it ran and n (D205, D225)',
      (lines[at('meas_latency')] ?? '').includes("212.4 ms p50 [measured] | The fast check's own time per answer: warm connection, " +
        'one request at a time, run on Linux, region cloud us-east, snapshot jev-1.13-20260917, n=199. |'),
      lines[at('meas_latency')]);
    const partial = renderMeasuredBlock(FIXTURE_CLAIMS, PARTIAL, read).lines;
    check('generated block (partly measured): a value the run lacks has no [measured] chip and no conditions',
      partial.some((l) => l.endsWith('<!-- claim:meas_latency --> not yet measured | |')), JSON.stringify(partial));
    const bare = renderMeasuredBlock(FIXTURE_CLAIMS, { ...MEASURED, sources: { fast_check_usd_per_1k: { cost_source: 'reported' } } }, read);
    check('generated block (measured): a latency without its conditions is a problem, never a raw placeholder',
      bare.problems.some((p) => p.includes('without sources.gate_latency.conditions')) && !bare.lines.join('\n').includes('{gate_conditions}'),
      JSON.stringify(bare.problems));
    check('generated block (measured): the cost names the dataset, n and the input tokens per answer (D229)',
      (lines[at('meas_cost')] ?? '').includes('$0.213 / 1,000 answers [measured] | Cost as the vendor reported it. The fast check alone, on ' +
        'RAGTruth-QA test split, n=200, about 640 input tokens per answer. |'),
      lines[at('meas_cost')]);
    check('generated block (measured): a cost without its conditions is a problem, never a raw placeholder',
      bare.problems.some((p) => p.includes('without sources.fast_check_usd_per_1k.conditions (D229)')) && !bare.lines.join('\n').includes('{cost_conditions}'),
      JSON.stringify(bare.problems));
    check('cost conditions: a cost is its own key, the ratio is the judge\'s cost, a proxy figure has none',
      costConditionsKey('fast_check_usd_per_1k', { unit: 'usd_per_1k', system: 'gate' }) === 'fast_check_usd_per_1k' &&
        costConditionsKey('same_checks_cost_ratio', { unit: 'multiplier', system: 'gate' }) === 'llm_same_checks_usd_per_1k' &&
        costConditionsKey('proxy_overhead_ms_p50', { unit: 'ms', system: 'proxy' }) === null);
    const split = renderMeasuredBlock(FIXTURE_CLAIMS, { ...MEASURED, snapshot: [SNAPSHOT, 'jev-1.14'], sources: { snapshot_split: true } }, read).lines;
    check('generated block (split snapshots): the footer names both', split[split.length - 1].includes(`snapshots ${SNAPSHOT} and jev-1.14`));
    const computed = renderMeasuredBlock(FIXTURE_CLAIMS, { ...MEASURED, sources: { fast_check_usd_per_1k: { cost_source: 'computed' } } }, read).lines;
    check('generated block (measured): a computed cost is labelled measured tokens × vendor price',
      computed.some((l) => l.includes('<!-- claim:meas_cost -->') && l.includes('Measured tokens × vendor price.')));
  }

  // Helpers.
  check('format: usd_per_1k under $1 keeps 3 decimals', formatValue(0.2134, 'usd_per_1k') === '0.213');
  check('format: fraction as a percentage', formatValue(0.873, 'fraction') === '87.3%');
  check('format: ms to 2 decimals', formatValue(1.234, 'ms') === '1.23');
  check('slug: GitHub heading anchors', slug('Latency `caveat` (P50)!') === 'latency-caveat-p50');
  check('pkce: unverified rows', JSON.stringify(pkceStatus(SPEC_UNVERIFIED)) === '{"localhost":false,"pages":false,"found":true}');
  check('pkce: dated rows', JSON.stringify(pkceStatus(SPEC_VERIFIED)) === '{"localhost":true,"pages":true,"found":true}');
  const half = SPEC_UNVERIFIED.replace('still unverified against the real openrouter.ai', 'Verified 2026-11-01');
  check('pkce: a sentence about the verified callback passes, the other fails',
    pkceWordingHits(['PKCE with a localhost callback is verified.'], pkceStatus(half)).length === 0 &&
      pkceWordingHits(['PKCE from the Pages site is verified.'], pkceStatus(half)).length === 1);
  check('png: size from IHDR', JSON.stringify(pngSize(png(1280, 640))) === '{"width":1280,"height":640}' && pngSize(Buffer.from('nope')) === null);
  check('mask: inline code keeps columns', maskCode(['a `<!-- x -->` b'])[0] === `a ${' '.repeat(12)} b`);
  const realRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const realHosts = vendorHosts((rel) => (existsSync(join(realRoot, rel)) ? readFileSync(join(realRoot, rel)) : null));
  check('hosts: the contract names the vendor hosts, and mock-upstream is not one',
    Array.isArray(realHosts) && realHosts.includes('openrouter.ai') && !realHosts.includes('mock-upstream'), JSON.stringify(realHosts));
  check('mask: comments span lines', JSON.stringify(maskComments(['a <!-- x', 'y --> b'])) === JSON.stringify(['a       ', '      b']));

  try {
    selfTestRun(check);
  } catch (err) {
    check('run: end-to-end fixture', false, `threw ${err.stack ?? err}`);
  }

  for (const failure of failures) console.error(`FAIL ${failure}`);
  console.log(`check-claims self-test: ${passed} passed, ${failures.length} failed.`);
  return failures.length ? 1 : 0;
}

// ---------------------------------------------------------------------------------------------

const USAGE = 'Usage: node scripts/check-claims.mjs [--write] [--launch] [--self-test] [--root <dir>] [--today YYYY-MM-DD]';

function main(argv) {
  let args;
  try {
    ({ values: args } = parseArgs({
      args: argv,
      options: {
        write: { type: 'boolean' },
        launch: { type: 'boolean' },
        'self-test': { type: 'boolean' },
        root: { type: 'string' },
        today: { type: 'string' },
        help: { type: 'boolean', short: 'h' },
      },
    }));
  } catch (err) {
    console.error(`check-claims: ${err.message}\n${USAGE}`);
    return 2;
  }
  if (args.help) {
    console.log(USAGE);
    return 0;
  }
  if (args['self-test']) return selfTest();
  const today = args.today ?? new Date().toISOString().slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(today) || Number.isNaN(Date.parse(`${today}T00:00:00Z`))) {
    console.error(`check-claims: --today must be a date (YYYY-MM-DD), got ${today}\n${USAGE}`);
    return 2;
  }
  const root = args.root ? resolve(args.root) : resolve(dirname(fileURLToPath(import.meta.url)), '..');
  try {
    return run({ root, write: Boolean(args.write), launch: Boolean(args.launch), today });
  } catch (err) {
    if (err instanceof ConfigError) {
      console.error(`check-claims: ${err.message}`);
      return 2;
    }
    throw err;
  }
}

/** True when this file is the program being run (compared as real paths, so symlinks work). */
function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isEntryPoint()) {
  process.exitCode = main(process.argv.slice(2));
}

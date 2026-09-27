#!/usr/bin/env node
/**
 * check-forbidden.mjs - fail when a superseded identifier reappears in code.
 *
 * docs/v2/SPEC.md Appendix A lists identifiers that earlier designs used and
 * that must not be implemented. The machine-readable list lives in
 * contracts/superseded.v1.json (authored by the contracts stream); this script
 * scans the repository for them. It runs in CI (.github/workflows/ci-contracts.yml).
 *
 * Usage
 *   node scripts/check-forbidden.mjs                 scan the repository
 *   node scripts/check-forbidden.mjs --self-test     run the built-in fixture tests
 *   node scripts/check-forbidden.mjs --spec <file>   use another identifier list
 *   node scripts/check-forbidden.mjs --root <dir>    scan another git checkout
 *
 * Exit codes
 *   0  no error-level hits, or the identifier list does not exist yet (a note is printed)
 *   1  at least one error-level hit, or a self-test assertion failed
 *   2  usage or configuration error (invalid list, invalid regex, not a git checkout)
 *
 * Identifier list format (contracts/superseded.v1.json)
 *
 *   {
 *     "version": "1",
 *     "description": "optional free text",
 *     "identifiers": [
 *       {
 *         "area": "dx",
 *         "pattern": "some_old_name",
 *         "kind": "literal",
 *         "note": "why it is superseded and what replaces it"
 *       },
 *       {
 *         "area": "gateway",
 *         "pattern": "^\\s*old_key:",
 *         "kind": "regex",
 *         "flags": "i",
 *         "allow": ["contracts/vectors/**"],
 *         "level": "warn",
 *         "note": "..."
 *       }
 *     ]
 *   }
 *
 *   Top level: "version" must be "1"; "identifiers" is a non-empty array;
 *   "$schema" and "description" are optional. Each identifier:
 *     area     required  non-empty string; grouping label reported with each hit
 *                        (SPEC Appendix A uses gateway, engine, frontend, dx, contract).
 *     pattern  required  non-empty string; what to look for, matched line by line.
 *     kind     required  "literal": case-sensitive substring, no special characters;
 *                        "regex":   JavaScript RegExp source without delimiters.
 *     note     required  non-empty string; why / what replaces it, printed with each hit.
 *     flags    optional  regex only; any of "i", "m", "s", "u" (e.g. "i" for HTTP headers).
 *     allow    optional  array of path globs (relative to the repo root, "/" separated;
 *                        "**" crosses directories, "*" and "?" do not) where this
 *                        identifier is permitted, e.g. vectors that assert its absence.
 *     level    optional  "error" (default: a hit fails the check) or "warn" (reported
 *                        only; for identifiers still present until a later phase removes them).
 *   Unknown keys are rejected so that typos fail loudly.
 *
 * What is scanned
 *   Paths: every identifier is also matched against the path of every file tracked
 *   by git (`git ls-files`), *.md files included, so a superseded file such as
 *   docs/JEV.md or scripts/traffic.py fails the check just by existing. A path hit
 *   is reported as "path: level [area] pattern - note (file path)".
 *   Contents: every tracked file that is not binary is scanned line by line, except
 *   any *.md file (the check covers code, not prose).
 *   Never checked at all: docs/archive/**, docs/v2/** (the spec itself names the
 *   identifiers), contracts/superseded.v1.json (or the --spec file) and this script.
 *   A line containing the marker "check-forbidden: ignore" is skipped, e.g. a test
 *   asserting an identifier is absent. "allow" globs apply to path and content hits.
 *
 * No dependencies: Node >= 20 standard library only (CI uses Node 22).
 */

import { execFileSync } from 'node:child_process';
import {
  lstatSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, unlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

export const SPEC_PATH = 'contracts/superseded.v1.json';
export const SELF_PATH = 'scripts/check-forbidden.mjs';
export const IGNORE_MARKER = 'check-forbidden: ignore';

/** Paths never checked at all, neither path nor contents (globs relative to the repo root). */
export const EXCLUDED_GLOBS = [
  'docs/archive/**',
  'docs/v2/**',
  SPEC_PATH,
  SELF_PATH,
];

/** Paths whose contents are not scanned; their paths are still checked. */
export const CONTENT_EXCLUDED_GLOBS = ['**/*.md'];

const BINARY_EXTENSIONS = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'ico', 'webp', 'pdf', 'woff', 'woff2', 'ttf', 'otf',
  'mp4', 'webm', 'zip', 'gz', 'tgz', 'jar', 'exe', 'so', 'dylib', 'wasm',
]);
const MAX_FILE_BYTES = 5 * 1024 * 1024;
const TOP_LEVEL_KEYS = new Set(['$schema', 'version', 'description', 'identifiers']);
const ENTRY_KEYS = new Set(['area', 'pattern', 'kind', 'note', 'flags', 'allow', 'level']);
const ALLOWED_FLAGS = new Set(['i', 'm', 's', 'u']);

/** A configuration problem (exit code 2). */
export class SpecError extends Error {
  constructor(problems) {
    super(problems.join('\n'));
    this.problems = problems;
  }
}

/** Translate a path glob ("**", "*", "?") into an anchored RegExp. */
export function globToRegExp(glob) {
  let source = '';
  for (let i = 0; i < glob.length; i += 1) {
    const c = glob[i];
    if (c === '*' && glob[i + 1] === '*') {
      if (glob[i + 2] === '/') {
        source += '(?:.*/)?'; // "**/" matches zero or more directories
        i += 2;
      } else {
        source += '.*';
        i += 1;
      }
    } else if (c === '*') {
      source += '[^/]*';
    } else if (c === '?') {
      source += '[^/]';
    } else {
      source += c.replace(/[.+^${}()|[\]\\]/g, '\\$&');
    }
  }
  return new RegExp(`^${source}$`);
}

const EXCLUDED = EXCLUDED_GLOBS.map(globToRegExp);
const CONTENT_EXCLUDED = CONTENT_EXCLUDED_GLOBS.map(globToRegExp);

/** True when `path` (repo-relative, "/" separated) is never checked, not even its path. */
export function isExcluded(path) {
  return EXCLUDED.some((re) => re.test(path));
}

/** True when the contents of `path` are not scanned (its path still is, unless isExcluded). */
export function isContentExcluded(path) {
  return isExcluded(path) || CONTENT_EXCLUDED.some((re) => re.test(path));
}

const isNonEmptyString = (v) => typeof v === 'string' && v.trim() !== '';

/**
 * Validate a parsed identifier list and compile it into rules.
 * Throws SpecError listing every problem found.
 */
export function compileSpec(spec, source = SPEC_PATH) {
  const problems = [];
  const fail = (msg) => problems.push(`${source}: ${msg}`);

  if (spec === null || typeof spec !== 'object' || Array.isArray(spec)) {
    throw new SpecError([`${source}: top level must be a JSON object`]);
  }
  for (const key of Object.keys(spec)) {
    if (!TOP_LEVEL_KEYS.has(key)) fail(`unknown top-level key "${key}"`);
  }
  if (spec.version !== '1') fail(`"version" must be the string "1" (got ${JSON.stringify(spec.version)})`);
  if (!Array.isArray(spec.identifiers) || spec.identifiers.length === 0) {
    fail('"identifiers" must be a non-empty array');
    throw new SpecError(problems);
  }

  const rules = [];
  spec.identifiers.forEach((entry, index) => {
    const at = `identifiers[${index}]`;
    if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) {
      fail(`${at} must be an object`);
      return;
    }
    const before = problems.length;
    for (const key of Object.keys(entry)) {
      if (!ENTRY_KEYS.has(key)) fail(`${at}: unknown key "${key}"`);
    }
    for (const key of ['area', 'pattern', 'note']) {
      if (!isNonEmptyString(entry[key])) fail(`${at}: "${key}" must be a non-empty string`);
    }
    if (entry.kind !== 'literal' && entry.kind !== 'regex') {
      fail(`${at}: "kind" must be "literal" or "regex" (got ${JSON.stringify(entry.kind)})`);
    }
    const level = entry.level ?? 'error';
    if (level !== 'error' && level !== 'warn') {
      fail(`${at}: "level" must be "error" or "warn" (got ${JSON.stringify(entry.level)})`);
    }
    const allow = entry.allow ?? [];
    if (!Array.isArray(allow) || !allow.every(isNonEmptyString)) {
      fail(`${at}: "allow" must be an array of non-empty path globs`);
    }
    const flags = entry.flags ?? '';
    if (entry.flags !== undefined) {
      if (entry.kind !== 'regex') fail(`${at}: "flags" is only valid with kind "regex"`);
      if (typeof flags !== 'string' || [...flags].some((f) => !ALLOWED_FLAGS.has(f))) {
        fail(`${at}: "flags" may only contain ${[...ALLOWED_FLAGS].join(', ')}`);
      }
    }
    if (problems.length > before) return;

    let test;
    if (entry.kind === 'regex') {
      let re;
      try {
        re = new RegExp(entry.pattern, flags);
      } catch (err) {
        fail(`${at}: invalid regex ${JSON.stringify(entry.pattern)}: ${err.message}`);
        return;
      }
      if (re.test('')) {
        fail(`${at}: regex ${JSON.stringify(entry.pattern)} matches the empty string (it would flag every line)`);
        return;
      }
      test = (line) => {
        const m = re.exec(line);
        return m ? m.index : -1;
      };
    } else {
      test = (line) => line.indexOf(entry.pattern);
    }
    rules.push({
      area: entry.area,
      pattern: entry.pattern,
      kind: entry.kind,
      flags,
      note: entry.note,
      level,
      allow: allow.map(globToRegExp),
      test,
    });
  });

  if (problems.length > 0) throw new SpecError(problems);
  return rules;
}

/** True when the content looks binary (NUL byte in the first 8 KiB). */
export function looksBinary(buffer) {
  return buffer.subarray(0, 8192).includes(0);
}

function hasBinaryExtension(path) {
  const dot = path.lastIndexOf('.');
  return dot !== -1 && BINARY_EXTENSIONS.has(path.slice(dot + 1).toLowerCase());
}

const rulesFor = (path, rules) => rules.filter((rule) => !rule.allow.some((re) => re.test(path)));

/** Check one tracked file's path; returns hits ({path, line: 0, column, rule}). */
export function scanPath(path, rules) {
  const hits = [];
  for (const rule of rulesFor(path, rules)) {
    const index = rule.test(path);
    if (index !== -1) hits.push({ path, line: 0, column: index + 1, rule });
  }
  return hits;
}

/** Scan one file's text; returns hits ({path, line, column, rule}). */
export function scanText(path, text, rules) {
  const applicable = rulesFor(path, rules);
  if (applicable.length === 0) return [];
  const hits = [];
  const lines = text.split(/\r?\n/);
  lines.forEach((content, i) => {
    if (content.includes(IGNORE_MARKER)) return;
    for (const rule of applicable) {
      const index = rule.test(content);
      if (index !== -1) hits.push({ path, line: i + 1, column: index + 1, rule });
    }
  });
  return hits;
}

function describePattern(rule) {
  return rule.kind === 'regex' ? `/${rule.pattern}/${rule.flags}` : JSON.stringify(rule.pattern);
}

/**
 * "path:line:col: level [area] pattern - note" for a content hit,
 * "path: level [area] pattern - note (file path)" for a path hit (line 0).
 */
export function formatHit(hit) {
  const { rule } = hit;
  const what = `${rule.level} [${rule.area}] ${describePattern(rule)} - ${rule.note}`;
  return hit.line === 0 ? `${hit.path}: ${what} (file path)` : `${hit.path}:${hit.line}:${hit.column}: ${what}`;
}

function githubAnnotation(hit) {
  const { rule } = hit;
  const command = rule.level === 'warn' ? 'warning' : 'error';
  const subject = hit.line === 0 ? 'Superseded file path' : 'Superseded identifier';
  const message = `${subject} ${describePattern(rule)} [${rule.area}]: ${rule.note}`
    .replace(/%/g, '%25')
    .replace(/\r/g, '%0D')
    .replace(/\n/g, '%0A');
  const location = hit.line === 0 ? `file=${hit.path}` : `file=${hit.path},line=${hit.line},col=${hit.column}`;
  return `::${command} ${location}::${message}`;
}

function repoRoot(start) {
  try {
    return execFileSync('git', ['rev-parse', '--show-toplevel'], {
      cwd: start,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    }).trim();
  } catch {
    throw new SpecError([`${start}: not inside a git checkout (git rev-parse failed)`]);
  }
}

function trackedFiles(root) {
  const out = execFileSync('git', ['ls-files', '-z'], {
    cwd: root,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  });
  return out.split('\0').filter(Boolean);
}

/** Read a tracked file for scanning, or null when its contents must be skipped. */
function readScannable(root, path) {
  if (isContentExcluded(path) || hasBinaryExtension(path)) return null;
  const full = join(root, path);
  let stat;
  try {
    stat = lstatSync(full);
  } catch {
    return null; // deleted in the working tree but still tracked
  }
  if (!stat.isFile() || stat.size > MAX_FILE_BYTES) return null; // symlinks, submodules, huge files
  const buffer = readFileSync(full);
  return looksBinary(buffer) ? null : buffer.toString('utf8');
}

/**
 * Scan the checkout at `root` against `specPath`; returns the process exit code.
 * `log` and `env` are injectable for the self-test.
 */
export function run({ root, specPath, log = console.log, env = process.env }) {
  const specRelative = relative(root, specPath).split('\\').join('/');
  const specDisplay = specRelative === '' || specRelative.startsWith('..') || isAbsolute(specRelative)
    ? specPath
    : specRelative;
  let raw;
  try {
    raw = readFileSync(specPath, 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') {
      log(`check-forbidden: ${specDisplay} not found; nothing to check yet.`);
      return 0;
    }
    throw err;
  }
  let spec;
  try {
    spec = JSON.parse(raw);
  } catch (err) {
    throw new SpecError([`${specDisplay}: invalid JSON: ${err.message}`]);
  }
  const rules = compileSpec(spec, specDisplay);

  const hits = [];
  let paths = 0;
  let scanned = 0;
  for (const path of trackedFiles(root)) {
    if (path === specDisplay || isExcluded(path)) continue; // incl. a --spec list inside the checkout
    paths += 1;
    hits.push(...scanPath(path, rules));
    const text = readScannable(root, path);
    if (text === null) continue;
    scanned += 1;
    hits.push(...scanText(path, text, rules));
  }

  const annotate = env.GITHUB_ACTIONS === 'true';
  for (const hit of hits) {
    log(formatHit(hit));
    if (annotate) log(githubAnnotation(hit));
  }
  const errors = hits.filter((h) => h.rule.level === 'error');
  const warnings = hits.length - errors.length;
  const files = new Set(errors.map((h) => h.path)).size;
  const summary =
    `check-forbidden: ${rules.length} identifier(s) from ${specDisplay}, ${paths} path(s) checked, ` +
    `${scanned} file(s) scanned: ${errors.length} error(s) in ${files} file(s), ${warnings} warning(s).`;
  log(summary);
  return errors.length > 0 ? 1 : 0;
}

// ---------------------------------------------------------------------------
// Self-test (--self-test): pure-function checks against an inline fixture, then
// an end-to-end run() over a throwaway git repository (needs git, like the scan).
// The fixture identifiers are made up, so this file never matches a real list.
// ---------------------------------------------------------------------------

const FIXTURE_SPEC = {
  version: '1',
  description: 'self-test fixture',
  identifiers: [
    { area: 'gateway', pattern: 'X-Legacy-Band', kind: 'literal', note: 'renamed header' },
    { area: 'dx', pattern: '^\\s*legacy_name:', kind: 'regex', note: 'compose key removed' },
    { area: 'frontend', pattern: '/legacyapi/', kind: 'regex', flags: 'i', note: 'route removed' },
    { area: 'engine', pattern: 'OLD_ENV_VAR', kind: 'literal', note: 'renamed', allow: ['contracts/vectors/**'] },
    { area: 'dx', pattern: 'soon_gone', kind: 'literal', note: 'removed in a later phase', level: 'warn' },
    { area: 'contract', pattern: 'a.b', kind: 'literal', note: 'literal dots are not wildcards' },
    { area: 'dx', pattern: 'docs/LEGACY.md', kind: 'literal', note: 'superseded doc' },
    { area: 'frontend', pattern: '(^|/)legacy-config\\.js$', kind: 'regex', note: 'superseded runtime file' },
  ],
};

const FIXTURE_FILES = {
  'backend-go/internal/gate/headers.go': 'package gate\n\nfunc set(w W) { w.Header().Set("X-Legacy-Band", "x") }\n',
  'docker-compose.yml': 'services:\n  proxy:\n    legacy_name: proxy\n',
  'frontend-react/src/api.ts': 'export const url = "/LegacyAPI/status";\r\nexport const ok = "a.b";\r\n',
  'contracts/vectors/decide/old.json': '{"env": "OLD_ENV_VAR"}\n',
  'backend-python/src/cfg.py': 'OLD_ENV_VAR = 1\nassert "X-Legacy-Band" not in h  # check-forbidden: ignore\naxb = 2\n',
  'scripts/tool.sh': 'echo soon_gone\n',
  'docs/archive/old.go': 'X-Legacy-Band\n',
  'docs/v2/notes.txt': 'X-Legacy-Band\n',
  'README.md': 'X-Legacy-Band\n',
  'frontend-react/docs/guide.md': 'X-Legacy-Band\n',
  'contracts/superseded.v1.json': '{"pattern": "X-Legacy-Band"}\n',
  'scripts/check-forbidden.mjs': 'X-Legacy-Band\n',
  // Path checks: a superseded file fails by existing, even as *.md (contents unscanned).
  'docs/LEGACY.md': 'prose that mentions X-Legacy-Band\n',
  'frontend-react/public/legacy-config.js': 'window.cfg = {};\n',
  'docs/archive/docs/LEGACY.md': 'excluded entirely\n',
  'contracts/vectors/lint/OLD_ENV_VAR.json': '{}\n',
};

const EXPECTED_HITS = [
  'backend-go/internal/gate/headers.go:3:33: error [gateway] "X-Legacy-Band" - renamed header',
  'docker-compose.yml:3:1: error [dx] /^\\s*legacy_name:/ - compose key removed',
  'frontend-react/src/api.ts:1:21: error [frontend] //legacyapi//i - route removed',
  'frontend-react/src/api.ts:2:20: error [contract] "a.b" - literal dots are not wildcards',
  'backend-python/src/cfg.py:1:1: error [engine] "OLD_ENV_VAR" - renamed',
  'scripts/tool.sh:1:6: warn [dx] "soon_gone" - removed in a later phase',
  'docs/LEGACY.md: error [dx] "docs/LEGACY.md" - superseded doc (file path)',
  'frontend-react/public/legacy-config.js: error [frontend] /(^|/)legacy-config\\.js$/ - superseded runtime file (file path)',
];

/**
 * End-to-end: run() over a throwaway git repository, so git ls-files, binary and
 * symlink skipping, the --spec self-skip, the summary and the exit codes are covered.
 */
function selfTestRun(check) {
  const dir = mkdtempSync(join(tmpdir(), 'check-forbidden-'));
  try {
    const git = (...args) => execFileSync('git', args, { cwd: dir, stdio: ['ignore', 'pipe', 'pipe'] });
    const write = (path, content) => {
      const full = join(dir, path);
      mkdirSync(dirname(full), { recursive: true });
      writeFileSync(full, content);
    };
    git('init', '-q');
    write('src/app.go', 'h.Set("X-Legacy-Band", v)\n');
    write('docs/LEGACY.md', 'X-Legacy-Band\n');
    write('README.md', 'X-Legacy-Band\n');
    write('assets/blob.dat', Buffer.from('X-Legacy-Band\0binary'));
    write('assets/logo.png', 'X-Legacy-Band\n');
    write('gone.go', 'X-Legacy-Band\n');
    write('scripts/tool.sh', 'echo soon_gone\n');
    write(SPEC_PATH, `${JSON.stringify(FIXTURE_SPEC)}\n`); // names every pattern, never scanned
    write('extra/list.json', '{}\n'); // tracked; becomes the --spec list below
    let symlinked = true;
    try {
      symlinkSync('app.go', join(dir, 'src/link.go'));
    } catch {
      symlinked = false; // e.g. Windows without symlink rights
    }
    git('add', '-A');
    unlinkSync(join(dir, 'gone.go')); // tracked but deleted in the working tree

    const scan = (specPath, env = {}) => {
      const out = [];
      const code = run({ root: dir, specPath, log: (line) => out.push(line), env });
      return { code, out };
    };

    const errorRun = scan(join(dir, SPEC_PATH), { GITHUB_ACTIONS: 'true' });
    check('run: exit 1 on error hits', errorRun.code === 1, `got ${errorRun.code}`);
    const expected = [
      'src/app.go:1:8: error [gateway] "X-Legacy-Band" - renamed header',
      '::error file=src/app.go,line=1,col=8::Superseded identifier "X-Legacy-Band" [gateway]: renamed header',
      'docs/LEGACY.md: error [dx] "docs/LEGACY.md" - superseded doc (file path)',
      '::error file=docs/LEGACY.md::Superseded file path "docs/LEGACY.md" [dx]: superseded doc',
      'scripts/tool.sh:1:6: warn [dx] "soon_gone" - removed in a later phase',
      '::warning file=scripts/tool.sh,line=1,col=6::Superseded identifier "soon_gone" [dx]: removed in a later phase',
    ];
    const hitLines = errorRun.out.slice(0, -1);
    const sameLines = JSON.stringify([...hitLines].sort()) === JSON.stringify([...expected].sort());
    check('run: expected hits and annotations', sameLines, `got ${JSON.stringify(hitLines)}`);
    // Paths: every tracked file but the list itself (the symlink adds one). Contents:
    // app.go, tool.sh and extra/list.json; not the *.md files, the binary blob, the
    // .png, the deleted file or the symlink.
    const pathsChecked = symlinked ? 9 : 8;
    check('run: summary line', errorRun.out.at(-1) ===
      `check-forbidden: ${FIXTURE_SPEC.identifiers.length} identifier(s) from ${SPEC_PATH}, ${pathsChecked} path(s) checked, ` +
      '3 file(s) scanned: 2 error(s) in 2 file(s), 1 warning(s).', `got ${errorRun.out.at(-1)}`);

    // A --spec list inside the checkout is skipped like the default one; warnings alone exit 0.
    const warnOnly = { ...FIXTURE_SPEC, identifiers: [FIXTURE_SPEC.identifiers.find((e) => e.level === 'warn')] };
    writeFileSync(join(dir, 'extra/list.json'), `${JSON.stringify(warnOnly)}\n`);
    const warnRun = scan(join(dir, 'extra/list.json'));
    check('run: warnings only exit 0', warnRun.code === 0, `got ${warnRun.code}`);
    check('run: --spec file is not scanned', !warnRun.out.some((l) => l.startsWith('extra/list.json')),
      `got ${JSON.stringify(warnRun.out)}`);
    check('run: no annotations outside GitHub Actions', !warnRun.out.some((l) => l.startsWith('::')));

    const missing = scan(join(dir, 'contracts/none.json'));
    const note = 'check-forbidden: contracts/none.json not found; nothing to check yet.';
    check('run: missing list exits 0 with a note', missing.code === 0 && missing.out.join('\n') === note,
      `got ${missing.code} ${JSON.stringify(missing.out)}`);

    writeFileSync(join(dir, 'extra/bad.json'), '{ not json');
    let error;
    try {
      scan(join(dir, 'extra/bad.json'));
    } catch (err) {
      error = err;
    }
    check('run: invalid JSON is a SpecError', error instanceof SpecError && /invalid JSON/.test(error.message),
      error ? error.message : 'no error thrown');

    // Invoked through a symlink (e.g. a symlinked checkout), the CLI must still run
    // rather than silently exit 0 without scanning.
    if (symlinked) {
      const link = join(dir, 'cli-link.mjs');
      symlinkSync(fileURLToPath(import.meta.url), link);
      const out = execFileSync(process.execPath, [link, '--help'], { encoding: 'utf8' });
      check('cli: runs when invoked through a symlink', out.trim() === USAGE, `got ${JSON.stringify(out)}`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const INVALID_SPECS = [
  [[], /top level must be a JSON object/],
  [{ version: 1, identifiers: [] }, /"version" must be the string "1"/],
  [{ version: '1', identifiers: [] }, /"identifiers" must be a non-empty array/],
  [{ version: '1', identifiers: [{}], extra: true }, /unknown top-level key "extra"/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'glob', note: 'n' }] }, /"kind" must be "literal" or "regex"/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: '', kind: 'literal', note: 'n' }] }, /"pattern" must be a non-empty string/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'literal' }] }, /"note" must be a non-empty string/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: '(', kind: 'regex', note: 'n' }] }, /invalid regex/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x?', kind: 'regex', note: 'n' }] }, /matches the empty string/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'regex', flags: 'g', note: 'n' }] }, /"flags" may only contain/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'literal', flags: 'i', note: 'n' }] }, /only valid with kind "regex"/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'literal', note: 'n', level: 'fatal' }] }, /"level" must be "error" or "warn"/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'literal', note: 'n', allow: 'docs/**' }] }, /"allow" must be an array/],
  [{ version: '1', identifiers: [{ area: 'dx', pattern: 'x', kind: 'literal', note: 'n', allowed: [] }] }, /unknown key "allowed"/],
];

const GLOB_CASES = [
  ['docs/archive/**', 'docs/archive/a/b.go', true],
  ['docs/archive/**', 'docs/archived.go', false],
  ['**/*.md', 'README.md', true],
  ['**/*.md', 'a/b/c.md', true],
  ['**/*.md', 'a/b/c.mdx', false],
  ['contracts/vectors/**', 'contracts/vectors/decide/x.json', true],
  ['src/*.ts', 'src/a.ts', true],
  ['src/*.ts', 'src/a/b.ts', false],
  ['src/?.ts', 'src/a.ts', true],
  ['a.b/c', 'aXb/c', false],
];

export function selfTest() {
  let passed = 0;
  const failures = [];
  const check = (name, ok, detail = '') => {
    if (ok) passed += 1;
    else failures.push(detail ? `${name}: ${detail}` : name);
  };

  for (const [glob, path, expected] of GLOB_CASES) {
    check(`glob ${glob} vs ${path}`, globToRegExp(glob).test(path) === expected, `expected ${expected}`);
  }

  for (const [spec, message] of INVALID_SPECS) {
    let error;
    try {
      compileSpec(spec, 'fixture.json');
    } catch (err) {
      error = err;
    }
    check(
      `invalid spec ${JSON.stringify(spec)}`,
      error instanceof SpecError && message.test(error.message),
      error ? `wrong error: ${error.message}` : 'no error thrown',
    );
  }

  const rules = compileSpec(FIXTURE_SPEC, 'fixture.json');
  check('fixture compiles', rules.length === FIXTURE_SPEC.identifiers.length);

  const hits = [];
  for (const [path, text] of Object.entries(FIXTURE_FILES)) {
    if (isExcluded(path)) continue;
    hits.push(...scanPath(path, rules));
    if (!isContentExcluded(path)) hits.push(...scanText(path, text, rules));
  }
  const actual = hits.map(formatHit);
  for (const line of EXPECTED_HITS) {
    check(`expected hit ${line}`, actual.includes(line), 'missing');
  }
  for (const line of actual) {
    check(`unexpected hit ${line}`, EXPECTED_HITS.includes(line), 'not expected');
  }

  check('binary detection', looksBinary(Buffer.from([0x89, 0x50, 0x00, 0x47])) && !looksBinary(Buffer.from('text')));
  check('annotation escaping', githubAnnotation({
    path: 'a.go', line: 1, column: 2,
    rule: { level: 'warn', area: 'dx', kind: 'literal', pattern: 'p', flags: '', note: '50%\nnext' },
  }) === '::warning file=a.go,line=1,col=2::Superseded identifier "p" [dx]: 50%25%0Anext');

  try {
    selfTestRun(check);
  } catch (err) {
    check('run: end-to-end fixture', false, `threw ${err.stack ?? err}`);
  }

  for (const failure of failures) console.error(`FAIL ${failure}`);
  console.log(`check-forbidden self-test: ${passed} passed, ${failures.length} failed.`);
  return failures.length === 0 ? 0 : 1;
}

// ---------------------------------------------------------------------------

const USAGE = `Usage: node scripts/check-forbidden.mjs [--self-test] [--spec <file>] [--root <dir>]`;

function main(argv) {
  let args;
  try {
    ({ values: args } = parseArgs({
      args: argv,
      options: {
        'self-test': { type: 'boolean' },
        spec: { type: 'string' },
        root: { type: 'string' },
        help: { type: 'boolean', short: 'h' },
      },
    }));
  } catch (err) {
    console.error(`check-forbidden: ${err.message}\n${USAGE}`);
    return 2;
  }
  if (args.help) {
    console.log(USAGE);
    return 0;
  }
  if (args['self-test']) return selfTest();

  try {
    const scriptDir = dirname(fileURLToPath(import.meta.url));
    const root = repoRoot(args.root ? resolve(args.root) : scriptDir);
    const specPath = args.spec ? resolve(args.spec) : join(root, SPEC_PATH);
    return run({ root, specPath });
  } catch (err) {
    if (err instanceof SpecError) {
      for (const problem of err.problems) console.error(`check-forbidden: ${problem}`);
      return 2;
    }
    throw err;
  }
}

/**
 * True when this file is the program being run. Node reports the entry module's URL
 * with symlinks resolved while argv[1] keeps the path as typed, so both sides are
 * compared as real paths; otherwise a symlinked checkout would silently skip the scan.
 */
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

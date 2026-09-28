/**
 * The demo recording's file set: what scripts/build-demo-recording.ts writes and its drift test
 * compares. Only this directory imports the decision core; the replay itself reads the files.
 */
import type { BuiltRecording } from './demoRecording';

export * from './demoRecording';

/** manifest.json and traces/<id>.json as written to disk (2-space JSON, trailing newline). */
export function serializeRecording(built: BuiltRecording): Record<string, string> {
  const files: Record<string, string> = { 'manifest.json': `${JSON.stringify(built.manifest, null, 2)}\n` };
  for (const d of built.details) files[`traces/${d.trace_id}.json`] = `${JSON.stringify(d, null, 2)}\n`;
  return files;
}

/** The manifest without its git revision, which changes with every commit. */
function withoutSha(text: string): string {
  try {
    const doc = JSON.parse(text) as Record<string, unknown>;
    return JSON.stringify({ ...doc, git_sha: null });
  } catch {
    return text;
  }
}

/** Whether two file sets hold the same recording (the manifest's git_sha aside). */
export function sameRecordingFiles(a: Record<string, string>, b: Record<string, string>): boolean {
  const names = Object.keys(a).sort();
  if (names.join('\n') !== Object.keys(b).sort().join('\n')) return false;
  return names.every((n) => (n === 'manifest.json' ? withoutSha(a[n]) === withoutSha(b[n]) : a[n] === b[n]));
}

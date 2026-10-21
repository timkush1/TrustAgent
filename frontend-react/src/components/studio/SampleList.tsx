/**
 * The test bench's samples (SPEC §7.3 step 3): where each came from, the answer, and the label the
 * viewer gives it (Should fire / Shouldn't fire / Not labelled), which the results and quick
 * calibration compare against. Samples come from the AI draft, from recent traffic, or pasted.
 * On the static site "recent traffic" is the replayed recording (D107): up to six distinct answers of
 * a route, labelled as coming from the simulated recording.
 */
import { Link } from '@tanstack/react-router';
import { useMutation } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
import { DEFAULT_ROUTE } from '@/components/policies/policyDraft';
import { describeError } from '@/components/common/describeError';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { IconButton } from '@/components/ui/icon-button';
import { Textarea } from '@/components/ui/input';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Select } from '@/components/ui/select';
import { Tab, TabPanel, Tabs, TabsList } from '@/components/ui/tabs';
import { STUDIO_TEST_MAX_SAMPLES } from '@/data/control/studioPlane';
import { formatCount } from '@/domain/format';
import type { StudioSample } from '@/types/api';
import { STUDIO_SAMPLE_FILTERS, studioExpectedWords, studioSampleFilterWords, studioStaticSamplesSource, type StudioSampleFilter } from './studioWords';

type LabelValue = 'fire' | 'no_fire' | 'none';
const LABELS: readonly { value: LabelValue; label: string }[] = [
  { value: 'fire', label: studioExpectedWords('fire') },
  { value: 'no_fire', label: studioExpectedWords('no_fire') },
  { value: 'none', label: studioExpectedWords(null) },
];

const ANY_ROUTE = '__any__';
type SampleWindow = '24h' | '7d' | '30d';

function sourceOf(s: StudioSample, fromRecording: boolean): string {
  if (s.trace_id) return fromRecording ? 'From the recording' : 'From traffic';
  if (s.id.startsWith('d')) return "From the AI's draft";
  return 'Pasted';
}

function withLabel(s: StudioSample, value: LabelValue): StudioSample {
  const { expected: _old, ...rest } = s;
  return value === 'none' ? rest : { ...rest, expected: value };
}

export function SampleList({
  samples,
  onChange,
  disabled,
  fromRecording = false,
}: {
  samples: readonly StudioSample[];
  onChange: (samples: StudioSample[]) => void;
  disabled: boolean;
  /** The static site: "traffic" samples come from the replayed recording. */
  fromRecording?: boolean;
}) {
  if (samples.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-border p-4 text-sm text-fg-muted">
        {fromRecording ? 'No samples yet. Load some from the recording or paste one below.' : 'No samples yet. Load some from recent traffic or paste one below.'}
      </p>
    );
  }
  return (
    <ol className="space-y-2" aria-label="Samples">
      {samples.map((s, i) => (
        <li key={s.id} className="space-y-2 rounded-md border border-border p-3">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted">
            <span>
              <span className="tabular font-medium text-fg">Sample {i + 1}</span> · {sourceOf(s, fromRecording)}
              {s.trace_id ? (
                <>
                  {' · '}
                  <Link to="/app/traffic/$traceId" params={{ traceId: s.trace_id }} className="font-medium text-fg underline underline-offset-2">
                    View trace
                  </Link>
                </>
              ) : null}
            </span>
            <span className="flex items-center gap-1">
              <SegmentedControl<LabelValue>
                aria-label={`Label for sample ${i + 1}`}
                size="sm"
                options={LABELS}
                value={s.expected ?? 'none'}
                disabled={disabled}
                onValueChange={(v) => onChange(samples.map((x) => (x.id === s.id ? withLabel(x, v) : x)))}
              />
              <IconButton icon={Trash2} size="sm" aria-label={`Remove sample ${i + 1}`} disabled={disabled} onClick={() => onChange(samples.filter((x) => x.id !== s.id))} />
            </span>
          </div>
          {s.user_request ? <p className="line-clamp-2 text-sm text-fg-muted">User: {s.user_request}</p> : null}
          <p className="line-clamp-3 text-md text-fg">{s.assistant_response}</p>
          {s.evidence && s.evidence.length > 0 ? (
            <p className="text-xs text-fg-muted">
              With {formatCount(s.evidence.length)} {s.evidence.length === 1 ? 'document' : 'documents'}
            </p>
          ) : null}
        </li>
      ))}
    </ol>
  );
}

function nextId(prefix: string, samples: readonly StudioSample[]): string {
  const used = new Set(samples.map((s) => s.id));
  for (let n = 1; ; n++) if (!used.has(`${prefix}${n}`)) return `${prefix}${n}`;
}

export function AddSamples({
  samples,
  onChange,
  routes,
  defaultRoute,
  writeReason,
  fromRecording = false,
}: {
  samples: readonly StudioSample[];
  onChange: (samples: StudioSample[]) => void;
  routes: readonly string[];
  defaultRoute: string | null;
  writeReason: Reason | null;
  /** The static site: samples come from the replayed recording, one route at a time (D107). */
  fromRecording?: boolean;
}) {
  const ds = useDataSource();
  const room = STUDIO_TEST_MAX_SAMPLES - samples.length;
  // The recording's samples are labelled with their route, so the static site always names one.
  const firstRoute = routes.find((r) => r !== DEFAULT_ROUTE) ?? routes[0] ?? ANY_ROUTE;
  const [route, setRoute] = useState(defaultRoute ?? (fromRecording ? firstRoute : ANY_ROUTE));
  const [since, setSince] = useState<SampleWindow>('7d');
  const [filter, setFilter] = useState<StudioSampleFilter>('flagged');
  const [request, setRequest] = useState('');
  const [answer, setAnswer] = useState('');
  const [doc, setDoc] = useState('');
  const [label, setLabel] = useState<LabelValue>('none');
  /** How many of the last load's answers were new to the bench (the rest were on it already). */
  const [added, setAdded] = useState<number | null>(null);

  const load = useMutation({
    mutationFn: () =>
      ds.getStudioSamples({ window: since, filter, limit: Math.min(20, room), ...(route === ANY_ROUTE ? {} : { route }) }),
    onSuccess: (res) => {
      const known = new Set(samples.map((s) => s.trace_id ?? s.id));
      const fresh: StudioSample[] = [];
      for (const s of res.samples) {
        if (known.has(s.trace_id ?? s.id)) continue;
        // Server ids are unique per response only: give each a bench id of its own.
        fresh.push({ ...s, id: nextId('t', [...samples, ...fresh]) });
      }
      const take = fresh.slice(0, room);
      setAdded(take.length);
      onChange([...samples, ...take]);
    },
  });

  const add = () => {
    const sample: StudioSample = { id: nextId('p', samples), assistant_response: answer.trim() };
    if (request.trim()) sample.user_request = request.trim();
    if (doc.trim()) sample.evidence = [{ id: `${sample.id}-doc`, text: doc.trim() }];
    if (label !== 'none') sample.expected = label;
    onChange([...samples, sample]);
    setRequest('');
    setAnswer('');
    setDoc('');
    setLabel('none');
  };

  const full: Reason | null = room <= 0 ? { text: `A test takes at most ${STUDIO_TEST_MAX_SAMPLES} samples` } : null;
  const loadReason = writeReason ?? full;
  const failure = load.error ? describeError(load.error) : null;
  const loadedNone = load.isSuccess && load.data.samples.length === 0;

  return (
    <Tabs defaultValue="traffic">
      <TabsList aria-label="Add samples">
        <Tab value="traffic">{fromRecording ? 'From the recording' : 'From recent traffic'}</Tab>
        <Tab value="paste">Paste your own</Tab>
      </TabsList>
      <TabPanel value="traffic" className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Route">
            <Select
              value={route}
              onValueChange={(v) => setRoute(v ?? (fromRecording ? firstRoute : ANY_ROUTE))}
              options={[...(fromRecording ? [] : [{ value: ANY_ROUTE, label: 'Any route' }]), ...routes.map((r) => ({ value: r, label: r }))]}
            />
          </Field>
          <Field label="Which answers">
            <Select
              value={filter}
              onValueChange={(v) => v && setFilter(v)}
              options={STUDIO_SAMPLE_FILTERS.map((f) => ({ value: f, ...studioSampleFilterWords(f) }))}
            />
          </Field>
          {fromRecording ? null : (
          <div className="space-y-1.5">
            <p className="text-sm font-medium text-fg">From the last</p>
            <SegmentedControl<SampleWindow>
              aria-label="From the last"
              options={[
                { value: '24h', label: '24 h' },
                { value: '7d', label: '7 days' },
                { value: '30d', label: '30 days' },
              ]}
              value={since}
              onValueChange={setSince}
            />
          </div>
          )}
        </div>
        <DisabledAction reason={loadReason}>
          {(p) => (
            <Button pending={load.isPending} onClick={() => load.mutate()} {...p}>
              Load samples
            </Button>
          )}
        </DisabledAction>
        <p className="text-xs text-fg-muted">
          {fromRecording
            ? `${studioStaticSamplesSource(route === ANY_ROUTE ? firstRoute : route)}. Free: nothing is sent; up to 6 distinct answers per load.`
            : 'Free: reads answers the gateway already stored. Only answers whose text was kept can be loaded.'}
        </p>
        {loadedNone ? (
          <p className="text-sm text-fg">{fromRecording ? 'No answers of this route in the recording match. Try another filter or route.' : 'No stored answers match. Try another filter or a longer window.'}</p>
        ) : null}
        {load.isSuccess && !loadedNone && added !== null ? (
          <p role="status" className="text-sm text-fg">
            {added === 0
              ? 'Every answer that matched is already in the samples above.'
              : `Added ${formatCount(added)} ${added === 1 ? 'answer' : 'answers'} to the samples above.`}
          </p>
        ) : null}
        {failure ? (
          <p role="alert" className="text-sm text-fg">
            Couldn't load samples: {failure.message}
            {failure.code ? <span className="ml-1 font-mono text-2xs text-fg-subtle">{failure.code}</span> : null}
          </p>
        ) : null}
      </TabPanel>
      <TabPanel value="paste" className="space-y-3">
        <Field label="The user's message" optional>
          <Textarea rows={2} value={request} onChange={(e) => setRequest(e.target.value)} />
        </Field>
        <Field label="The answer to check">
          <Textarea rows={3} value={answer} onChange={(e) => setAnswer(e.target.value)} />
        </Field>
        <Field label="A document the answer should rely on" optional description="Only checks that read the app's documents use it.">
          <Textarea rows={2} value={doc} onChange={(e) => setDoc(e.target.value)} />
        </Field>
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1.5">
            <p className="text-sm font-medium text-fg">Your label</p>
            <SegmentedControl<LabelValue> aria-label="Your label" size="sm" options={LABELS} value={label} onValueChange={setLabel} />
          </div>
          <DisabledAction reason={full ?? (answer.trim() ? null : { text: 'Paste an answer first' })}>
            {(p) => (
              <Button onClick={add} {...p}>
                Add sample
              </Button>
            )}
          </DisabledAction>
        </div>
      </TabPanel>
    </Tabs>
  );
}

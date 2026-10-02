/**
 * The test bench's samples (SPEC §7.3 step 3): where each came from, the answer, and the label the
 * viewer gives it (Should fire / Shouldn't fire / Not labelled), which the results and quick
 * calibration compare against. Samples come from the AI draft, from recent traffic, or pasted.
 */
import { Link } from '@tanstack/react-router';
import { useMutation } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { DisabledAction, type Reason } from '@/components/common/DisabledReason';
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
import { STUDIO_SAMPLE_FILTERS, studioExpectedWords, studioSampleFilterWords, type StudioSampleFilter } from '@/domain/presentation';
import type { StudioSample } from '@/types/api';

type LabelValue = 'fire' | 'no_fire' | 'none';
const LABELS: readonly { value: LabelValue; label: string }[] = [
  { value: 'fire', label: studioExpectedWords('fire') },
  { value: 'no_fire', label: studioExpectedWords('no_fire') },
  { value: 'none', label: studioExpectedWords(null) },
];

const ANY_ROUTE = '__any__';
type SampleWindow = '24h' | '7d' | '30d';

function sourceOf(s: StudioSample): string {
  if (s.trace_id) return 'From traffic';
  if (s.id.startsWith('d')) return "From the AI's draft";
  return 'Pasted';
}

function withLabel(s: StudioSample, value: LabelValue): StudioSample {
  const { expected: _old, ...rest } = s;
  return value === 'none' ? rest : { ...rest, expected: value };
}

export function SampleList({ samples, onChange, disabled }: { samples: readonly StudioSample[]; onChange: (samples: StudioSample[]) => void; disabled: boolean }) {
  if (samples.length === 0) {
    return <p className="rounded-md border border-dashed border-border p-4 text-sm text-fg-muted">No samples yet. Load some from recent traffic or paste one below.</p>;
  }
  return (
    <ol className="space-y-2" aria-label="Samples">
      {samples.map((s, i) => (
        <li key={s.id} className="space-y-2 rounded-md border border-border p-3">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted">
            <span>
              <span className="tabular font-medium text-fg">Sample {i + 1}</span> · {sourceOf(s)}
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
}: {
  samples: readonly StudioSample[];
  onChange: (samples: StudioSample[]) => void;
  routes: readonly string[];
  defaultRoute: string | null;
  writeReason: Reason | null;
}) {
  const ds = useDataSource();
  const room = STUDIO_TEST_MAX_SAMPLES - samples.length;
  const [route, setRoute] = useState(defaultRoute ?? ANY_ROUTE);
  const [since, setSince] = useState<SampleWindow>('7d');
  const [filter, setFilter] = useState<StudioSampleFilter>('flagged');
  const [request, setRequest] = useState('');
  const [answer, setAnswer] = useState('');
  const [doc, setDoc] = useState('');
  const [label, setLabel] = useState<LabelValue>('none');

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
      onChange([...samples, ...fresh.slice(0, room)]);
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
        <Tab value="traffic">From recent traffic</Tab>
        <Tab value="paste">Paste your own</Tab>
      </TabsList>
      <TabPanel value="traffic" className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Route">
            <Select value={route} onValueChange={(v) => setRoute(v ?? ANY_ROUTE)} options={[{ value: ANY_ROUTE, label: 'Any route' }, ...routes.map((r) => ({ value: r, label: r }))]} />
          </Field>
          <Field label="Which answers">
            <Select
              value={filter}
              onValueChange={(v) => v && setFilter(v)}
              options={STUDIO_SAMPLE_FILTERS.map((f) => ({ value: f, ...studioSampleFilterWords(f) }))}
            />
          </Field>
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
        </div>
        <DisabledAction reason={loadReason}>
          {(p) => (
            <Button pending={load.isPending} onClick={() => load.mutate()} {...p}>
              Load samples
            </Button>
          )}
        </DisabledAction>
        <p className="text-xs text-fg-muted">Free: reads answers the gateway already stored. Only answers whose text was kept can be loaded.</p>
        {loadedNone ? <p className="text-sm text-fg">No stored answers match. Try another filter or a longer window.</p> : null}
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

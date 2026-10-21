/**
 * The Playground form (SPEC §3.3 Playground): chat through your model or audit a pasted answer, on
 * any route and model, with the conversation, the documents to check against, streaming, and an
 * optional stricter mode. It lives in a store, so it is still there after a run or a visit to
 * another page.
 */
import { FilePlus2, MessageSquarePlus, RotateCcw, Trash2 } from 'lucide-react';
import { useId } from 'react';
import { Term } from '@/components/common/Term';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { IconButton } from '@/components/ui/icon-button';
import { Input, Textarea } from '@/components/ui/input';
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu';
import { SegmentedControl } from '@/components/ui/segmented-control';
import { Select } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { modeView, POLICY_MODES } from '@/domain/controlPresentation';
import { formatCount } from '@/domain/format';
import { cn } from '@/lib/cn';
import type { ChatMessage, PolicyMode } from '@/types/api';
import { playgroundExamples, textLength, type PlaygroundMode } from './playgroundModel';
import { usePlaygroundStore } from './playgroundStore';

const ROLE_OPTIONS: { value: ChatMessage['role']; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'user', label: 'User' },
  { value: 'assistant', label: 'Assistant' },
];

export interface PlaygroundFormProps {
  /** Route names from the policy, or null when it could not be read (free text then). */
  routes: readonly string[] | null;
  maxChars: number | null;
  /** Everything is locked while a run is in flight. */
  busy: boolean;
  /** A demo or test-mode stack: an unedited example is answered with its scenario's scripted checks. */
  scripted?: boolean;
  /** The knowledge add-on is on: its scenarios are examples too. */
  knowledge?: boolean;
  /** The routes the gateway serves, once its policy is read (null: unknown); examples on other routes are not offered. */
  servedRoutes?: readonly string[] | null;
}

export function PlaygroundFormView({ routes, maxChars, busy, scripted = false, knowledge = false, servedRoutes = null }: PlaygroundFormProps) {
  const examples = playgroundExamples({ knowledge, routes: servedRoutes });
  const { form, set, setMessage, addMessage, removeMessage, setDoc, addDoc, removeDoc, load, reset } = usePlaygroundStore();
  const modelListId = useId();
  const length = textLength(form);
  const over = maxChars !== null && length > maxChars;

  return (
    <fieldset disabled={busy} className="min-w-0 space-y-5">
      <legend className="sr-only">Request</legend>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SegmentedControl<PlaygroundMode>
          aria-label="What to try"
          options={[
            { value: 'chat', label: 'Chat with your model' },
            { value: 'audit', label: 'Check an answer' },
          ]}
          value={form.mode}
          onValueChange={(m) => set('mode', m)}
          disabled={busy}
        />
        <div className="flex items-center gap-1">
          <Menu>
            <MenuTrigger render={<Button size="sm" variant="ghost">Load an example</Button>} />
            <MenuContent>
              {examples.map((ex) => (
                <MenuItem key={ex.id} onClick={() => load(ex.form)}>
                  {ex.title}
                </MenuItem>
              ))}
            </MenuContent>
          </Menu>
          <Button size="sm" variant="ghost" onClick={reset}>
            <RotateCcw aria-hidden="true" />
            Clear form
          </Button>
        </div>
      </div>
      <p className="text-sm text-fg-muted">
        {form.mode === 'chat'
          ? 'Your request goes to your model through the gateway, then the answer is checked exactly as your app’s would be.'
          : 'No model call: paste an answer and see how the checks judge it against the documents.'}
      </p>
      {scripted && form.scenario ? (
        <p className="text-xs text-fg-muted">
          Example “{examples.find((e) => e.id === form.scenario)?.title ?? form.scenario}”: this stack’s mock checks answer it with the
          scenario’s scripted results (simulated). Editing the example makes it your own request.
        </p>
      ) : null}

      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="Route"
          description={
            <>
              Which <Term id="policy">policy</Term> applies.
            </>
          }
        >
          {routes ? (
            <Select
              value={form.route || '__rules__'}
              onValueChange={(v) => set('route', !v || v === '__rules__' ? '' : v)}
              options={[{ value: '__rules__', label: 'Let the route rules decide' }, ...routes.map((r) => ({ value: r, label: r }))]}
            />
          ) : (
            <Input value={form.route} onValueChange={(v) => set('route', v)} placeholder="support-bot" />
          )}
        </Field>
        <Field label="Model" optional description={form.mode === 'chat' ? 'Sent to your model as is.' : 'Recorded with the check.'}>
          <Input value={form.model} onValueChange={(v) => set('model', v)} placeholder="gpt-4o-mini" list={modelListId} autoComplete="off" />
          <datalist id={modelListId}>
            <option value="gpt-4o-mini" />
            <option value="gpt-4o" />
            <option value="claude-haiku-4-5" />
          </datalist>
        </Field>
      </div>

      <section aria-label="Conversation" className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold text-fg">Conversation</h3>
          <Button size="sm" variant="ghost" onClick={addMessage}>
            <MessageSquarePlus aria-hidden="true" />
            Add message
          </Button>
        </div>
        <ol className="space-y-3">
          {form.messages.map((m, i) => (
            <li key={i} className="space-y-1.5 rounded-lg border border-border bg-surface p-3">
              <div className="flex items-center justify-between gap-2">
                <Select<ChatMessage['role']>
                  aria-label={`Message ${i + 1} role`}
                  value={m.role}
                  onValueChange={(role) => role && setMessage(i, { role })}
                  options={ROLE_OPTIONS}
                  className="w-32"
                />
                {form.messages.length > 1 ? (
                  <IconButton icon={Trash2} aria-label={`Remove message ${i + 1}`} size="sm" onClick={() => removeMessage(i)} />
                ) : null}
              </div>
              <Field label={`Message ${i + 1}`} hideLabel>
                <Textarea
                  value={m.content}
                  onValueChange={(v) => setMessage(i, { content: v })}
                  rows={m.role === 'system' ? 3 : 2}
                  placeholder={m.role === 'system' ? 'You are the Acme support assistant. Answer only from the documents below.' : 'Can I still get a refund on my annual plan?'}
                />
              </Field>
            </li>
          ))}
        </ol>
      </section>

      {form.mode === 'audit' ? (
        <Field label="The answer to check" description="As your model wrote it.">
          <Textarea value={form.response} onValueChange={(v) => set('response', v)} rows={4} placeholder="You can get a full refund within 60 days of purchase." />
        </Field>
      ) : null}

      <section aria-label="Documents" className="space-y-3">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h3 className="text-sm font-semibold text-fg">
              <Term id="evidence">Documents</Term> to check against
            </h3>
            <p className="text-xs text-fg-muted">Sent as trustagent.context, next to any documents in the system prompt.</p>
          </div>
          <Button size="sm" variant="ghost" onClick={addDoc}>
            <FilePlus2 aria-hidden="true" />
            Add document
          </Button>
        </div>
        {form.evidence.length === 0 ? (
          <p className="text-sm text-fg-muted">
            None added here. Documents in the system prompt still count; with none at all, groundedness is marked not checked
            instead of guessed.
          </p>
        ) : (
          <ol className="space-y-3">
            {form.evidence.map((d, i) => (
              <li key={i} className="space-y-1.5 rounded-lg border border-border bg-surface p-3">
                <div className="flex items-end gap-2">
                  <Field label={`Document ${i + 1} name`} optional className="flex-1">
                    <Input value={d.id} onValueChange={(v) => setDoc(i, { id: v })} placeholder="refund-policy.md" />
                  </Field>
                  <IconButton icon={Trash2} aria-label={`Remove document ${i + 1}`} size="sm" onClick={() => removeDoc(i)} />
                </div>
                <Field label={`Document ${i + 1} text`} hideLabel>
                  <Textarea value={d.text} onValueChange={(v) => setDoc(i, { text: v })} rows={3} placeholder="Annual plans can be refunded within 30 days of purchase." />
                </Field>
              </li>
            ))}
          </ol>
        )}
      </section>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Act as" description="Try a stricter mode without changing the policy.">
          <Select<PolicyMode | 'route'>
            value={form.modeOverride || 'route'}
            onValueChange={(v) => set('modeOverride', !v || v === 'route' ? '' : v)}
            options={[
              { value: 'route', label: 'The route’s own mode' },
              ...POLICY_MODES.map((m) => ({ value: m, label: modeView(m).label, description: modeView(m).description })),
            ]}
          />
        </Field>
        {form.mode === 'chat' ? (
          <Switch
            label="Stream the answer"
            description="See it arrive token by token, as a streaming client would."
            checked={form.stream}
            onCheckedChange={(v) => set('stream', v)}
            className="self-center"
          />
        ) : null}
      </div>

      <p className={cn('text-xs tabular', over ? 'font-medium text-fg' : 'text-fg-muted')} aria-live="polite">
        {formatCount(length)}
        {maxChars !== null ? ` of ${formatCount(maxChars)}` : ''} characters
        {over ? ' — too long to check' : ''}
      </p>
    </fieldset>
  );
}

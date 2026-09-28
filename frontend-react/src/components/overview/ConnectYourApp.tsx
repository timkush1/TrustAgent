/**
 * "Connect your app" (SPEC §3.3): point an SDK at the gateway. Tabs for OpenAI Python, OpenAI TS,
 * Anthropic, curl and LangChain show the base URL, the `X-TrustAgent-Key` header (read from an
 * environment variable, never printed) and the `trustagent.context` extension. A status line waits
 * for the first request and turns to "received" as soon as a trace arrives on the live stream.
 *
 * On the demo stack the card says what the demo is instead (mock checks, $0) and how to switch to
 * real keys.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, CircleCheck, FlaskConical } from 'lucide-react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { KeyValueList } from '@/components/common/KeyValue';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Spinner } from '@/components/ui/spinner';
import { Tab, TabPanel, Tabs, TabsList } from '@/components/ui/tabs';
import { buttonVariants } from '@/components/ui/button-variants';
import { copy, fillTemplate, splitCommands } from '@/domain/presentation';
import { INGEST_KEY_ENV, SNIPPET_TABS, snippet } from '@/domain/snippets';
import { cn } from '@/lib/cn';

export interface ConnectYourAppProps {
  /** Gateway origin the snippets point at ("http://localhost:3000"). */
  origin: string;
  demo: boolean;
  /** A request has reached the gateway (live stream or totals). */
  received: boolean;
  className?: string;
}

function CommandText({ text }: { text: string }) {
  return (
    <>
      {splitCommands(text).map((part, i) =>
        part.command ? (
          <code key={i} className="rounded-sm bg-surface-sunken px-1 font-mono text-xs text-fg">
            {part.text}
          </code>
        ) : (
          <span key={i}>{part.text}</span>
        ),
      )}
    </>
  );
}

function FirstRequestStatus({ received }: { received: boolean }) {
  return (
    <div role="status" aria-live="polite" className="flex flex-wrap items-center gap-2 text-sm">
      {received ? (
        <>
          <CircleCheck aria-hidden="true" className="size-4 text-tone-success" />
          <span className="font-medium text-fg">{copy.connect_app.received}</span>
          <Link to="/app/traffic" className="inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2">
            See it in Traffic
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        </>
      ) : (
        <>
          <Spinner size="sm" />
          <span className="text-fg-muted">{copy.connect_app.waiting}</span>
        </>
      )}
    </div>
  );
}

export function ConnectYourApp({ origin, demo, received, className }: ConnectYourAppProps) {
  const playground = (
    <Link to="/app/playground" className={cn(buttonVariants({ variant: 'secondary', size: 'sm' }))}>
      <FlaskConical aria-hidden="true" />
      Try the Playground
    </Link>
  );

  if (demo) {
    return (
      <Card className={className}>
        <CardHeader title={copy.connect_app.title} actions={playground} />
        <CardContent className="space-y-3">
          <p className="text-sm text-fg">
            <CommandText text={fillTemplate(copy.connect_app.demo_stack)} />
          </p>
          <FirstRequestStatus received={received} />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className={className}>
      <CardHeader
        title={copy.connect_app.title}
        description="Send your app's requests through the gateway: change the base URL and add one header."
        actions={playground}
      />
      <CardContent className="space-y-4">
        <KeyValueList
          items={[
            { label: 'Base URL', value: `${origin}/v1`, mono: true, copy: true },
            { label: 'Key header', value: `X-TrustAgent-Key: $${INGEST_KEY_ENV}`, mono: true, hint: 'an ingest key from Settings → Access' },
          ]}
        />
        <Tabs defaultValue="openai-python">
          <TabsList aria-label="Snippet language" className="overflow-x-auto">
            {SNIPPET_TABS.map((t) => (
              <Tab key={t.id} value={t.id}>
                {t.label}
              </Tab>
            ))}
          </TabsList>
          {SNIPPET_TABS.map((t) => (
            <TabPanel key={t.id} value={t.id}>
              <CodeBlock label={`${t.label} (${t.language})`} code={snippet(t.id, { origin })} maxHeight="22rem" />
            </TabPanel>
          ))}
        </Tabs>
        <FirstRequestStatus received={received} />
      </CardContent>
    </Card>
  );
}

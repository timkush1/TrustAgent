/**
 * "Connect your app" (SPEC §3.3): point an SDK at the gateway. Tabs for OpenAI Python, OpenAI TS,
 * Anthropic, curl and LangChain show the base URL, the `X-TrustAgent-Key` header (read from an
 * environment variable, never printed) and the `trustagent.context` extension. A status line waits
 * for the first request and turns to "received" as soon as a trace arrives on the live stream.
 *
 * The key line names the app key to use (its label and prefix, never the secret) and links to
 * Settings → Access; when no app key is active an admin can create one here, shown once
 * (P4-PLAN ON-8, D87).
 *
 * On the demo stack the card says what the demo is instead (mock checks, $0) and how to switch to
 * real keys.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, CircleCheck, FlaskConical, KeyRound } from 'lucide-react';
import { useState } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { CommandText } from '@/components/common/CommandText';
import { describeError } from '@/components/common/describeError';
import { KeyValueList } from '@/components/common/KeyValue';
import { SecretOnce } from '@/components/common/SecretOnce';
import { useWriteReason } from '@/components/common/useWriteAccess';
import { DisabledAction } from '@/components/common/DisabledReason';
import { Button } from '@/components/ui/button';
import { apiKeysQuery, qkcSecurity } from '@/data/queries/security';
import { useSystemStatus } from '@/data/useSystem';
import type { CreatedApiKey } from '@/types/api';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Spinner } from '@/components/ui/spinner';
import { Tab, TabPanel, Tabs, TabsList } from '@/components/ui/tabs';
import { buttonVariants } from '@/components/ui/button-variants';
import { copy, fillTemplate } from '@/domain/presentation';
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

const accessLink = 'font-medium text-fg underline underline-offset-2';

function AccessLink({ children }: { children: string }) {
  return (
    <Link to="/app/settings/$section" params={{ section: 'access' }} className={accessLink}>
      {children}
    </Link>
  );
}

/** The app key for the snippets: the active one, or (for an admin) one created here and shown once. */
function useAppKey(enabled: boolean) {
  const ds = useDataSource();
  const qc = useQueryClient();
  const { status } = useSystemStatus();
  const admin = status ? status.auth.caller_scopes.includes('admin') : false;
  const writeReason = useWriteReason('admin');
  const keys = useQuery({ ...apiKeysQuery(ds), enabled: enabled && admin });
  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [saved, setSaved] = useState(false);
  const mint = useMutation({
    mutationFn: () => ds.createApiKey({ label: 'my-app', scopes: ['ingest'] }),
    onSuccess: (key) => {
      setCreated(key);
      void qc.invalidateQueries({ queryKey: qkcSecurity.apiKeys });
    },
  });
  // An app key sends requests and nothing else; the admin key is never suggested for an app.
  const active = keys.data?.items.find((k) => k.revoked_at === null && k.scopes.includes('ingest') && !k.scopes.includes('admin')) ?? null;
  const canMint = admin && keys.isSuccess && !active && !created;
  return { active, created, saved, setSaved, mint, writeReason, canMint };
}

type AppKey = ReturnType<typeof useAppKey>;

/** The key line's hint: which key, with its prefix, and where keys are managed. */
function AppKeyHint({ appKey }: { appKey: AppKey }) {
  const { active, created } = appKey;
  const key = created ?? active;
  if (key) {
    return (
      <span>
        the key “{key.label}” (<code className="font-mono">{key.prefix}…</code>) from <AccessLink>Settings → Access</AccessLink>
      </span>
    );
  }
  return (
    <span>
      an app key from <AccessLink>Settings → Access</AccessLink>
    </span>
  );
}

/** "Create an app key" when none is active (D87), and the new key shown once. */
function AppKeyCreate({ appKey }: { appKey: AppKey }) {
  const { created, saved, setSaved, mint, writeReason, canMint } = appKey;
  if (created) {
    return <SecretOnce title={`${created.label} (shown once)`} value={created.secret} note={copy.setup.step_try.ingest_key} copyLabel="Copy the app key" saved={saved} onSavedChange={setSaved} />;
  }
  if (!canMint) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <span className="text-fg-muted">No app key yet.</span>
      <DisabledAction reason={writeReason}>
        {(p) => (
          <Button size="sm" onClick={() => mint.mutate()} pending={mint.isPending} {...p}>
            <KeyRound aria-hidden="true" />
            Create an app key
          </Button>
        )}
      </DisabledAction>
      {mint.isError ? (
        <span role="alert" className="text-fg">
          {describeError(mint.error).message}
        </span>
      ) : null}
    </div>
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
  const appKey = useAppKey(!demo);
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
            { label: 'Key header', value: `X-TrustAgent-Key: $${INGEST_KEY_ENV}`, mono: true, hint: <AppKeyHint appKey={appKey} /> },
          ]}
        />
        <AppKeyCreate appKey={appKey} />
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

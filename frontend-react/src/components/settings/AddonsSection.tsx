/**
 * Settings → Add-ons (SPEC §3.3; P4-PLAN ON-4): the optional compose profiles, each with whether it
 * is on here and the one command that turns it on. Add-ons change containers, so they are started
 * from the shell, never from the browser.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, BookOpen, Cpu, LineChart } from 'lucide-react';
import type { ReactNode } from 'react';
import { CodeBlock } from '@/components/common/CodeBlock';
import { DegradedState } from '@/components/common/DegradedState';
import { PageHeader } from '@/components/common/PageHeader';
import { LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { useAppMode, useSystemStatus } from '@/data/useSystem';
import { formatCount } from '@/domain/format';
import { commandText } from '@/domain/presentation';
import type { SystemStatus } from '@/types/api';

const linkClass = 'inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2';

type AddonState = 'on' | 'off' | 'unknown';

function StateBadge({ state }: { state: AddonState }) {
  if (state === 'on') return <Badge tone="success" emphasis="tint">On</Badge>;
  if (state === 'off') return <Badge emphasis="quiet">Off</Badge>;
  return <Badge emphasis="quiet">Unknown</Badge>;
}

function Addon({
  icon: Icon,
  title,
  description,
  state,
  detail,
  commandId,
  next,
}: {
  icon: typeof BookOpen;
  title: string;
  description: string;
  /** null: this page cannot see it, so no badge. */
  state: AddonState | null;
  detail?: ReactNode;
  commandId: string;
  next?: ReactNode;
}) {
  const command = commandText(commandId);
  return (
    <Card>
      <CardHeader
        level={2}
        title={
          <span className="flex items-center gap-2">
            <Icon aria-hidden="true" className="size-4 text-fg-muted" />
            {title}
          </span>
        }
        description={description}
        actions={state ? <StateBadge state={state} /> : null}
      />
      <CardContent className="space-y-3">
        {detail}
        {state !== 'on' && command ? (
          <>
            <CodeBlock label={`Turn on: ${title}`} code={command} />
            {next ? <p className="text-sm text-fg-muted">{next}</p> : null}
          </>
        ) : null}
      </CardContent>
    </Card>
  );
}

function knowledgeState(status: SystemStatus | null): AddonState {
  if (!status) return 'unknown';
  return status.knowledge.enabled ? 'on' : 'off';
}

function localLlmState(status: SystemStatus | null): AddonState {
  if (!status) return 'unknown';
  return status.judge.provider === 'ollama' ? 'on' : 'off';
}

export function AddonsSection() {
  const { status, unavailable } = useSystemStatus();
  const mode = useAppMode();
  const pending = !status && !unavailable;
  const showLoading = useDelayedLoading(pending);
  if (pending) return showLoading ? <LoadingState rows={3} /> : null;
  const kb = knowledgeState(status);
  return (
    <div className="space-y-5">
      <PageHeader description="Optional parts you can add to this stack. Each runs in its own containers, so you turn it on from the shell." />
      {unavailable ? (
        <DegradedState
          title="Can't tell which add-ons are on"
          reason="This proxy does not report its status to this browser."
          stillWorks="The commands below still turn each add-on on."
        />
      ) : null}
      {mode.demo ? (
        <p className="text-sm text-fg-muted">The demo stack runs without add-ons. Start your own stack first, then add them.</p>
      ) : null}
      <Addon
        icon={BookOpen}
        title="Knowledge base"
        description="Checks answers against documents you upload too, not only the ones each request carries. Adds a vector store and an embedding model."
        state={kb}
        detail={
          kb === 'on' && status ? (
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg">
              <span className="tabular">{formatCount(status.knowledge.kb_claims ?? null)} claims</span>
              {status.knowledge.embedding_model ? <span className="font-mono text-xs text-fg-muted">{status.knowledge.embedding_model}</span> : null}
              <Link to="/app/knowledge" className={linkClass}>
                Open Knowledge
                <ArrowRight aria-hidden="true" className="size-3.5" />
              </Link>
            </p>
          ) : null
        }
        commandId="knowledge_up"
      />
      <Addon
        icon={Cpu}
        title="Local LLM"
        description="Runs the deep-audit judge on your own machine with Ollama: no judge key, nothing leaves the host. Slower and less accurate than a hosted judge."
        state={localLlmState(status)}
        detail={localLlmState(status) === 'on' && status?.judge.model ? <p className="font-mono text-xs text-fg-muted">{status.judge.model}</p> : null}
        commandId="local_llm_up"
        next={
          <>
            Then choose Ollama as the judge in{' '}
            <Link to="/app/settings/$section" params={{ section: 'models' }} className={linkClass}>
              Models
            </Link>
            .
          </>
        }
      />
      <Addon
        icon={LineChart}
        title="Metrics dashboards"
        description="Prometheus and Grafana with the gateway and engine metrics, for long-term charts and alerts."
        state={null}
        commandId="observability_up"
      />
    </div>
  );
}

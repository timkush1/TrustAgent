/** Section 7 (SPEC §8.1): run it yourself, from the zero-key demo to your own stack. */
import { claim } from './claims';
import { command, copy, REPO_URL } from './content';
import { ClaimValue, Command, GitHubMark, Section } from './ui';
import { buttonClass } from './styles';

export function SelfHost() {
  const demoCost = claim('demo_cost_usd');
  return (
    <Section
      id="self-host"
      eyebrow="Run it yourself"
      title="On your laptop in one command."
      lead="Everything on this site runs locally with Docker. The demo needs no API keys; your own stack needs one OpenRouter key."
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4">
          <h3 className="text-lg font-semibold text-fg">The zero-key demo</h3>
          <div className="mt-4">
            <Command command={command('demo')} />
          </div>
          <ul className="mt-5 space-y-2 text-fg-muted">
            <li className="flex flex-wrap items-baseline gap-x-1.5">
              Docker only, costs <ClaimValue claim={demoCost} />
            </li>
            <li>Mock checks, a mock judge and scripted traffic; every number it shows is labelled simulated.</li>
            <li>
              Opens the dashboard at localhost. Stop it with <code className="font-mono text-sm text-fg">{command('demo_down')}</code>.
            </li>
          </ul>
        </div>
        <div className="rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4">
          <h3 className="text-lg font-semibold text-fg">Your own stack</h3>
          <div className="mt-4">
            <Command command={command('up')} />
          </div>
          <ul className="mt-5 space-y-2 text-fg-muted">
            <li>Four services: proxy, engine, Postgres and the dashboard. Add-ons: knowledge base, local LLM, observability.</li>
            <li>One OpenRouter key covers the fast check and the deep-audit judge; the setup wizard connects it.</li>
            <li>{copy.setup.step_connect.privacy}</li>
          </ul>
        </div>
      </div>
      <div className="mt-10 flex flex-wrap items-center gap-3">
        <a href={REPO_URL} className={buttonClass.secondary}>
          <GitHubMark className="size-4" />
          Source, docs and issues on GitHub
        </a>
        <span className="text-sm text-fg-muted">MIT licensed.</span>
      </div>
    </Section>
  );
}

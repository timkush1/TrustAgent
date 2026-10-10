/**
 * Section 7 (SPEC §8.1): run it yourself, from the zero-key demo to your own stack, and the one
 * line your app changes. Commands come from contracts/ui/commands.v1.json; the SDK line is the
 * dashboard's own "Connect your app" snippet (src/domain/snippets.ts), cut to the line that changes.
 */
import { command, REPO_URL } from './content';
import { sectionCopy } from './sectionCopy';
import { oneLineSnippet } from './snippetLine';
import { buttonClass } from './styles';
import { CodeScroll, Command, GitHubMark, Section } from './ui';

export function SelfHost() {
  return (
    <Section
      id="self-host"
      eyebrow="Run it yourself"
      title="On your laptop in one command."
      lead="Everything on this site runs locally with Docker. The demo needs no keys; your own stack needs one OpenRouter key."
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4">
          <h3 className="text-lg font-semibold text-fg">The zero-key demo</h3>
          <div className="mt-4">
            <Command command={command('demo')} />
          </div>
          <ul className="mt-5 space-y-2 text-fg-muted">
            <li>Docker only: no keys, and nothing is billed.</li>
            <li>Mock checks, a mock judge and scripted traffic; every number it shows is labelled simulated.</li>
            <li>
              Opens the dashboard on localhost. Stop it with <code className="font-mono text-sm text-fg">{command('demo_down')}</code>.
            </li>
          </ul>
        </div>
        <div className="rounded-lg border border-border-strong bg-surface p-6 max-sm:p-4">
          <h3 className="text-lg font-semibold text-fg">Your own stack</h3>
          <div className="mt-4">
            <Command command={command('up')} />
          </div>
          <p className="mt-3 text-sm text-fg-muted">Without make:</p>
          <div className="mt-1.5">
            <Command command={command('compose_up')} scroll />
          </div>
          <ul className="mt-5 space-y-2 text-fg-muted">
            <li>Four services: proxy, engine, Postgres and the dashboard. Add-ons: knowledge base, local LLM, observability.</li>
            <li>Pulls the prebuilt images of a published release, otherwise builds them locally.</li>
            <li>One OpenRouter key covers the fast check and the deep-audit judge; the setup wizard connects it, and keys are encrypted at rest.</li>
            <li>{sectionCopy.setup.step_connect.privacy}</li>
          </ul>
        </div>
      </div>
      <div className="mt-6 rounded-lg border border-border bg-surface-sunken p-5 max-sm:p-4">
        <p className="text-sm text-fg">Then point your app at it. With the OpenAI Python SDK, the one line that changes:</p>
        <CodeScroll label="The line that changes" className="mt-3">
          <pre className="w-max font-mono text-sm whitespace-pre text-fg" data-snippet>
            {oneLineSnippet()}
          </pre>
        </CodeScroll>
        <p className="mt-2 text-xs text-fg-muted">
          Your provider key still goes to your provider. The dashboard’s Connect your app shows the full snippet for OpenAI, Anthropic, LangChain and curl,
          with the ingest key header and the documents to check against.
        </p>
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

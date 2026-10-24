/**
 * The static Playground (SPEC §7.5, D99-D104): the recorded examples first, then the live check.
 * Every state of the live panel (no key, running, a result, an error with what to do, the 402
 * screen, the call cap, the token cap, the rejected pause, a frame, no live scope), axe, and every
 * disabled action saying why. OpenRouter is a fetch stub: nothing reaches the network.
 */
import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { act, configure, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import decisions401 from '@contracts/fixtures/vendor/openrouter/decisions.401.json';
import decisions402 from '@contracts/fixtures/vendor/openrouter/decisions.402_credits.json';
import decisions429 from '@contracts/fixtures/vendor/openrouter/decisions.429.json';
import { ServicesContext, type AppServices } from '@/app/services';
import { NotFound, RouteError } from '@/components/common/RouteStates';
import { buildRuntimeConfig } from '@/config/runtime';
import { createReplayDataSource } from '@/data/replay/replayDataSource';
import { copy } from '@/domain/presentation';
import { liveMessage } from '@/domain/words/live';
import { liveText } from './live/liveText';
import { browserKeyStore } from '@/lib/openrouter/keyStore';
import { memoryStorage } from '@/lib/openrouter/testing';
import { createUsageStore, type UsageStore } from '@/lib/openrouter/usage';
import { routeTree } from '@/routeTree.gen';
import { axeViolations, formatViolations } from '@/test/axe';
import { fakeDataSource } from '@/test/fakeDataSource';
import { ManualClock, recordingFetch } from '@/test/recording';
import { createTestQueryClient } from '@/test/renderApp';
import { resetKeyCheckForTests } from './live/keyCheck';
import LivePanel, { type LivePanelProps } from './live/LivePanel';

configure({ asyncUtilTimeout: 5000 });

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';
const DOCS =
  'Support is open from 9 am to 5 pm, Monday to Friday. There is no phone or chat support on weekends or public holidays; email answers arrive the next working day.';

interface Fixture {
  status: number;
  headers: Record<string, string>;
  body: unknown;
}
const serve = (f: Fixture): Response => new Response(JSON.stringify(f.body), { status: f.status, headers: f.headers });
const json = (status: number, body: unknown): Response => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

/** A decisions answer for every question the request asked, all at `noul`, with OpenRouter's usage.cost. */
function answerAll(init: RequestInit | undefined, noul: number): Response {
  const body = JSON.parse(String(init?.body)) as { questions: Record<string, { type: string }> };
  const answers = Object.fromEntries(Object.entries(body.questions).map(([qid, q]) => [qid, { type: q.type, noul }]));
  return json(200, { id: 'dec_test', model: 'typesafe/jev-1.13-20260917', provider: 'TypeSafe', answers, usage: { cost: 0.000021, input_tokens: 500, output_tokens: 0 } });
}

type Answer = (url: string, init?: RequestInit) => Response | Promise<Response>;
let decisions: Answer;
let fetchMock: ReturnType<typeof vi.fn>;

function stubOpenRouter() {
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === 'https://openrouter.ai/api/v1/key') return json(200, { data: { limit: 5, limit_remaining: 4.5, usage: 0.5 } });
    if (url === 'https://openrouter.ai/api/alpha/decisions') return decisions(url, init);
    throw new TypeError(`unexpected ${url}`);
  });
  vi.stubGlobal('fetch', fetchMock);
}

const decisionCalls = () => fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/alpha/decisions'));

let usage: UsageStore;

function renderPanel(props: LivePanelProps = {}) {
  const queryClient = createTestQueryClient();
  const config = buildRuntimeConfig(undefined, {
    location: { href: 'https://example.github.io/TrustAgent/', origin: 'https://example.github.io', protocol: 'https:' },
    env: { BASE_URL: '/TrustAgent/', VITE_APP_MODE: 'static' },
  });
  const services: AppServices = { config, dataSource: fakeDataSource(), queryClient };
  return render(
    <ServicesContext.Provider value={services}>
      <QueryClientProvider client={queryClient}>
        <LivePanel usage={usage} {...props} />
      </QueryClientProvider>
    </ServicesContext.Provider>,
  );
}

/** Every disabled control names its reason through aria-describedby (DESIGN.md: no dead buttons). */
function expectDisabledSayWhy(root: HTMLElement = document.body) {
  const disabled = [...root.querySelectorAll('button')].filter((b) => b.getAttribute('aria-disabled') === 'true' || b.disabled);
  for (const b of disabled) {
    if (b.getAttribute('aria-busy') === 'true' || b.hasAttribute('data-pending')) continue;
    const ids = (b.getAttribute('aria-describedby') ?? '').split(/\s+/).filter(Boolean);
    const why = ids.map((i) => document.getElementById(i)?.textContent ?? '').join(' ').trim();
    expect(why, `the disabled button "${b.textContent}" says why`).not.toBe('');
  }
}

beforeEach(() => {
  resetKeyCheckForTests();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
  usage = createUsageStore({ storage: memoryStorage(), target: null, maxCalls: 100 });
  decisions = (_u, init) => answerAll(init, 0.97);
  stubOpenRouter();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
  usage.dispose();
  window.sessionStorage.clear();
  browserKeyStore().refresh();
});

describe('StaticPlayground', () => {
  async function renderStatic(path: string) {
    const config = buildRuntimeConfig(undefined, {
      location: { href: 'https://example.github.io/TrustAgent/', origin: 'https://example.github.io', protocol: 'https:' },
      env: { BASE_URL: '/TrustAgent/', VITE_APP_MODE: 'static' },
    });
    const tape = recordingFetch('/TrustAgent/');
    const dataSource = createReplayDataSource({ basePath: config.basePath, fetchImpl: tape.fetch, clock: new ManualClock() });
    await dataSource.ready;
    const queryClient = createTestQueryClient();
    const services: AppServices = { config, dataSource, queryClient };
    const router = createRouter({
      routeTree,
      context: services,
      history: createMemoryHistory({ initialEntries: [path] }),
      defaultNotFoundComponent: NotFound,
      defaultErrorComponent: RouteError,
      defaultPendingMs: 0,
    });
    render(
      <ServicesContext.Provider value={services}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ServicesContext.Provider>,
    );
    return router;
  }

  it('shows the recorded examples first, then the live check with the scenario from ?scenario=', async () => {
    await renderStatic('/app/playground?scenario=pii-leak-flag');
    const examples = await screen.findByRole('list', { name: 'Example requests' });
    const live = await screen.findByRole('region', { name: liveText.section_title });
    expect(examples.compareDocumentPosition(live) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(live.id).toBe('live');
    expect(within(live).getByText(copy.playground_live.recording_is_simulated)).toBeInTheDocument();
    const picked = await within(live).findByRole('radio', { checked: true });
    expect(picked).toHaveAccessibleName(/Another customer's email address leaked/);
    // Nothing reaches openrouter.ai before consent.
    expect(fetchMock).not.toHaveBeenCalled();
    expect(formatViolations(await axeViolations())).toEqual([]);
  });

  it('ignores a ?scenario= that is not a live scenario', async () => {
    await renderStatic('/app/playground?scenario=javascript:alert(1)');
    const live = await screen.findByRole('region', { name: liveText.section_title });
    expect(await within(live).findByRole('radio', { checked: true })).toHaveAccessibleName(/Grounded answer passes/);
  });

  it('opens the consent dialog for ?paste=1 (the callback’s Paste a key instead)', async () => {
    await renderStatic('/app/playground?paste=1');
    expect(await screen.findByRole('dialog', { name: liveText.consent_title })).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('LivePanel', () => {
  it('without a key: Run opens the consent dialog and nothing is sent', async () => {
    const user = userEvent.setup();
    renderPanel();
    const run = screen.getByRole('button', { name: copy.playground_live.run_check });
    expect(run).toHaveAccessibleDescription(liveText.connect_first);
    expect(screen.getByTestId('live-estimate')).toHaveTextContent(/≈ \$0\.0000\d/);
    await user.click(run);
    expect(await screen.findByRole('dialog', { name: liveText.consent_title })).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(screen.queryByTestId('live-counter')).toBeNull();
  });

  it('runs the refund example: flagged, contradiction, the live label, the vendor cost and 1 of 100', async () => {
    browserKeyStore().set(KEY);
    const user = userEvent.setup();
    renderPanel({ scenario: 'refund-contradiction-flag' });
    expect(screen.getByTestId('live-counter')).toHaveTextContent('0 of 100 calls to OpenRouter (checks and AI drafts)');
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    const result = await screen.findByTestId('live-result');
    expect(within(result).getAllByText('Flagged').length).toBeGreaterThan(0);
    expect(within(result).getByText('Contradicts your documents')).toBeInTheDocument();
    expect(within(result).getByTestId('live-label')).toHaveTextContent('Live · typesafe/jev-1.13-20260917 · uncalibrated · your key');
    expect(within(result).getByTestId('live-check-cost')).toHaveTextContent('This check: $0.000021');
    expect(within(within(result).getByTestId('live-check-cost')).getByRole('button', { name: /^Vendor/ })).toBeInTheDocument();
    expect(within(result).getByText(/does not run in the browser/)).toBeInTheDocument();
    expect(within(result).getByText('make demo').tagName).toBe('CODE');
    // A screen reader hears that the deep audit does not run here, not that it is waiting.
    expect(within(result).getByRole('heading', { name: `${copy.tiers.deep_audit} (not run here)` })).toBeInTheDocument();
    expect(within(result).queryByRole('link', { name: /Open the full trace/ })).toBeNull();
    expect(screen.getByTestId('live-counter')).toHaveTextContent('1 of 100');
    expect(screen.getByTestId('live-spent')).toHaveTextContent('Spent so far: $0.000021');
    // The exact request: the body that was sent.
    await user.click(within(result).getByText(liveText.show_request));
    const sent = JSON.parse(String(decisionCalls()[0][1].body)) as { state: { assistant_response: string } };
    expect(sent.state.assistant_response).toBe('You can get a full refund within 60 days of purchase.');
    expect(within(result).getByTestId('jev-request')).toBeInTheDocument();
    // The key went only to openrouter.ai, and never into the page.
    expect(document.body.innerHTML).not.toContain(KEY);
  });

  it('shows the spinner only after 200 ms while running', async () => {
    browserKeyStore().set(KEY);
    let release: () => void = () => {};
    decisions = (_u, init) => new Promise<Response>((resolve) => (release = () => resolve(answerAll(init, 0.5))));
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) });
    renderPanel();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    expect(screen.queryByTestId('live-running')).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(250);
    });
    expect(screen.getByTestId('live-running')).toHaveTextContent(liveText.running);
    expect(screen.getByRole('radiogroup')).toBeInTheDocument();
    await act(async () => {
      release();
      vi.advanceTimersByTime(600);
    });
    vi.useRealTimers();
    await waitFor(() => expect(screen.queryByTestId('live-running')).toBeNull());
  });

  it('a 402 shows the credits screen and leaves the counter unchanged; Check again asks the free key check', async () => {
    browserKeyStore().set(KEY);
    decisions = () => serve(decisions402 as Fixture);
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    const screen402 = await screen.findByTestId('live-no-credits');
    expect(screen402).toHaveTextContent(copy.playground_live.errors.no_credits);
    const link = within(screen402).getByRole('link', { name: /Add credits/ });
    expect(link).toHaveAttribute('href', 'https://openrouter.ai/settings/credits');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getByTestId('live-counter')).toHaveTextContent('0 of 100');
    const keyChecks = () => fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/v1/key')).length;
    const before = keyChecks();
    await user.click(within(screen402).getByRole('button', { name: copy.playground_live.check_again }));
    await waitFor(() => expect(screen.queryByTestId('live-no-credits')).toBeNull());
    expect(keyChecks()).toBe(before + 1);
    expect(document.body.textContent).not.toContain('Insufficient credits');
  });

  it('a 401 forgets the key and asks for a new one, never showing the vendor text', async () => {
    browserKeyStore().set(KEY);
    decisions = () => serve(decisions401 as Fixture);
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    const error = await screen.findByTestId('live-error');
    expect(error).toHaveTextContent(copy.playground_live.errors.unauthorized);
    expect(browserKeyStore().currentKey()).toBeNull();
    expect(document.body.textContent).not.toContain('User not found');
    await user.click(within(error).getByRole('button', { name: copy.playground_live.actions.paste_new_key }));
    expect(await screen.findByRole('dialog', { name: liveText.consent_title })).toBeInTheDocument();
  });

  it('a 429 offers Retry, which runs the check again', async () => {
    browserKeyStore().set(KEY);
    let n = 0;
    decisions = (_u, init) => (n++ === 0 ? serve(decisions429 as Fixture) : answerAll(init, 0.05));
    const user = userEvent.setup();
    renderPanel({ scenario: 'grounded-pass' });
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    const error = await screen.findByTestId('live-error');
    expect(error).toHaveTextContent(copy.playground_live.errors.rate_limited);
    await user.click(within(error).getByRole('button', { name: copy.playground_live.actions.retry }));
    expect(await screen.findByTestId('live-result')).toBeInTheDocument();
    expect(decisionCalls().length).toBeGreaterThanOrEqual(2);
  });

  it('a request the browser blocked says nothing was billed and offers Retry', async () => {
    browserKeyStore().set(KEY);
    decisions = () => Promise.reject(new TypeError('Failed to fetch'));
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    const error = await screen.findByTestId('live-error');
    expect(error).toHaveTextContent(copy.playground_live.errors.network);
    expect(error).toHaveTextContent(copy.playground_live.browser_blocked);
    expect(screen.getByTestId('live-counter')).toHaveTextContent('0 of 100');
  });

  it('at the call cap: says so, offers make demo, and has no Run button', async () => {
    browserKeyStore().set(KEY);
    usage.dispose();
    usage = createUsageStore({ storage: memoryStorage(), target: null, maxCalls: 1 });
    usage.reserve([10]);
    usage.settle(200, { input_tokens: 10, cost: 0.00001 });
    renderPanel();
    const cap = await screen.findByTestId('live-cap-reached');
    expect(cap).toHaveTextContent('This demo stops after 1 calls to OpenRouter');
    expect(within(cap).getByText('make demo')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: copy.playground_live.run_check })).toBeNull();
    expect(screen.getByTestId('live-counter')).toHaveTextContent('1 of 1');
  });

  it('pauses after 10 rejected responses in a row: Retry resumes, Forget key forgets', async () => {
    browserKeyStore().set(KEY);
    for (let i = 0; i < 10; i += 1) {
      usage.reserve([100]);
      usage.settle(400);
    }
    const user = userEvent.setup();
    renderPanel();
    const pause = screen.getByTestId('live-rejected-pause');
    expect(pause).toHaveTextContent(copy.playground_live.rejected_pause);
    expect(screen.queryByRole('button', { name: copy.playground_live.run_check })).toBeNull();
    await user.click(within(pause).getByRole('button', { name: copy.playground_live.actions.retry }));
    expect(await screen.findByTestId('live-result')).toBeInTheDocument();
    expect(usage.getSnapshot().paused).toBe(false);
  });

  it('refuses a check over the token cap before any call, and says why the button is off', () => {
    browserKeyStore().set(KEY);
    renderPanel({ maxTokens: 50, scenario: 'pii-leak-flag' });
    const run = screen.getByRole('button', { name: copy.playground_live.run_check });
    expect(run).toHaveAttribute('aria-disabled', 'true');
    expect(run).toHaveAccessibleDescription(liveMessage('token_cap', { max_tokens: 50 }));
    expect(decisionCalls()).toHaveLength(0);
    expectDisabledSayWhy();
  });

  it('runs the visitor’s own text on support-bot once documents and an answer are there', async () => {
    browserKeyStore().set(KEY);
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('radio', { name: liveText.custom_title }));
    const run = screen.getByRole('button', { name: copy.playground_live.run_check });
    expect(run).toHaveAttribute('aria-disabled', 'true');
    expect(run).toHaveAccessibleDescription(liveText.documents_missing);
    expectDisabledSayWhy();
    await user.type(screen.getByLabelText(liveText.documents_label), DOCS);
    expect(run).toHaveAccessibleDescription(liveText.answer_missing);
    await user.type(screen.getByLabelText(liveText.answer_label), 'Support is open around the clock.');
    expect(run).not.toHaveAttribute('aria-disabled', 'true');
    await user.click(run);
    await screen.findByTestId('live-result');
    const sent = JSON.parse(String(decisionCalls()[0][1].body)) as { state: { evidence: string[] } };
    expect(JSON.stringify(sent.state.evidence)).toContain('Support is open from 9 am to 5 pm');
    // The result belongs to the text it checked: an edit drops it, and so does another pick.
    await user.type(screen.getByLabelText(liveText.answer_label), '!');
    expect(screen.queryByTestId('live-result')).toBeNull();
    await user.click(run);
    await screen.findByTestId('live-result');
    await user.click(screen.getByRole('radio', { name: /Refund window/ }));
    expect(screen.queryByTestId('live-result')).toBeNull();
    expect(decisionCalls()).toHaveLength(2);
  });

  it('keeps the visitor’s text as sent while its check runs', async () => {
    browserKeyStore().set(KEY);
    let release: () => void = () => {};
    decisions = (_u, init) => new Promise<Response>((resolve) => (release = () => resolve(answerAll(init, 0.5))));
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('radio', { name: liveText.custom_title }));
    await user.type(screen.getByLabelText(liveText.documents_label), DOCS);
    await user.type(screen.getByLabelText(liveText.answer_label), 'Support is open around the clock.');
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    await waitFor(() => expect(decisionCalls()).toHaveLength(1));
    expect(screen.getByLabelText(liveText.answer_label)).toHaveAttribute('readonly');
    release();
    await screen.findByTestId('live-result');
    expect(screen.getByLabelText(liveText.answer_label)).not.toHaveAttribute('readonly');
  });

  it('Forget key aborts the running check, clears the key and keeps the counter', async () => {
    browserKeyStore().set(KEY);
    decisions = (_u, init) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    usage.reserve([10]);
    usage.settle(200, { input_tokens: 10, cost: 0.00001 });
    const user = userEvent.setup();
    renderPanel();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    await user.click(screen.getByRole('button', { name: copy.playground_static.forget_key }));
    expect(browserKeyStore().currentKey()).toBeNull();
    await waitFor(() => expect(screen.getByRole('button', { name: copy.playground_live.run_check })).not.toHaveAttribute('data-pending'));
    expect(screen.queryByTestId('live-error')).toBeNull();
    expect(usage.getSnapshot().calls).toBe(1);
    expect(screen.getByTestId('live-counter')).toHaveTextContent('1 of 100');
    expect(window.sessionStorage.length).toBe(0);
  });

  it('advises a key without a credit limit', async () => {
    fetchMock.mockImplementation(async (input: RequestInfo | URL) =>
      String(input).endsWith('/v1/key') ? json(200, { data: { limit: null, limit_remaining: null } }) : json(500, {}),
    );
    browserKeyStore().set(KEY);
    renderPanel();
    const note = await screen.findByTestId('live-no-limit');
    // The contract's words, with the page it names a link that opens in a new tab.
    const link = within(note).getByRole('link', { name: /openrouter\.ai\/settings\/keys/ });
    expect(link).toHaveAttribute('href', 'https://openrouter.ai/settings/keys');
    expect(link).toHaveAttribute('target', '_blank');
    expect(note.textContent?.replace(link.querySelector('.sr-only')?.textContent ?? '', '')).toBe(copy.playground_live.key_no_limit);
  });

  it('hides the credit-limit note while the out-of-credits screen gives its own advice', async () => {
    fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/v1/key')) return json(200, { data: { limit: null, limit_remaining: null } });
      if (url.endsWith('/alpha/decisions')) return serve(decisions402 as Fixture);
      throw new TypeError(`unexpected ${url}`);
    });
    browserKeyStore().set(KEY);
    const user = userEvent.setup();
    renderPanel();
    expect(await screen.findByTestId('live-no-limit')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    expect(await screen.findByTestId('live-no-credits')).toBeInTheDocument();
    expect(screen.queryByTestId('live-no-limit')).toBeNull();
  });

  it('inside a frame shows only Open in its own tab (D106)', () => {
    const top = Object.getOwnPropertyDescriptor(window, 'top');
    Object.defineProperty(window, 'top', { configurable: true, get: () => ({}) });
    try {
      renderPanel();
      expect(screen.getByText(copy.playground_live.frame_blocked)).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: copy.playground_live.run_check })).toBeNull();
      expect(screen.queryByRole('radiogroup')).toBeNull();
    } finally {
      if (top) Object.defineProperty(window, 'top', top);
    }
  });

  it('without a live scope says how to run it locally (D100 scope R)', () => {
    renderPanel({ liveChecks: false });
    const unavailable = screen.getByTestId('live-unavailable');
    expect(unavailable).toHaveTextContent("Live checks from the browser aren't available here. Try it on the zero-key demo: make demo; on your own stack: make up");
    // Commands read as code, as on every other surface.
    expect(within(unavailable).getByText('make demo').tagName).toBe('CODE');
    expect(within(unavailable).getByText('make up').tagName).toBe('CODE');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('never renders the key or a no-pattern secret, and has no axe violations with a result', async () => {
    browserKeyStore().set(KEY);
    decisions = (_u, init) => answerAll(init, 0.97);
    const user = userEvent.setup();
    renderPanel({ scenario: 'refund-contradiction-flag' });
    await user.click(screen.getByRole('button', { name: copy.playground_live.run_check }));
    await screen.findByTestId('live-result');
    expect(document.body.innerHTML).not.toContain(KEY);
    expect(document.body.innerHTML).not.toContain(NO_PATTERN);
    expect(formatViolations(await axeViolations())).toEqual([]);
  });
});

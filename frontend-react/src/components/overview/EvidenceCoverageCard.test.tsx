/**
 * The evidence-suggestion one-click fix (EVW-7): a suggested tag pattern becomes a policy draft for
 * the chosen route and opens the policy review (validation, Preview impact, save with If-Match).
 */
import { configure, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { parse } from 'yaml';
import { evidenceSuggestion } from '@/test/control/fixtures';
import { createControlApi } from '@/test/control/handlers';
import { renderPage } from '@/test/control/renderPage';
import { installMswServer } from '@/test/msw';
import { stopLive } from '@/test/renderLive';
import { warmRoutes } from '@/test/warmRoutes';

installMswServer();
warmRoutes('/app/overview');
afterEach(() => stopLive());
configure({ asyncUtilTimeout: 4000 });

const PATTERN = { pattern: '<kb_article[^>]*>(.*?)</kb_article>', flags: 's' };

async function openSuggestions(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Why not?' }));
  const item = (await screen.findByText('<kb_article> blocks in the system prompt')).closest('li') as HTMLElement;
  return item;
}

describe('Evidence suggestion one-click', { timeout: 20_000 }, () => {
  it('adds the pattern to the chosen route in a draft, reviews it and saves it', async () => {
    const user = userEvent.setup();
    const { control } = renderPage('/app/overview');
    const item = await openSuggestions(user);
    await user.click(await within(item).findByRole('combobox', { name: 'Route for this pattern' }));
    await user.click(await screen.findByRole('option', { name: 'payments-faq' }));
    await user.click(within(item).getByRole('button', { name: 'Add to this route…' }));

    const dialog = await screen.findByRole('dialog', { name: 'Review and save a new policy version' });
    await waitFor(() => expect(control.callsTo('/api/policies/dry-run')).toHaveLength(1));
    const draft = parse((control.callsTo('/api/policies/validate')[0].body as { yaml: string }).yaml) as {
      routes: { name: string; evidence?: { sources?: string[]; tag_patterns?: object[] } }[];
    };
    expect(draft.routes.find((r) => r.name === 'payments-faq')?.evidence).toEqual({
      sources: ['request_system', 'tool_results', 'extension'],
      tag_patterns: [PATTERN],
    });
    await user.type(within(dialog).getByRole('textbox', { name: 'What changed and why' }), 'Read kb_article blocks as documents');
    await user.click(within(dialog).getByRole('button', { name: 'Save version' }));
    await waitFor(() => expect(control.callsTo('/api/policies', 'PUT')).toHaveLength(1));
    expect(control.callsTo('/api/policies', 'PUT')[0].headers.get('if-match')).toBe('"v3"');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('says so when the route already reads the pattern', async () => {
    const user = userEvent.setup();
    const control = createControlApi();
    const state = control.state.policies as { yaml: string };
    state.yaml = state.yaml.replace('  evidence:\n    sources:', `  evidence:\n    tag_patterns: [{pattern: "${PATTERN.pattern}", flags: s}]\n    sources:`);
    renderPage('/app/overview', { control });
    const item = await openSuggestions(user);
    await user.click(await within(item).findByRole('button', { name: 'Add to this route…' }));
    expect(await within(item).findByRole('alert')).toHaveTextContent('default already reads documents with this pattern.');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('says an admin is needed to change the policy', async () => {
    const user = userEvent.setup();
    renderPage('/app/overview', { status: { auth: { ingest: 'keys', read: 'keys', setup_required: false, caller_scopes: ['read', 'content'] } } });
    const item = await openSuggestions(user);
    expect(await within(item).findByRole('button', { name: 'Add to this route…' })).toHaveAttribute('aria-disabled', 'true');
    expect(within(item).getByText('Needs an admin key')).toBeInTheDocument();
  });

  it('links to Policies on a proxy without the policy API', async () => {
    const user = userEvent.setup();
    renderPage('/app/overview', { control: createControlApi({ policies: undefined, suggestions: { items: [evidenceSuggestion()] } }) });
    const item = await openSuggestions(user);
    expect(await within(item).findByRole('link', { name: 'Add it to a route’s evidence.tag_patterns' })).toHaveAttribute('href', '/app/policies');
  });
});

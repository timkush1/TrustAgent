/**
 * axe-core over a page composed from the whole UI kit, rendered in jsdom (there is no gallery
 * route: this test is where the kit is seen together). The page is checked closed and then with
 * each popup open, in both themes. Contrast is checked from the tokens in
 * charts/tokenContrast.test.ts, because jsdom cannot compute colors.
 */
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Flag, Trash2 } from 'lucide-react';
import { afterEach, describe, expect, it } from 'vitest';
import { BarChart } from '@/components/charts/BarChart';
import { OutcomeTimeSeries } from '@/components/charts/OutcomeTimeSeries';
import { ProbabilityBars } from '@/components/charts/ProbabilityBars';
import { RiskBar, RiskBarLegend } from '@/components/charts/RiskBar';
import { Sparkline } from '@/components/charts/Sparkline';
import { CodeBlock } from '@/components/common/CodeBlock';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { DataTable } from '@/components/common/DataTable';
import { DegradedState } from '@/components/common/DegradedState';
import { DisabledAction } from '@/components/common/DisabledReason';
import { KeyValueList } from '@/components/common/KeyValue';
import { PageHeader } from '@/components/common/PageHeader';
import { Pagination } from '@/components/common/Pagination';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { VerdictBadge } from '@/components/outcome/VerdictBadge';
import { axeViolations, formatViolations } from '@/test/axe';
import { renderWithRouter } from '@/test/renderWithRouter';
import { Badge, Chip } from './badge';
import { Button } from './button';
import { Card, CardContent, CardHeader, Section } from './card';
import { Checkbox } from './checkbox';
import { Combobox } from './combobox';
import { Dialog, DialogContent, DialogTrigger } from './dialog';
import { Field } from './field';
import { IconButton } from './icon-button';
import { Input, Textarea } from './input';
import { Kbd } from './kbd';
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from './menu';
import { RadioGroup } from './radio-group';
import { SegmentedControl } from './segmented-control';
import { Select } from './select';
import { Sheet, SheetContent, SheetTrigger } from './sheet';
import { Skeleton } from './skeleton';
import { Slider } from './slider';
import { Spinner } from './spinner';
import { Switch } from './switch';
import { Tab, TabPanel, Tabs, TabsList } from './tabs';
import { Toaster } from './toast';
import { TooltipProvider } from './tooltip';

const rows = Array.from({ length: 30 }, (_, i) => ({ id: `k${i}`, name: `key-${i}`, uses: i * 3 }));

function KitPage() {
  return (
    <TooltipProvider>
      <main>
        <h1>UI kit</h1>
        <PageHeader description="Every primitive together." actions={<Button variant="primary">Save</Button>}>
          <SegmentedControl
            aria-label="Time window"
            options={[
              { value: '1h', label: '1 h' },
              { value: '24h', label: '24 h' },
            ]}
          />
        </PageHeader>

        <Section title="Actions" description="Buttons, links and reasons.">
          <div className="flex gap-2">
            <Button>Secondary</Button>
            <Button variant="ghost">Ghost</Button>
            <Button pending>Saving</Button>
            <IconButton icon={Trash2} aria-label="Remove key" />
            <CopyButton value="0192f3a0" label="Copy trace id" />
            <DisabledAction reason="needs_admin">{(p) => <Button {...p}>Rotate key</Button>}</DisabledAction>
            <DisabledAction reason="demo">{(p) => <Button {...p}>Save policy</Button>}</DisabledAction>
            <Dialog>
              <DialogTrigger render={<Button>Review changes</Button>} />
              <DialogContent title="Review policy changes" description="2 routes change mode." footer={<Button variant="primary">Save</Button>}>
                <Field label="Note">
                  <Input />
                </Field>
              </DialogContent>
            </Dialog>
            <ConfirmDialog
              trigger={<Button>Allow uncalibrated blocking</Button>}
              title="Allow uncalibrated blocking?"
              description="Answers can be blocked before the evaluator is calibrated."
              confirmLabel="Allow blocking"
              confirmationText="support-bot"
              onConfirm={() => {}}
            />
            <Sheet>
              <SheetTrigger render={<Button>Open label queue</Button>} />
              <SheetContent title="Label queue" footer={<Button>Skip</Button>}>
                <p>Answer text</p>
              </SheetContent>
            </Sheet>
            <Menu>
              <MenuTrigger render={<Button>More actions</Button>} />
              <MenuContent>
                <MenuItem shortcut={<Kbd>E</Kbd>}>Export as JSON</MenuItem>
                <MenuSeparator />
                <MenuItem disabledReason="Needs an admin key">Delete trace</MenuItem>
              </MenuContent>
            </Menu>
          </div>
        </Section>

        <Section title="Form controls">
          <Field label="Daily cap (USD)" description="Fast checks stop for the day at this spend.">
            <Input defaultValue="5" />
          </Field>
          <Field label="Route" error="A route named support-bot already exists">
            <Input defaultValue="support-bot" />
          </Field>
          <Field label="Policy YAML" optional>
            <Textarea mono />
          </Field>
          <Field label="Mode">
            <Select
              options={[
                { value: 'observe', label: 'Observe' },
                { value: 'flag', label: 'Flag' },
              ]}
              defaultValue="flag"
            />
          </Field>
          <Field label="Model">
            <Combobox options={[{ value: 'openai/gpt-4o-mini', label: 'openai/gpt-4o-mini' }]} />
          </Field>
          <Checkbox label="I saved it" description="The key is shown only once." />
          <Switch label="Live" />
          <RadioGroup
            label="When the fast check fails"
            options={[
              { value: 'open', label: 'Pass the answer through' },
              { value: 'closed', label: 'Block the answer' },
            ]}
            defaultValue="open"
          />
          <Slider label="Thresholds" defaultValue={[0.7, 0.95]} thumbLabels={['Flags from', 'Blocks from']} marks={[{ value: 0.8, label: 'Suggested' }]} />
        </Section>

        <Section title="Status and data">
          <Tabs defaultValue="python">
            <TabsList aria-label="Snippet language">
              <Tab value="python">OpenAI Python</Tab>
              <Tab value="curl">curl</Tab>
            </TabsList>
            <TabPanel value="python">
              <CodeBlock label="OpenAI Python" code={'from openai import OpenAI\nclient = OpenAI(base_url="http://localhost:8080/v1")'} />
            </TabPanel>
            <TabPanel value="curl">curl</TabPanel>
          </Tabs>
          <div className="flex gap-2">
            <OutcomePill outcome="blocked" />
            <OutcomePill outcome="would_flag" />
            <VerdictBadge verdict="CONTRADICTED" legend />
            <Badge tone="warning" emphasis="tint" icon={Flag}>
              Flagged
            </Badge>
            <Chip onRemove={() => {}} removeLabel="Remove filter: Blocked">
              Blocked
            </Chip>
            <Kbd keys={['Shift', '?']} />
            <Spinner label="Loading claims" />
            <Skeleton className="h-4 w-20" />
          </div>
          <Card>
            <CardHeader title="Groundedness" description="Checked on 12% of requests" />
            <CardContent>
              <RiskBar risk={0.96} level="critical" warnAt={0.7} criticalAt={0.95} label="Groundedness risk" />
              <RiskBarLegend />
              <ProbabilityBars
                label="Refusal"
                options={[
                  { key: 'a', label: 'Answers the request', p: 0.1 },
                  { key: 'd', label: 'Declines or deflects', p: 0.9, tone: 'warning' },
                ]}
              />
              <Sparkline label="Fast-check p95" values={[210, 230, 190, 250]} />
            </CardContent>
          </Card>
          <KeyValueList
            items={[
              { label: 'Trace id', value: '0192f3a0', mono: true, copy: true },
              { label: 'Deep audit cost', value: null },
            ]}
          />
          <DataTable
            aria-label="API keys"
            rows={rows}
            getRowId={(r) => r.id}
            onRowActivate={() => {}}
            columns={[
              { id: 'name', header: 'Name', cell: (r) => r.name },
              { id: 'uses', header: 'Uses', cell: (r) => r.uses, numeric: true },
            ]}
          />
          <Pagination page={2} pageSize={10} total={30} onPageChange={() => {}} noun="keys" />
          <OutcomeTimeSeries
            title="Requests by outcome"
            description="Per hour"
            width={600}
            buckets={[
              { t: '2026-09-27T10:00:00Z', counts: { passed: 40, flagged: 3, blocked: 1 } },
              { t: '2026-09-27T11:00:00Z', counts: { passed: 52, replaced: 2 } },
            ]}
          />
          <BarChart title="What was caught" valueLabel="Requests" width={480} data={[{ key: 'c', label: 'Contradicts your documents', value: 12 }]} />
          <EmptyState title="No requests yet">Send one with the curl snippet.</EmptyState>
          <ErrorState error={new Error('Knowledge base is starting')} onRetry={() => {}} />
          <DegradedState title="Claims are not available" reason="The deep audit is not reachable." stillWorks="Fast checks still run." />
        </Section>
      </main>
      <Toaster />
    </TooltipProvider>
  );
}

async function expectNoViolations() {
  const violations = await axeViolations(document.body);
  expect(formatViolations(violations)).toEqual([]);
}

afterEach(() => {
  document.documentElement.removeAttribute('data-theme');
});

describe('UI kit accessibility (axe, jsdom)', () => {
  it('catches a real violation (the harness is live)', async () => {
    render(
      <button type="button">
        <Trash2 aria-hidden="true" />
      </button>,
    );
    const ids = (await axeViolations(document.body)).map((v) => v.id);
    expect(ids).toContain('button-name');
  });

  it.each(['light', 'dark'])('has no violations with every popup closed (%s theme)', async (theme) => {
    document.documentElement.setAttribute('data-theme', theme);
    await renderWithRouter(<KitPage />);
    await expectNoViolations();
  });

  it.each([
    ['dialog', 'Review changes', 'dialog'],
    ['confirm dialog', 'Allow uncalibrated blocking', 'alertdialog'],
    ['sheet', 'Open label queue', 'dialog'],
    ['menu', 'More actions', 'menu'],
  ] as const)('has no violations with the %s open', async (_name, trigger, role) => {
    await renderWithRouter(<KitPage />);
    await userEvent.click(screen.getByRole('button', { name: trigger }));
    await screen.findByRole(role);
    await expectNoViolations();
  });

  it('has no violations with the select and combobox lists open', async () => {
    await renderWithRouter(<KitPage />);
    await userEvent.click(screen.getByRole('combobox', { name: 'Mode' }));
    await screen.findByRole('listbox');
    await expectNoViolations();
    await userEvent.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull());
    await userEvent.type(screen.getByRole('combobox', { name: 'Model' }), 'gpt');
    await screen.findByRole('option', { name: 'openai/gpt-4o-mini' });
    await expectNoViolations();
  });
});

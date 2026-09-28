import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Flag } from 'lucide-react';
import { describe, expect, it, vi } from 'vitest';
import { Badge, Chip } from './badge';
import { Card, CardContent, CardFooter, CardHeader, Section } from './card';
import { Kbd } from './kbd';
import { Spinner } from './spinner';

describe('Badge', () => {
  it('carries its tone as a CSS variable and its shape as an emphasis, with an icon beside the words', () => {
    const { container } = render(
      <Badge tone="warning" emphasis="tint" icon={Flag}>
        Flagged
      </Badge>,
    );
    const badge = container.querySelector('[data-tone="warning"]') as HTMLElement;
    expect(badge).toHaveAttribute('data-emphasis', 'tint');
    expect(badge.style.getPropertyValue('--badge')).toBe('var(--tone-warning)');
    expect(badge.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    expect(badge).toHaveTextContent('Flagged');
  });

  it('is quiet and neutral by default', () => {
    const { container } = render(<Badge>Watching</Badge>);
    expect(container.firstElementChild).toHaveAttribute('data-tone', 'neutral');
    expect(container.firstElementChild).toHaveAttribute('data-emphasis', 'quiet');
  });
});

describe('Chip', () => {
  it('has a named remove button', async () => {
    const onRemove = vi.fn();
    render(
      <Chip onRemove={onRemove} removeLabel="Remove filter: Blocked">
        Blocked
      </Chip>,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Remove filter: Blocked' }));
    expect(onRemove).toHaveBeenCalledOnce();
  });
});

describe('Kbd', () => {
  it('marks up a single key and a chord as nested kbd elements', () => {
    const { container } = render(
      <>
        <Kbd>C</Kbd>
        <Kbd keys={['Shift', '?']} />
      </>,
    );
    const kbds = container.querySelectorAll('kbd');
    expect(kbds[0]).toHaveTextContent('C');
    const chord = kbds[1];
    expect(chord.querySelectorAll('kbd')).toHaveLength(2);
    expect(chord).toHaveTextContent('Shift+?');
  });
});

describe('Spinner', () => {
  it('announces its label as a status, and is decorative without one', () => {
    const { container } = render(
      <>
        <Spinner label="Loading claims" />
        <Spinner />
      </>,
    );
    expect(screen.getByRole('status')).toHaveTextContent('Loading claims');
    expect(container.querySelectorAll('svg[aria-hidden="true"]')).toHaveLength(2);
  });
});

describe('Card and Section', () => {
  it('names a section by its h2 and a card by its heading', () => {
    render(
      <Section title="Evidence coverage" description="How many answers could be checked." actions={<button type="button">Why not?</button>}>
        <Card>
          <CardHeader title="Groundedness" description="Checked on 12% of requests" />
          <CardContent>body</CardContent>
          <CardFooter>footer</CardFooter>
        </Card>
      </Section>,
    );
    expect(screen.getByRole('region', { name: 'Evidence coverage' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: 'Evidence coverage' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 3, name: 'Groundedness' })).toBeInTheDocument();
  });

  it('collapses to a disclosure whose heading stays navigable', async () => {
    render(
      <Section title="Performance" collapsible>
        <p>Stage latencies</p>
      </Section>,
    );
    const heading = screen.getByRole('heading', { level: 2, name: 'Performance' });
    const toggle = screen.getByRole('button', { name: 'Performance' });
    expect(heading).toContainElement(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('Stage latencies')).toBeNull();
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('Stage latencies')).toBeVisible();
  });
});

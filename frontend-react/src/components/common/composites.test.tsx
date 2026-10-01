import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/button';
import { renderWithRouter } from '@/test/renderWithRouter';
import { copyText } from './clipboard';
import { CodeBlock } from './CodeBlock';
import { CopyButton } from './CopyButton';
import { DegradedState } from './DegradedState';
import { DisabledAction, DisabledReason } from './DisabledReason';
import { KeyValueList } from './KeyValue';
import { PageHeader } from './PageHeader';
import { Pagination } from './Pagination';

function mockClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
}

afterEach(() => {
  Reflect.deleteProperty(navigator, 'clipboard');
  Reflect.deleteProperty(document, 'execCommand');
  vi.restoreAllMocks();
});

describe('DisabledReason', () => {
  it.each([
    ['needs_connection', 'Connect OpenRouter first', 'Open Connections', '/app/settings/connections'],
    ['needs_admin', 'Needs an admin key', 'Sign in as an admin', '/app/setup'],
    ['env_managed', 'Set by an environment variable; change it there', 'See where it is set', '/app/settings/all'],
  ] as const)('%s says why and links the fix', async (reason, text, linkName, href) => {
    await renderWithRouter(<DisabledReason reason={reason} />);
    expect(screen.getByText(text)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: linkName })).toHaveAttribute('href', href);
  });

  it('gives the demo stack its command as code', async () => {
    await renderWithRouter(<DisabledReason reason="demo" />);
    expect(screen.getByText('Not available on the demo stack')).toBeInTheDocument();
    expect(screen.getByText('make demo-down && make up').tagName).toBe('CODE');
  });

  it('explains read-only without pretending there is a fix', async () => {
    await renderWithRouter(<DisabledReason reason="readonly" />);
    expect(screen.getByText('Not available on a read-only instance')).toBeInTheDocument();
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('takes a page-specific reason and fix', async () => {
    await renderWithRouter(<DisabledReason reason={{ text: 'Label 157 more responses first', fix: <a href="#queue">Open the label queue</a> }} />);
    expect(screen.getByText('Label 157 more responses first')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open the label queue' })).toBeInTheDocument();
  });
});

describe('DisabledAction', () => {
  it('disables the control, keeps it focusable and describes it with the reason', async () => {
    const onClick = vi.fn();
    await renderWithRouter(
      <DisabledAction reason="needs_admin">
        {(props) => (
          <Button {...props} onClick={onClick}>
            Rotate key
          </Button>
        )}
      </DisabledAction>,
    );
    const button = screen.getByRole('button', { name: 'Rotate key' });
    expect(button).toHaveAttribute('aria-disabled', 'true');
    expect(button).toHaveAccessibleDescription(/Needs an admin key/);
    await userEvent.tab();
    expect(button).toHaveFocus();
    await userEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('renders the control alone when the action is available', () => {
    render(<DisabledAction reason={null}>{(props) => <Button {...props}>Rotate key</Button>}</DisabledAction>);
    const button = screen.getByRole('button', { name: 'Rotate key' });
    expect(button).not.toHaveAttribute('aria-disabled', 'true');
    expect(button).not.toHaveAttribute('aria-describedby');
  });
});

describe('copyText', () => {
  it('uses the Clipboard API when it is there', async () => {
    const writeText = vi.fn(async () => {});
    mockClipboard(writeText);
    await expect(copyText('trace-1')).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith('trace-1');
  });

  it('falls back to execCommand when the Clipboard API refuses', async () => {
    mockClipboard(async () => {
      throw new Error('denied');
    });
    const exec = vi.fn(() => true);
    Object.defineProperty(document, 'execCommand', { value: exec, configurable: true });
    await expect(copyText('trace-1')).resolves.toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
    expect(document.querySelector('textarea')).toBeNull();
  });

  it('reports failure when neither works', async () => {
    Object.defineProperty(document, 'execCommand', { value: () => false, configurable: true });
    await expect(copyText('x')).resolves.toBe(false);
  });
});

describe('CopyButton', () => {
  it('copies, turns into a check and announces it', async () => {
    const writeText = vi.fn(async () => {});
    mockClipboard(writeText);
    render(<CopyButton value="0192f3a0" label="Copy trace id" />);
    const button = screen.getByRole('button', { name: 'Copy trace id' });
    await userEvent.click(button);
    expect(writeText).toHaveBeenCalledWith('0192f3a0');
    expect(button).toHaveAttribute('data-copy-state', 'copied');
    expect(screen.getByRole('status')).toHaveTextContent('Copied');
  });

  it('says how to copy by hand when the browser refuses', async () => {
    Object.defineProperty(document, 'execCommand', { value: () => false, configurable: true });
    render(<CopyButton value="x" label="Copy" showLabel />);
    await userEvent.click(screen.getByRole('button', { name: 'Copy' }));
    expect(screen.getByRole('status')).toHaveTextContent("Couldn't copy");
  });
});

describe('CodeBlock', () => {
  it('shows code in a named, focusable scroll region with a copy button', async () => {
    const writeText = vi.fn(async () => {});
    mockClipboard(writeText);
    render(<CodeBlock label="curl" code={'curl -s http://localhost:8080/v1/chat/completions'} copyValue="curl -H 'X-TrustAgent-Key: tk_live_x'" />);
    const region = screen.getByRole('region', { name: 'curl' });
    expect(region).toHaveAttribute('tabindex', '0');
    expect(within(region).getByText(/curl -s/).tagName).toBe('CODE');
    await userEvent.click(screen.getByRole('button', { name: 'Copy curl' }));
    expect(writeText).toHaveBeenCalledWith("curl -H 'X-TrustAgent-Key: tk_live_x'");
  });
});

describe('KeyValueList', () => {
  it('lists facts with an em dash for a missing value and a copy button where asked', () => {
    render(
      <KeyValueList
        items={[
          { label: 'Trace id', value: '0192f3a0', mono: true, copy: true },
          { label: 'Fast check', value: '212 ms', numeric: true, hint: 'Simulated' },
          { label: 'Deep audit cost', value: null },
        ]}
      />,
    );
    const terms = screen.getAllByRole('term').map((t) => t.textContent);
    expect(terms).toEqual(['Trace id', 'Fast check', 'Deep audit cost']);
    const defs = screen.getAllByRole('definition');
    expect(defs[0]).toHaveTextContent('0192f3a0');
    expect(within(defs[0]).getByRole('button', { name: 'Copy trace id' })).toBeInTheDocument();
    expect(defs[1]).toHaveTextContent('212 msSimulated');
    expect(defs[2]).toHaveTextContent('—');
    expect(within(defs[2]).queryByRole('button')).toBeNull();
  });
});

describe('Pagination', () => {
  it('writes the range, names the page each button goes to, and pages', async () => {
    const onPageChange = vi.fn();
    render(<Pagination page={2} pageSize={50} total={1234} noun="claims" onPageChange={onPageChange} />);
    expect(screen.getByRole('navigation', { name: 'Pagination' })).toHaveTextContent('51–100 of 1,234 claims');
    await userEvent.click(screen.getByRole('button', { name: 'Next page, page 3' }));
    expect(onPageChange).toHaveBeenCalledWith(3);
    await userEvent.click(screen.getByRole('button', { name: 'Previous page, page 1' }));
    expect(onPageChange).toHaveBeenCalledWith(1);
  });

  it('says why an end button is disabled', () => {
    render(<Pagination page={1} pageSize={50} total={20} onPageChange={() => {}} />);
    expect(screen.getByRole('button', { name: 'Previous page (this is the first page)' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('button', { name: 'Next page (this is the last page)' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('1–20 of 20 items')).toBeInTheDocument();
  });

  it('handles an empty list', () => {
    render(<Pagination page={1} pageSize={50} total={0} noun="keys" onPageChange={() => {}} />);
    expect(screen.getByText('No keys')).toBeInTheDocument();
  });
});

describe('DegradedState and PageHeader', () => {
  it('names what is missing, why, what still works and the fix', () => {
    render(
      <DegradedState
        title="Claims are not available"
        reason="The deep audit is not reachable."
        stillWorks="Fast checks still run."
        action={<a href="#health">Open health</a>}
      />,
    );
    const status = screen.getByRole('status');
    expect(status).toHaveTextContent('Claims are not available');
    expect(status).toHaveTextContent('Fast checks still run.');
    expect(within(status).getByRole('link', { name: 'Open health' })).toBeInTheDocument();
  });

  it('never adds a second h1', () => {
    render(<PageHeader title="Trace 0192f3a0" description="What happened to this request." actions={<Button>Open evaluator</Button>} />);
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull();
    expect(screen.getByRole('heading', { level: 2, name: 'Trace 0192f3a0' })).toBeInTheDocument();
  });
});

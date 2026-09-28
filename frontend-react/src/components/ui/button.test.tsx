import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Trash2 } from 'lucide-react';
import { describe, expect, it, vi } from 'vitest';
import { Button } from './button';
import { buttonVariants } from './button-variants';
import { IconButton } from './icon-button';
import { TooltipProvider } from './tooltip';

describe('Button', () => {
  it('is secondary by default and primary is the inverted foreground fill', () => {
    render(
      <>
        <Button>Save draft</Button>
        <Button variant="primary">Save</Button>
      </>,
    );
    expect(screen.getByRole('button', { name: 'Save draft' }).className).toContain('bg-surface');
    const primary = screen.getByRole('button', { name: 'Save' });
    expect(primary.className).toContain('bg-fg');
    expect(primary.className).toContain('text-fg-inverse');
  });

  it('keeps a disabled button focusable so its reason can be reached, and ignores presses', async () => {
    const onClick = vi.fn();
    render(<Button disabled onClick={onClick}>Save</Button>);
    const button = screen.getByRole('button', { name: 'Save' });
    await userEvent.tab();
    expect(button).toHaveFocus();
    expect(button).toHaveAttribute('aria-disabled', 'true');
    await userEvent.click(button);
    await userEvent.keyboard('{Enter}');
    expect(onClick).not.toHaveBeenCalled();
  });

  it('shows progress while pending and ignores presses', async () => {
    const onClick = vi.fn();
    render(
      <Button pending onClick={onClick}>
        Remove key
      </Button>,
    );
    const button = screen.getByRole('button', { name: 'Remove key' });
    expect(button).toHaveAttribute('aria-busy', 'true');
    expect(button.querySelector('svg.animate-spin')).not.toBeNull();
    await userEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('runs on Enter and Space', async () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Retry</Button>);
    await userEvent.tab();
    await userEvent.keyboard('{Enter}');
    await userEvent.keyboard(' ');
    expect(onClick).toHaveBeenCalledTimes(2);
  });

  it('exposes its classes for links styled as buttons, danger only by request', () => {
    expect(buttonVariants({ variant: 'danger' })).toContain('bg-tone-danger');
    expect(buttonVariants()).not.toContain('tone-danger');
  });
});

describe('IconButton', () => {
  it('names the button with its aria-label and repeats it as a tooltip on focus', async () => {
    const onClick = vi.fn();
    render(
      <TooltipProvider delay={0}>
        <IconButton icon={Trash2} aria-label="Remove filter" onClick={onClick} />
      </TooltipProvider>,
    );
    const button = screen.getByRole('button', { name: 'Remove filter' });
    expect(button.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    await userEvent.tab();
    expect(button).toHaveFocus();
    expect(await screen.findByText('Remove filter', { selector: '[data-open], [data-open] *' })).toBeInTheDocument();
    await userEvent.keyboard('{Enter}');
    expect(onClick).toHaveBeenCalledOnce();
  });

  it('can skip the tooltip', () => {
    render(<IconButton icon={Trash2} aria-label="Remove" tooltip={false} />);
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });
});

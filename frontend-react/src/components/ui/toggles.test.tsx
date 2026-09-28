import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Checkbox } from './checkbox';
import { RadioGroup } from './radio-group';
import { SegmentedControl } from './segmented-control';
import { Switch } from './switch';

describe('Checkbox', () => {
  it('toggles from its label, the keyboard and reports the new state', async () => {
    const onCheckedChange = vi.fn();
    render(<Checkbox label="I saved it" description="The key is shown only once." onCheckedChange={onCheckedChange} />);
    const box = screen.getByRole('checkbox', { name: 'I saved it' });
    expect(box).toHaveAccessibleDescription('The key is shown only once.');
    expect(box).toHaveAttribute('aria-checked', 'false');
    await userEvent.click(screen.getByText('I saved it'));
    expect(box).toHaveAttribute('aria-checked', 'true');
    expect(onCheckedChange).toHaveBeenLastCalledWith(true, expect.anything());
    box.focus();
    await userEvent.keyboard(' ');
    expect(box).toHaveAttribute('aria-checked', 'false');
  });

  it('shows a mixed state', () => {
    render(<Checkbox label="All routes" indeterminate />);
    expect(screen.getByRole('checkbox', { name: 'All routes' })).toHaveAttribute('aria-checked', 'mixed');
  });

  it('ignores presses when disabled', async () => {
    const onCheckedChange = vi.fn();
    render(<Checkbox label="Store bodies" disabled onCheckedChange={onCheckedChange} />);
    await userEvent.click(screen.getByText('Store bodies'));
    expect(onCheckedChange).not.toHaveBeenCalled();
    expect(screen.getByRole('checkbox', { name: 'Store bodies' })).toHaveAttribute('aria-disabled', 'true');
  });
});

describe('Switch', () => {
  it('is a named switch that flips on Space and Enter', async () => {
    const onCheckedChange = vi.fn();
    render(<Switch label="Live" onCheckedChange={onCheckedChange} />);
    const sw = screen.getByRole('switch', { name: 'Live' });
    sw.focus();
    await userEvent.keyboard(' ');
    expect(sw).toHaveAttribute('aria-checked', 'true');
    await userEvent.keyboard('{Enter}');
    expect(sw).toHaveAttribute('aria-checked', 'false');
    expect(onCheckedChange).toHaveBeenCalledTimes(2);
  });
});

describe('RadioGroup', () => {
  const options = [
    { value: 'open', label: 'Pass the answer through', description: 'Users are never blocked by an outage.' },
    { value: 'closed', label: 'Block the answer', description: 'Nothing unchecked reaches users.' },
  ] as const;

  it('is a named group whose arrow keys move the choice', async () => {
    const onValueChange = vi.fn();
    render(<RadioGroup label="When the fast check fails" options={options} defaultValue="open" onValueChange={onValueChange} />);
    const group = screen.getByRole('radiogroup', { name: 'When the fast check fails' });
    expect(group).toBeInTheDocument();
    const open = screen.getByRole('radio', { name: 'Pass the answer through' });
    expect(open).toHaveAttribute('aria-checked', 'true');
    expect(open).toHaveAccessibleDescription('Users are never blocked by an outage.');
    open.focus();
    await userEvent.keyboard('{ArrowDown}');
    const closed = screen.getByRole('radio', { name: 'Block the answer' });
    expect(closed).toHaveAttribute('aria-checked', 'true');
    expect(closed).toHaveFocus();
    expect(onValueChange).toHaveBeenLastCalledWith('closed');
  });
});

describe('SegmentedControl', () => {
  it('always has one option selected and moves with the arrow keys', async () => {
    const onValueChange = vi.fn();
    render(
      <SegmentedControl
        aria-label="Time window"
        options={[
          { value: '1h', label: '1 h' },
          { value: '24h', label: '24 h' },
          { value: '7d', label: '7 d' },
        ]}
        onValueChange={onValueChange}
      />,
    );
    expect(screen.getByRole('radiogroup', { name: 'Time window' })).toBeInTheDocument();
    const first = screen.getByRole('radio', { name: '1 h' });
    expect(first).toHaveAttribute('aria-checked', 'true');
    first.focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByRole('radio', { name: '24 h' })).toHaveAttribute('aria-checked', 'true');
    expect(onValueChange).toHaveBeenLastCalledWith('24h');
  });

  it('names icon-only options with their label', () => {
    render(
      <SegmentedControl
        aria-label="View"
        value="chart"
        options={[
          { value: 'chart', label: 'Chart', iconOnly: true },
          { value: 'table', label: 'Table', iconOnly: true },
        ]}
      />,
    );
    expect(screen.getByRole('radio', { name: 'Table' })).toBeInTheDocument();
  });
});

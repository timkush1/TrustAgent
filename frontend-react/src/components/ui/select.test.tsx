import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Combobox } from './combobox';
import { Field } from './field';
import { Select } from './select';

const modes = [
  { value: 'observe', label: 'Observe', description: 'Record only' },
  { value: 'flag', label: 'Flag' },
  { value: 'block', label: 'Block' },
] as const;

describe('Select', () => {
  it('opens from the keyboard, moves with arrows and picks with Enter', async () => {
    const onValueChange = vi.fn();
    render(
      <Field label="Mode">
        <Select options={modes} defaultValue="observe" onValueChange={onValueChange} />
      </Field>,
    );
    const trigger = screen.getByRole('combobox', { name: 'Mode' });
    expect(trigger).toHaveTextContent('Observe');
    trigger.focus();
    await userEvent.keyboard('{ArrowDown}');
    const listbox = await screen.findByRole('listbox');
    expect(listbox).toBeInTheDocument();
    await userEvent.click(screen.getByRole('option', { name: 'Block' }));
    expect(onValueChange).toHaveBeenCalledWith('block');
    await waitFor(() => expect(trigger).toHaveTextContent('Block'));
  });

  it('shows the placeholder when nothing is chosen and takes an aria-label outside a Field', () => {
    render(<Select aria-label="Page size" options={[{ value: '50', label: '50 per page' }]} placeholder="Choose a size" />);
    expect(screen.getByRole('combobox', { name: 'Page size' })).toHaveTextContent('Choose a size');
  });

  it('marks the chosen option as selected', async () => {
    render(<Select aria-label="Mode" options={modes} defaultValue="flag" />);
    await userEvent.click(screen.getByRole('combobox', { name: 'Mode' }));
    expect(await screen.findByRole('option', { name: 'Flag' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('option', { name: /Observe/ })).toHaveAttribute('aria-selected', 'false');
  });
});

describe('Combobox', () => {
  const models = [
    { value: 'openai/gpt-4o-mini', label: 'openai/gpt-4o-mini' },
    { value: 'anthropic/claude-haiku-4-5', label: 'anthropic/claude-haiku-4-5' },
    { value: 'meta/llama-3.2-1b', label: 'meta/llama-3.2-1b' },
  ];

  it('filters as you type and picks with the keyboard', async () => {
    const onValueChange = vi.fn();
    render(
      <Field label="Model">
        <Combobox options={models} onValueChange={onValueChange} />
      </Field>,
    );
    const input = screen.getByRole('combobox', { name: 'Model' });
    await userEvent.type(input, 'haiku');
    const options = await screen.findAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual(['anthropic/claude-haiku-4-5']);
    await userEvent.keyboard('{Enter}');
    expect(onValueChange).toHaveBeenCalledWith('anthropic/claude-haiku-4-5');
    expect(input).toHaveValue('anthropic/claude-haiku-4-5');
  });

  it('says so when nothing matches', async () => {
    render(<Combobox aria-label="Model" options={models} emptyText="No model with that name" />);
    await userEvent.type(screen.getByRole('combobox', { name: 'Model' }), 'zzz');
    expect(await screen.findByText('No model with that name')).toBeInTheDocument();
    expect(screen.queryAllByRole('option')).toHaveLength(0);
  });

  it('shows a controlled value by its label', () => {
    render(<Combobox aria-label="Model" options={models} value="meta/llama-3.2-1b" />);
    expect(screen.getByRole('combobox', { name: 'Model' })).toHaveValue('meta/llama-3.2-1b');
  });
});

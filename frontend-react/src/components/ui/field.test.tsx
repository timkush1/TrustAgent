import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { Field } from './field';
import { Input, Textarea } from './input';

describe('Field with Input', () => {
  it('labels the control and describes it with its help text', () => {
    render(
      <Field label="Daily cap (USD)" description="Fast checks stop for the day when this is reached.">
        <Input defaultValue="5" />
      </Field>,
    );
    const input = screen.getByRole('textbox', { name: 'Daily cap (USD)' });
    expect(input).toHaveAccessibleDescription('Fast checks stop for the day when this is reached.');
    expect(input).not.toHaveAttribute('aria-invalid');
  });

  it("marks the control invalid and describes it with the server's message", () => {
    render(
      <Field label="Route" error="A route named support-bot already exists">
        <Input defaultValue="support-bot" />
      </Field>,
    );
    const input = screen.getByRole('textbox', { name: 'Route' });
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(input).toHaveAccessibleDescription(/already exists/);
  });

  it('says when a field is optional and can hide its label visually', () => {
    render(
      <>
        <Field label="Note" optional>
          <Input />
        </Field>
        <Field label="Search requests" hideLabel>
          <Input />
        </Field>
      </>,
    );
    expect(screen.getByRole('textbox', { name: 'Note (optional)' })).toBeInTheDocument();
    expect(screen.getByText('Search requests')).toHaveClass('sr-only');
  });

  it('reports typed text through onValueChange', async () => {
    const onValueChange = vi.fn();
    render(
      <Field label="Model">
        <Input onValueChange={onValueChange} />
      </Field>,
    );
    await userEvent.type(screen.getByRole('textbox', { name: 'Model' }), 'gpt');
    expect(onValueChange).toHaveBeenLastCalledWith('gpt', expect.anything());
  });

  it('disables the control from the field', () => {
    render(
      <Field label="Base URL" disabled>
        <Input />
      </Field>,
    );
    expect(screen.getByRole('textbox', { name: 'Base URL' })).toBeDisabled();
  });
});

describe('Textarea', () => {
  it('is a labelled multi-line control that can be controlled', async () => {
    function Controlled() {
      const [value, setValue] = useState('{}');
      return (
        <Field label="Policy YAML">
          <Textarea mono rows={6} value={value} onValueChange={setValue} />
        </Field>
      );
    }
    render(<Controlled />);
    const area = screen.getByRole('textbox', { name: 'Policy YAML' });
    expect(area.tagName).toBe('TEXTAREA');
    expect(area).toHaveAttribute('rows', '6');
    expect(area).toHaveAttribute('spellcheck', 'false');
    await userEvent.clear(area);
    await userEvent.type(area, 'mode: flag');
    expect(area).toHaveValue('mode: flag');
  });
});

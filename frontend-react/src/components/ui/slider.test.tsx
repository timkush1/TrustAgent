import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Slider } from './slider';

describe('Slider', () => {
  it('is a named slider that steps with the arrow keys and writes its value', async () => {
    const onValueChange = vi.fn();
    const onValueCommitted = vi.fn();
    render(<Slider label="Flag from" defaultValue={0.7} onValueChange={onValueChange} onValueCommitted={onValueCommitted} />);
    const slider = screen.getByRole('slider', { name: 'Flag from' });
    expect(slider).toHaveAttribute('aria-valuetext', '0.70');
    expect(screen.getByText('0.70', { selector: 'output' })).toBeInTheDocument();
    slider.focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(onValueChange).toHaveBeenLastCalledWith(0.71);
    expect(onValueCommitted).toHaveBeenLastCalledWith(0.71);
    await userEvent.keyboard('{PageUp}');
    expect(onValueChange).toHaveBeenLastCalledWith(0.81);
    await userEvent.keyboard('{End}');
    expect(onValueChange).toHaveBeenLastCalledWith(1);
  });

  it('draws tick marks and names the mark a thumb sits on', () => {
    const { container } = render(
      <Slider
        label="Block from"
        value={0.95}
        marks={[
          { value: 0.8, label: 'Suggested' },
          { value: 0.95, label: 'Now' },
        ]}
      />,
    );
    const slider = screen.getByRole('slider', { name: 'Block from' });
    expect(slider).toHaveAttribute('aria-valuetext', '0.95, Now');
    expect((container.querySelector('[data-mark="0.8"]') as HTMLElement).style.left).toBe('80%');
    expect(screen.getByText('Suggested 0.80')).toBeInTheDocument();
  });

  it('describes the thumbs with its help text', () => {
    render(<Slider label="Flag from" defaultValue={0.7} description="Answers at or above this risk are flagged." />);
    expect(screen.getByRole('slider', { name: 'Flag from' })).toHaveAccessibleDescription('Answers at or above this risk are flagged.');
  });

  it('edits two thresholds with one thumb each', async () => {
    const onValueChange = vi.fn();
    render(<Slider label="Thresholds" defaultValue={[0.7, 0.95]} thumbLabels={['Flags from', 'Blocks from']} onValueChange={onValueChange} />);
    const flag = screen.getByRole('slider', { name: 'Flags from' });
    const block = screen.getByRole('slider', { name: 'Blocks from' });
    expect(flag).toHaveAttribute('aria-valuetext', '0.70');
    expect(block).toHaveAttribute('aria-valuetext', '0.95');
    expect(screen.getByText('0.70 – 0.95')).toBeInTheDocument();
    block.focus();
    await userEvent.keyboard('{ArrowLeft}');
    expect(onValueChange).toHaveBeenLastCalledWith([0.7, 0.94]);
  });
});

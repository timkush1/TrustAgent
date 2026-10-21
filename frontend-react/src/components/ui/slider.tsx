/**
 * Slider over Base UI, built for editing thresholds: one thumb ("Flag from") or two ("Flags from"
 * and "Blocks from"), the current value(s) written out, and tick marks for reference points such
 * as the current policy or a calibrated suggestion. Each thumb is a native range input: arrow keys
 * step, Page Up/Down and Shift+arrows take a large step, Home/End jump to the ends. The value text
 * a screen reader hears names a mark when a thumb sits on one.
 */
import { Slider as BaseSlider } from '@base-ui/react/slider';
import { useId, type ReactNode } from 'react';
import { r2 } from '@/domain/format';
import { cn } from '@/lib/cn';

export interface SliderMark {
  value: number;
  /** Short text under the tick ("Now", "Suggested"). A mark without one is an unlabelled tick. */
  label?: string;
}

type SliderValue = number | readonly number[];

export interface SliderProps<T extends SliderValue = number> {
  /** Visible label; for one thumb it also names the thumb. */
  label: ReactNode;
  value?: T;
  defaultValue?: T;
  /** Every change while dragging or stepping. */
  onValueChange?: (value: T) => void;
  /** Once, when the viewer lets go or finishes a key press: save here. */
  onValueCommitted?: (value: T) => void;
  min?: number;
  max?: number;
  step?: number;
  largeStep?: number;
  /** Names each thumb. Required for two thumbs, e.g. ["Flags from", "Blocks from"]. */
  thumbLabels?: readonly string[];
  /** How values are written (default: two decimals). */
  format?: (value: number) => string;
  marks?: readonly SliderMark[];
  /** Help under the slider. */
  description?: ReactNode;
  disabled?: boolean;
  name?: string;
  className?: string;
}

const defaultFormat = (v: number) => r2(v).toFixed(2);

export function Slider<T extends SliderValue = number>({
  label,
  value,
  defaultValue,
  onValueChange,
  onValueCommitted,
  min = 0,
  max = 1,
  step = 0.01,
  largeStep,
  thumbLabels,
  format = defaultFormat,
  marks = [],
  description,
  disabled,
  name,
  className,
}: SliderProps<T>) {
  const descriptionId = useId();
  const initial = value ?? defaultValue ?? min;
  const thumbCount = Array.isArray(initial) ? initial.length : 1;
  const span = max - min || 1;
  const pct = (v: number) => `${((Math.min(max, Math.max(min, v)) - min) / span) * 100}%`;
  const markAt = (v: number) => marks.find((m) => m.label && Math.abs(m.value - v) < step / 2);
  const labelledMarks = marks.filter((m) => m.label);

  return (
    <BaseSlider.Root
      value={value as number | number[] | undefined}
      defaultValue={defaultValue as number | number[] | undefined}
      onValueChange={(next) => onValueChange?.(next as T)}
      onValueCommitted={(next) => onValueCommitted?.(next as T)}
      min={min}
      max={max}
      step={step}
      largeStep={largeStep ?? step * 10}
      minStepsBetweenValues={thumbCount > 1 ? 1 : 0}
      disabled={disabled}
      name={name}
      className={cn('flex flex-col gap-1.5 data-disabled:opacity-50', className)}
    >
      <div className="flex items-baseline justify-between gap-3">
        <BaseSlider.Label className="text-sm font-medium text-fg">{label}</BaseSlider.Label>
        <BaseSlider.Value className="text-sm text-fg tabular">
          {(_formatted, values) => values.map(format).join(' – ')}
        </BaseSlider.Value>
      </div>
      <BaseSlider.Control className="relative flex h-5 touch-none items-center select-none data-disabled:cursor-not-allowed">
        <BaseSlider.Track className="relative h-1 w-full rounded-full bg-border-strong">
          {marks.map((m) => (
            <span
              key={`${m.value}-${m.label ?? ''}`}
              aria-hidden="true"
              data-mark={m.value}
              className="absolute -top-1 h-3 w-px -translate-x-1/2 bg-fg-muted"
              style={{ left: pct(m.value) }}
            />
          ))}
          <BaseSlider.Indicator className="rounded-full bg-fg" />
          {Array.from({ length: thumbCount }, (_, i) => (
            <BaseSlider.Thumb
              key={i}
              index={i}
              aria-describedby={description ? descriptionId : undefined}
              getAriaLabel={thumbLabels ? (index) => thumbLabels[index] ?? '' : undefined}
              getAriaValueText={(_formatted, v) => {
                const mark = markAt(v);
                return mark ? `${format(v)}, ${mark.label}` : format(v);
              }}
              className={cn(
                'size-4 rounded-full border-2 border-fg bg-surface transition-[scale] select-none',
                'has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-fg',
                'data-dragging:scale-110',
              )}
            />
          ))}
        </BaseSlider.Track>
      </BaseSlider.Control>
      {labelledMarks.length > 0 ? (
        <div aria-hidden="true" className="relative h-4 text-2xs text-fg-muted">
          {labelledMarks.map((m) => (
            <span
              key={`${m.value}-${m.label}`}
              className="absolute top-0 -translate-x-1/2 whitespace-nowrap tabular"
              style={{ left: pct(m.value) }}
            >
              {m.label} {format(m.value)}
            </span>
          ))}
        </div>
      ) : null}
      {description ? (
        <p id={descriptionId} className="text-xs text-fg-muted">
          {description}
        </p>
      ) : null}
    </BaseSlider.Root>
  );
}

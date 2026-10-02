/**
 * A text input whose value is written back when the viewer leaves it (or presses Enter), for values
 * that only make sense once typed in full: a comma list, a number, an id. While focused it keeps
 * what is typed; a value that does not parse shows its problem and is not written.
 */
import { useState } from 'react';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';

export interface CommitInputProps {
  label: string;
  /** The value as text. */
  value: string;
  /** Writes the text back; returns a problem to show instead when the text is not valid. */
  onCommit: (text: string) => string | null;
  description?: string;
  optional?: boolean;
  disabled?: boolean;
  mono?: boolean;
  placeholder?: string;
  inputMode?: 'decimal' | 'numeric' | 'text';
}

export function CommitInput({ label, value, onCommit, description, optional, disabled, mono, placeholder, inputMode }: CommitInputProps) {
  const [text, setText] = useState(value);
  const [editing, setEditing] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const shown = editing ? text : value;

  const commit = () => {
    setEditing(false);
    if (text === value) {
      setProblem(null);
      return;
    }
    const p = onCommit(text);
    setProblem(p);
    if (p) setEditing(true);
  };

  return (
    <Field label={label} description={problem ? undefined : description} error={problem} optional={optional}>
      <Input
        value={shown}
        disabled={disabled}
        placeholder={placeholder}
        inputMode={inputMode}
        className={mono ? 'font-mono text-xs' : undefined}
        onFocus={() => {
          if (!editing) setText(value);
          setEditing(true);
        }}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit();
        }}
      />
    </Field>
  );
}

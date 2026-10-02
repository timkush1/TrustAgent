/**
 * The CodeMirror 6 JSON editor of Studio's JSON tab (SPEC §7.3 step 2), loaded only when the tab
 * opens (React.lazy), so CodeMirror never weighs on the rest of Studio.
 *
 * The same house rules as the policy YAML editor: achromatic syntax styling (color means a verdict,
 * DESIGN.md), the design tokens for every surface and ink, the 2px focus ring, and Tab left to the
 * browser so keyboard users can leave the editor. Problems (JSON syntax, the evaluator schema, the
 * hard rules and lints) appear as marks on their exact range; the tab also lists them as text.
 */
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { json } from '@codemirror/lang-json';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { linter, lintGutter, type Diagnostic } from '@codemirror/lint';
import { EditorState } from '@codemirror/state';
import { EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { useEffect, useRef } from 'react';
import type { JsonProblem } from '@/lib/evaluator/checkSpec';

export interface CodeMirrorJsonProps {
  value: string;
  onChange: (value: string) => void;
  readOnly?: boolean;
  /** Accessible name of the editing area. */
  label: string;
  /** Id of the element describing the editor (its problems list). */
  describedBy?: string;
  /** Returns the problems of a text, for the marks. */
  check: (text: string) => JsonProblem[];
}

const highlight = HighlightStyle.define([
  { tag: [tags.propertyName], color: 'var(--fg)', fontWeight: '500' },
  { tag: [tags.string], color: 'var(--fg-muted)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--fg)' },
  { tag: [tags.punctuation, tags.separator, tags.brace, tags.squareBracket], color: 'var(--fg-subtle)' },
]);

const theme = EditorView.theme({
  '&': {
    color: 'var(--fg)',
    backgroundColor: 'var(--surface)',
    fontSize: '12px',
    height: '100%',
    border: '1px solid var(--border)',
    borderRadius: '8px',
  },
  '&.cm-focused': { outline: '2px solid var(--fg)', outlineOffset: '2px' },
  '.cm-scroller': { fontFamily: "'JetBrains Mono Variable', ui-monospace, monospace", lineHeight: '20px' },
  '.cm-content': { caretColor: 'var(--fg)', padding: '8px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--fg)' },
  '.cm-gutters': { backgroundColor: 'var(--surface-sunken)', color: 'var(--fg-subtle)', border: 'none', borderRight: '1px solid var(--border)' },
  '.cm-activeLine': { backgroundColor: 'var(--surface-hover)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)', color: 'var(--fg)' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': { backgroundColor: 'var(--surface-hover)' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'wavy underline var(--tone-danger)', textUnderlineOffset: '3px' },
  '.cm-lintRange-warning': { backgroundImage: 'none', textDecoration: 'dotted underline var(--tone-warning)', textUnderlineOffset: '3px' },
  '.cm-tooltip': { backgroundColor: 'var(--surface-raised)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: '6px' },
  '.cm-diagnostic-error': { borderLeft: '3px solid var(--tone-danger)' },
  '.cm-diagnostic-warning': { borderLeft: '3px solid var(--tone-warning)' },
});

export default function CodeMirrorJson({ value, onChange, readOnly = false, label, describedBy, check }: CodeMirrorJsonProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  const checkRef = useRef(check);
  useEffect(() => {
    onChangeRef.current = onChange;
    checkRef.current = check;
  });

  // The editor owns its text once mounted; `value` seeds it and later outside changes replace it.
  useEffect(() => {
    if (!host.current) return undefined;
    const attributes: Record<string, string> = { 'aria-label': label };
    if (describedBy) attributes['aria-describedby'] = describedBy;
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          highlightActiveLineGutter(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          json(),
          syntaxHighlighting(highlight),
          lintGutter(),
          linter(
            (ed): Diagnostic[] =>
              checkRef.current(ed.state.doc.toString()).map((p) => ({
                from: Math.min(p.from, ed.state.doc.length),
                to: Math.min(Math.max(p.to, p.from), ed.state.doc.length),
                severity: p.severity,
                message: p.path ? `${p.path}: ${p.message}` : p.message,
              })),
            { delay: 250 },
          ),
          theme,
          EditorView.lineWrapping,
          EditorView.contentAttributes.of(attributes),
          EditorState.readOnly.of(readOnly),
          EditorView.editable.of(!readOnly),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onChangeRef.current(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // Recreate only when the editor's configuration changes, not on every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [readOnly, label, describedBy]);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  return <div ref={host} data-testid="json-editor" className="h-[min(60vh,32rem)] min-h-64" />;
}

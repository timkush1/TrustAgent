import exampleYaml from '@contracts/policies/example.policies.yaml?raw';
import { EditorView } from '@codemirror/view';
import { act, configure, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { axeViolations, formatViolations } from '@/test/axe';
import { YamlEditorDialog } from './YamlEditorDialog';

configure({ asyncUtilTimeout: 4000 });

/** Replaces the editor's text the way typing would (jsdom cannot type into contenteditable). */
function setEditorText(text: string) {
  const content = document.querySelector('.cm-content');
  if (!content) throw new Error('no editor');
  const view = EditorView.findFromDOM(content as HTMLElement);
  if (!view) throw new Error('no editor view');
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

describe('YamlEditorDialog', { timeout: 20_000 }, () => {
  it('loads CodeMirror, lists problems as you type and applies a valid change', async () => {
    const user = userEvent.setup();
    const onApply = vi.fn();
    render(<YamlEditorDialog yaml={exampleYaml} writeReason={null} onApply={onApply} />);
    await user.click(screen.getByRole('button', { name: 'Edit YAML' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit the policy as YAML' });
    const editor = await within(dialog).findByRole('textbox', { name: 'policies.yaml' });
    expect(editor).toHaveAttribute('contenteditable', 'true');
    expect(within(dialog).getByText(/No problems found/)).toBeInTheDocument();
    expect(within(dialog).getByText('No changes to apply yet.')).toBeInTheDocument();

    setEditorText(exampleYaml.replace('mode: flag', 'mode: enforce'));
    expect(await within(dialog).findByText(/routes\[0\]\.mode must be one of/)).toBeInTheDocument();
    expect(within(dialog).getByText('Fix the error first.')).toBeInTheDocument();

    setEditorText(exampleYaml.replace('mode: flag', 'mode: observe'));
    expect(await within(dialog).findByText(/No problems found/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Apply to draft' }));
    expect(onApply).toHaveBeenCalledWith(exampleYaml.replace('mode: flag', 'mode: observe'));
  });

  it('is a read-only view, with the reason, when the viewer cannot edit', async () => {
    const user = userEvent.setup();
    render(<YamlEditorDialog yaml={exampleYaml} writeReason="readonly" onApply={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'View YAML' }));
    const dialog = await screen.findByRole('dialog', { name: 'The policy as YAML' });
    const editor = await within(dialog).findByRole('textbox', { name: 'policies.yaml (read-only)' });
    expect(editor).toHaveAttribute('contenteditable', 'false');
    expect(within(dialog).getByText('Not available on a read-only instance')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Apply to draft' })).not.toBeInTheDocument();
    expect(formatViolations(await axeViolations(dialog))).toEqual([]);
  });
});

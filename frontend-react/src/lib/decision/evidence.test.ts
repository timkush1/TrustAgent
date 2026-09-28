// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { decisionItems, EvidenceError, extractEvidence, forwardBody, LABEL_TOOL_RESULT, LABEL_USER_CONTEXT } from './evidence';

describe('extractEvidence', () => {
  it('labels tag blocks by their first label attribute and keeps valid ids only', () => {
    const ex = extractEvidence('openai_chat', {
      messages: [
        {
          role: 'system',
          content: 'Be brief.\r\n<doc title="FAQ" id="bad id">A1</doc>\r\n<SOURCE name=\'kb\' id=kb-7>ignored-attr-form</SOURCE><source id="s-1" title="Policy">Refunds within 30 days.</source>',
        },
        { role: 'user', content: 'Hi' },
      ],
    });
    expect(ex.items.map(({ id, label, text }) => ({ id, label, text }))).toEqual([
      // An invalid id is not the item id, but it is still the first label attribute.
      { id: 'sys1', label: 'bad id', text: 'A1' },
      { id: 'sys2', label: 'kb', text: 'ignored-attr-form' },
      { id: 's-1', label: 's-1', text: 'Refunds within 30 days.' },
    ]);
    expect(ex.system_instructions).toBe('Be brief.');
    expect(ex.items.every((it) => it.truncated === false)).toBe(true);
  });

  it('applies route tag patterns after the built-in blocks', () => {
    const ex = extractEvidence(
      'openai_chat',
      { messages: [{ role: 'system', content: 'Rules.\n{{kb}}Plan A costs $10.{{/kb}}\n{{kb}}{{/kb}}' }] },
      { tag_patterns: [{ pattern: '\\{\\{kb\\}\\}(.*?)\\{\\{/kb\\}\\}', flags: 's' }] },
    );
    expect(ex.items.map((it) => it.text)).toEqual(['Plan A costs $10.']);
    expect(ex.system_instructions).toBe('Rules.');
    expect(() => extractEvidence('openai_chat', {}, { tag_patterns: [{ pattern: '(', flags: '' }] })).toThrow(EvidenceError);
  });

  it('reads blocks from earlier user messages and pasted parts of the last one', () => {
    const ex = extractEvidence('openai_chat', {
      messages: [
        { role: 'user', content: 'Context:\nOrders ship in 2 days.\nQuestion: when?' },
        { role: 'assistant', content: 'Soon.' },
        { role: 'user', content: [{ type: 'text', text: '  pasted note  ' }, { type: 'text', text: 'And now?' }] },
      ],
    });
    expect(ex.items).toEqual([
      { id: 'ctx1', source: 'request_context', label: 'Context', text: 'Orders ship in 2 days.', truncated: false },
      { id: 'ctx2', source: 'request_context', label: LABEL_USER_CONTEXT, text: 'pasted note', truncated: false },
    ]);
    expect(ex.user_request).toBe('And now?');
  });

  it('resolves Anthropic tool_result names and keeps untrusted tools out of decision evidence', () => {
    const ex = extractEvidence(
      'anthropic_messages',
      {
        system: [{ type: 'text', text: 'You help.' }],
        messages: [
          { role: 'user', content: 'Look it up' },
          { role: 'assistant', content: [{ type: 'tool_use', id: 't1', name: 'web_search', input: {} }] },
          {
            role: 'user',
            content: [{ type: 'tool_result', tool_use_id: 't1', content: [{ type: 'text', text: 'result one' }, { type: 'image' }, { type: 'text', text: 'two' }] }],
          },
        ],
      },
      { untrusted_tools: ['web_*'] },
    );
    expect(ex.items).toEqual([{ id: 'tool1', source: 'tool_results', label: 'web_search', text: 'result one\ntwo', truncated: false, untrusted: true }]);
    expect(decisionItems(ex.items)).toEqual([{ id: 'tool1', source: 'tool_results', text: 'result one\ntwo', untrusted: true }]);
    expect(ex.system_instructions).toBe('You help.');
    // The last user message holds only a tool result: there is no user_request text.
    expect(ex.user_request).toBe('');
  });

  it('labels unnamed tool results and treats a completions prompt as the user message', () => {
    const tools = extractEvidence('openai_chat', { messages: [{ role: 'tool', tool_call_id: 'x', content: 'data' }] });
    expect(tools.items[0]).toMatchObject({ id: 'tool1', label: LABEL_TOOL_RESULT });
    const batch = extractEvidence('openai_completions', { prompt: ['<document>Doc text.</document> First?', 'Second?'] });
    expect(batch.user_request).toBe('First?');
    expect(batch.items.map((it) => it.text)).toEqual(['Doc text.']);
    expect(extractEvidence('openai_completions', { prompt: [1, 2] }).user_request).toBe('');
  });

  it('respects system_mode off and whole', () => {
    const body = { messages: [{ role: 'system', content: 'Intro.\n<document>D</document>' }] };
    const off = extractEvidence('openai_chat', body, { system_mode: 'off' });
    expect(off.items).toEqual([]);
    expect(off.system_instructions).toBe('Intro.');
    const whole = extractEvidence('openai_chat', body, { system_mode: 'whole' });
    expect(whole.items.map((it) => it.text)).toEqual(['Intro.\n<document>D</document>']);
    expect(whole.system_instructions).toBe('Intro.\n<document>D</document>');
  });

  it('tolerates malformed members and rejects a body that is not an object', () => {
    const ex = extractEvidence('openai_chat', { messages: [null, 3, { role: 7, content: { text: 'x' } }, { role: 'user', content: 'ok' }], trustagent: 'nope' });
    expect(ex.user_request).toBe('ok');
    expect(() => extractEvidence('openai_chat', [])).toThrow(EvidenceError);
    expect(() => extractEvidence('xml' as never, {})).toThrow(/unknown api format/);
  });

  it('forwards every member except the gateway ones', () => {
    expect(forwardBody({ model: 'm', trustagent: { route: 'r' }, test_response: 'x', temperature: 0 })).toEqual({ model: 'm', temperature: 0 });
  });
});

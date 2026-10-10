/**
 * downloadText: a Blob behind a temporary object URL, clicked through an <a download> that leaves
 * the page at once, and released after the click; false where the browser cannot do it.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';
import { downloadText } from './download';

// jsdom has no object URLs: each test sets its own, and they are put back afterwards.
const original = { createObjectURL: URL.createObjectURL, revokeObjectURL: URL.revokeObjectURL };

afterEach(() => {
  Object.assign(URL, original);
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('downloadText', () => {
  it('clicks a hidden download link for a Blob of the text, then removes it and frees the URL', async () => {
    vi.useFakeTimers();
    const blobs: Blob[] = [];
    const revoke = vi.fn();
    Object.assign(URL, {
      createObjectURL: vi.fn((b: Blob) => {
        blobs.push(b);
        return 'blob:test-1';
      }),
      revokeObjectURL: revoke,
    });
    const inPage: boolean[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      inPage.push(document.body.contains(this));
    });

    expect(downloadText('check.v1.json', '{"a":1}\n')).toBe(true);
    expect(click).toHaveBeenCalledTimes(1);
    expect(inPage).toEqual([true]);
    const clicked = click.mock.contexts[0] as HTMLAnchorElement;
    expect(clicked.download).toBe('check.v1.json');
    expect(clicked.getAttribute('href')).toBe('blob:test-1');
    expect(document.body.contains(clicked)).toBe(false);
    expect(blobs[0].type).toBe('application/json;charset=utf-8');
    expect(await blobs[0].text()).toBe('{"a":1}\n');
    expect(revoke).not.toHaveBeenCalled();
    vi.runAllTimers();
    expect(revoke).toHaveBeenCalledWith('blob:test-1');
  });

  it('says false when the browser cannot make an object URL', () => {
    Object.assign(URL, { createObjectURL: undefined });
    expect(downloadText('x.json', '{}')).toBe(false);
  });
});

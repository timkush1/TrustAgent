import { describe, expect, it, vi } from 'vitest';
import { idleDataSource, isStaticCallbackPath } from './boot';

describe('the static callback boots without data (D105)', () => {
  it.each([
    ['/TrustAgent/connect/callback/', true],
    ['/TrustAgent/connect/callback', true],
    ['/TrustAgent/connect/callback/index.html', true],
    ['/TrustAgent/connect/callbacks/', false],
    ['/TrustAgent/app/playground/', false],
    ['/connect/callback/', false],
  ])('%s → %s', (path, expected) => {
    expect(isStaticCallbackPath(path, '/TrustAgent/')).toBe(expected);
  });

  it('reads nothing and streams nothing', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    const ds = idleDataSource();
    await expect(ds.getStatus()).rejects.toMatchObject({ status: 404 });
    ds.subscribe({ onEvent: () => {}, onConnection: () => {} }).stop();
    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
  });
});

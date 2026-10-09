import { describe, expect, it } from 'vitest';
import { ApiError } from '@/data/errors';
import { describeError } from './describeError';

describe('describeError', () => {
  it('quotes the status and code of an HTTP answer', () => {
    const err = new ApiError({ kind: 'http', status: 409, code: 'stale_version', message: 'The settings changed.' });
    expect(describeError(err)).toEqual({ message: 'The settings changed.', code: 'HTTP 409 · stale_version' });
  });

  it('quotes no status for a failure the page raised itself (status 0)', () => {
    const err = new ApiError({ kind: 'http', status: 0, code: 'storage_blocked', message: 'Site data is blocked.' });
    expect(describeError(err)).toEqual({ message: 'Site data is blocked.', code: null });
  });

  it('quotes no status when there was no answer at all', () => {
    const err = new ApiError({ kind: 'network', status: 0, code: 'network_error', message: 'The server could not be reached.' });
    expect(describeError(err).code).toBeNull();
  });

  it('stringifies anything that is not an Error, and never shows an empty message', () => {
    expect(describeError('boom')).toEqual({ message: 'boom', code: null });
    expect(describeError(new Error(''))).toEqual({ message: 'Something went wrong', code: null });
  });
});

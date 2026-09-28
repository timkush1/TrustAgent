/**
 * msw server for data-layer tests. Handlers are added per test with `server.use(...)`; any
 * unhandled request fails the test, so nothing reaches the network.
 */
import { setupServer } from 'msw/node';
import { afterAll, afterEach, beforeAll } from 'vitest';

export const API = 'http://proxy.test';
export const server = setupServer();

export function installMswServer(): void {
  beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
  afterEach(() => server.resetHandlers());
  afterAll(() => server.close());
}

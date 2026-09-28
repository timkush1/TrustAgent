/**
 * The static GitHub Pages build (recorded traffic replayed through ReplayDataSource, no backend).
 * It arrives in P5 (SPEC §4.2), together with the `replay` and `csp-network` specs; until then the
 * `static` project holds this one pending test so its place in the config is real.
 */
import { test } from '@playwright/test';

test.describe('static', () => {
  test.fixme('replays the recorded demo without a backend (arrives in P5)', async () => {});
});

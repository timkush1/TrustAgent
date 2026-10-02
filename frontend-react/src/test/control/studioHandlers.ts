/**
 * msw handlers and state for the Studio plane (data/control/studioPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-ST after Step 0b: the state is seeded from the golden
 * fixtures (contracts/fixtures/api); the handlers arrive with STW-1.
 */
import type { HttpHandler } from 'msw';
import dryRunFixture from '@contracts/fixtures/api/DryRunResult.done.json';
import quickCalibrationFixture from '@contracts/fixtures/api/QuickCalibration.unstable.json';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import samplesFixture from '@contracts/fixtures/api/StudioSamplesResponse.flagged.json';
import testFixture from '@contracts/fixtures/api/StudioTestResponse.six.json';
import type { DryRunResult, QuickCalibration, StudioDraftResponse, StudioSamplesResponse, StudioTestResponse } from '@/types/api';
import type { HandlerKit, Reply } from './kit';

export interface StudioState {
  /** POST /api/studio/draft (#34). */
  studioDraft: Reply<StudioDraftResponse>;
  /** POST /api/studio/test (#35). */
  studioTest: Reply<StudioTestResponse>;
  /** POST /api/studio/samples (#36). */
  studioSamples: Reply<StudioSamplesResponse>;
  /** POST /api/studio/calibrate (#37). */
  quickCalibration: Reply<QuickCalibration>;
  /** The `done` result of POST /api/studio/dry-run (#38). */
  studioDryRun: Reply<DryRunResult>;
}

export function defaultStudioState(): StudioState {
  return {
    studioDraft: structuredClone(draftFixture) as StudioDraftResponse,
    studioTest: structuredClone(testFixture) as StudioTestResponse,
    studioSamples: structuredClone(samplesFixture) as StudioSamplesResponse,
    quickCalibration: structuredClone(quickCalibrationFixture) as QuickCalibration,
    studioDryRun: structuredClone(dryRunFixture) as DryRunResult,
  };
}

export function studioHandlers(_state: StudioState, _kit: HandlerKit): HttpHandler[] {
  return [];
}

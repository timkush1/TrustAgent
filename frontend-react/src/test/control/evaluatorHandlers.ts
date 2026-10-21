/**
 * msw handlers and state for the evaluator plane (data/control/evaluatorPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-EV: the state is seeded from the golden fixtures
 * (contracts/fixtures/api) and behaves like the gateway for the flows the pages drive:
 *
 * - labels: PUT upserts and DELETE removes (both 204); a trace in `expiredTraces` answers 409
 *   `body_expired`; the queue leaves out what was labelled; each label moves the evaluator's
 *   calibration progress (`evaluatorDetails`, when the full control state is passed in);
 * - calibration: fit appends an inactive (or active) record; activate and deactivate flip
 *   `active`, keeping one active record per target; import answers its fixture;
 * - decision request (#12): per trace in `decisionRequests`, else `decisionRequest`.
 */
import { http, HttpResponse, type HttpHandler } from 'msw';
import calibrationFitFixture from '@contracts/fixtures/api/CalibrationFitResponse.provisional.json';
import calibrationImportFixture from '@contracts/fixtures/api/CalibrationImportResult.mixed.json';
import calibrationListFixture from '@contracts/fixtures/api/CalibrationList.default.json';
import calibrationRecordFixture from '@contracts/fixtures/api/CalibrationRecord.provisional.json';
import decisionRequestFixture from '@contracts/fixtures/api/DecisionRequestView.match.json';
import validateCleanFixture from '@contracts/fixtures/api/EvaluatorValidateResult.clean.json';
import labelQueueFixture from '@contracts/fixtures/api/LabelQueue.default.json';
import type {
  CalibrationFitRequest,
  CalibrationFitResponse,
  CalibrationImportResult,
  CalibrationList,
  CalibrationRecord,
  CalibrationRecordMeta,
  DecisionRequestView,
  EvaluatorDetail,
  EvaluatorMeta,
  EvaluatorValidateResult,
  EvaluatorVersion,
  LabelDelete,
  LabelPut,
  LabelQueue,
} from '@/types/api';
import { API } from '../msw';
import { errorReply, isError, reply, type ErrorReply, type HandlerKit, type Reply } from './kit';

export interface EvaluatorState {
  /** GET /api/traces/:id/decision-request (#12): the answer for any trace not in `decisionRequests`. */
  decisionRequest: Reply<DecisionRequestView>;
  /** Per-trace answers to #12 (a value, an error, or `undefined` for 404). */
  decisionRequests: Record<string, Reply<DecisionRequestView>>;
  /** POST /api/evaluators/validate (#30). */
  evaluatorValidate: Reply<EvaluatorValidateResult>;
  /** POST /api/evaluators (#31), /:id/versions (#32), /:id/archive (#33): an error to answer instead of the echo. */
  evaluatorWriteError: ErrorReply | null;
  /** GET /api/labels/queue (#39). */
  labelQueue: Reply<LabelQueue>;
  /** The labels written so far (PUT upserts by trace and target; DELETE removes). */
  labels: LabelPut[];
  /** Traces whose stored text has expired: PUT /api/labels answers 409 `body_expired`. */
  expiredTraces: string[];
  /** An error PUT and DELETE /api/labels answer instead of 204. */
  labelError: ErrorReply | null;
  /** GET /api/calibration (#42) and GET /api/calibration/:id (#43). */
  calibrations: Reply<CalibrationList>;
  calibrationRecord: Reply<CalibrationRecord>;
  /** POST /api/calibration (#44) and /api/calibration/fit (#45). */
  calibrationImport: Reply<CalibrationImportResult>;
  calibrationFit: Reply<CalibrationFitResponse>;
  /** An error #46 and #46b answer instead of flipping `active`. */
  calibrationActivateError: ErrorReply | null;
}

export function defaultEvaluatorState(): EvaluatorState {
  return {
    decisionRequest: structuredClone(decisionRequestFixture) as DecisionRequestView,
    decisionRequests: {},
    evaluatorValidate: structuredClone(validateCleanFixture) as EvaluatorValidateResult,
    evaluatorWriteError: null,
    labelQueue: structuredClone(labelQueueFixture) as LabelQueue,
    labels: [],
    expiredTraces: [],
    labelError: null,
    calibrations: structuredClone(calibrationListFixture) as CalibrationList,
    calibrationRecord: structuredClone(calibrationRecordFixture) as CalibrationRecord,
    calibrationImport: structuredClone(calibrationImportFixture) as CalibrationImportResult,
    calibrationFit: structuredClone(calibrationFitFixture) as CalibrationFitResponse,
    calibrationActivateError: null,
  };
}

/** What the evaluator handlers read from the rest of the control state when it is there. */
interface WithLibrary {
  evaluatorDetails?: Record<string, Reply<EvaluatorDetail>>;
  evaluators?: Reply<EvaluatorMeta[]>;
}

const sameLabel = (a: { trace_id: string; target: string }, b: { trace_id: string; target: string }) =>
  a.trace_id === b.trace_id && a.target === b.target;

/** "groundedness" from "groundedness@1" (or "groundedness@1#key#params"). */
const idOf = (ref: string) => ref.split('#')[0].split('@')[0];

/** The gateway's target and label pairs (D59, cmd/proxy/labels_api.go): the reason a pair is refused, or null. */
function invalidLabel(put: LabelPut): string | null {
  if (typeof put.note === 'string' && [...put.note.trim()].length > 1000) return 'The note is too long: at most 1000 characters.';
  if (/^[a-z][a-z0-9_]{1,47}@[1-9][0-9]{0,8}$/.test(put.target)) {
    return put.label === 'should_fire' || put.label === 'should_not_fire' ? null : 'An evaluator label is should_fire or should_not_fire.';
  }
  if (/^claim:[0-9]+$/.test(put.target)) {
    return ['SUPPORTED', 'PARTIALLY_SUPPORTED', 'CONTRADICTED', 'UNSUPPORTED'].includes(put.label)
      ? null
      : 'A claim label is SUPPORTED, PARTIALLY_SUPPORTED, CONTRADICTED or UNSUPPORTED.';
  }
  if (put.target === 'response') return put.label === 'hallucinated' || put.label === 'grounded' ? null : 'A response label is hallucinated or grounded.';
  return 'The target is an evaluator ref (groundedness@1), claim:<n> or response.';
}

const noContent = () => new HttpResponse(null, { status: 204 });

export function evaluatorHandlers(state: EvaluatorState & WithLibrary, kit: HandlerKit): HttpHandler[] {
  /** Moves the evaluator's calibration progress by one label, like the server's counts. */
  const moveProgress = (label: { target: string; label: string }, by: 1 | -1) => {
    if (!label.target.includes('@') || (label.label !== 'should_fire' && label.label !== 'should_not_fire')) return;
    const detail = state.evaluatorDetails?.[idOf(label.target)];
    if (!detail || isError(detail)) return;
    const h = detail.calibration_progress.human_labels;
    detail.calibration_progress = {
      ...detail.calibration_progress,
      human_labels: {
        n: Math.max(0, h.n + by),
        pos: Math.max(0, h.pos + (label.label === 'should_fire' ? by : 0)),
        neg: Math.max(0, h.neg + (label.label === 'should_not_fire' ? by : 0)),
      },
    };
  };

  const calibrationItems = (): CalibrationRecordMeta[] | null =>
    state.calibrations && !isError(state.calibrations) ? state.calibrations.items : null;

  const evaluatorVersionEcho = (id: string, body: unknown, version: number): EvaluatorVersion => {
    const b = (body ?? {}) as { spec?: EvaluatorVersion['spec']; changelog?: string };
    return {
      evaluator_id: id,
      version,
      spec: b.spec ?? ({ id } as EvaluatorVersion['spec']),
      spec_sha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
      question_sha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
      calibration_key: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
      created_by: 'setup-admin',
      created_at: new Date().toISOString(),
      changelog: b.changelog ?? '',
    };
  };

  return [
    http.get(`${API}/api/traces/:id/decision-request`, async ({ request, params }) => {
      await kit.record(request);
      const id = String(params.id);
      return reply(id in state.decisionRequests ? state.decisionRequests[id] : state.decisionRequest);
    }),

    http.post(`${API}/api/evaluators/validate`, async ({ request }) => {
      await kit.record(request);
      return reply(state.evaluatorValidate);
    }),
    http.post(`${API}/api/evaluators`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.evaluatorWriteError) return reply(state.evaluatorWriteError);
      const id = ((body ?? {}) as { spec?: { id?: string } }).spec?.id ?? 'new_check';
      return HttpResponse.json(evaluatorVersionEcho(id, body, 1), { status: 201 });
    }),
    http.post(`${API}/api/evaluators/:id/versions`, async ({ request, params }) => {
      const { body } = await kit.record(request);
      if (state.evaluatorWriteError) return reply(state.evaluatorWriteError);
      const base = ((body ?? {}) as { base_version?: number }).base_version ?? 0;
      return HttpResponse.json(evaluatorVersionEcho(String(params.id), body, base + 1), { status: 201 });
    }),
    http.post(`${API}/api/evaluators/:id/archive`, async ({ request, params }) => {
      const { body } = await kit.record(request);
      if (state.evaluatorWriteError) return reply(state.evaluatorWriteError);
      const list = state.evaluators && !isError(state.evaluators) ? state.evaluators : [];
      const meta = list.find((e) => e.id === String(params.id));
      if (!meta) return reply(errorReply(404, 'not_found', `No evaluator called ${String(params.id)}.`));
      meta.archived = Boolean((body as { archived?: boolean } | undefined)?.archived);
      return HttpResponse.json(meta);
    }),

    http.get(`${API}/api/labels/queue`, async ({ request }) => {
      const { url } = await kit.record(request);
      const queue = state.labelQueue;
      if (!queue || isError(queue)) return reply(queue);
      const evaluator = url.searchParams.get('evaluator');
      const limit = Number(url.searchParams.get('limit') ?? 20);
      const items = queue.items
        .filter((item) => !evaluator || item.evaluator_ref === evaluator)
        .filter((item) => !state.labels.some((l) => sameLabel(l, { trace_id: item.trace_id, target: item.evaluator_ref })))
        .slice(0, limit);
      return HttpResponse.json({ items });
    }),
    http.put(`${API}/api/labels`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.labelError) return reply(state.labelError);
      const put = body as LabelPut;
      const invalid = invalidLabel(put);
      if (invalid) return reply(errorReply(422, 'validation_failed', invalid));
      if (state.expiredTraces.includes(put.trace_id)) {
        return reply(errorReply(409, 'body_expired', "This answer's stored text has expired, so it can't be labelled."));
      }
      const old = state.labels.find((l) => sameLabel(l, put));
      if (old) moveProgress(old, -1);
      state.labels = [...state.labels.filter((l) => !sameLabel(l, put)), put];
      moveProgress(put, 1);
      return noContent();
    }),
    http.delete(`${API}/api/labels`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.labelError) return reply(state.labelError);
      const del = body as LabelDelete;
      const old = state.labels.find((l) => sameLabel(l, del));
      if (old) moveProgress(old, -1);
      state.labels = state.labels.filter((l) => !sameLabel(l, del));
      return noContent();
    }),

    http.get(`${API}/api/calibration`, async ({ request }) => {
      const { url } = await kit.record(request);
      const list = state.calibrations;
      if (!list || isError(list)) return reply(list);
      const active = url.searchParams.get('active');
      const snapshot = url.searchParams.get('snapshot');
      const items = list.items
        .filter((r) => active === null || String(r.active) === active)
        .filter((r) => !snapshot || r.snapshot === snapshot);
      return HttpResponse.json({ items });
    }),
    http.get(`${API}/api/calibration/:id`, async ({ request, params }) => {
      await kit.record(request);
      const id = Number(params.id);
      const record = state.calibrationRecord;
      if (record && !isError(record) && record.id === id) return HttpResponse.json(record);
      const fit = state.calibrationFit;
      if (fit && !isError(fit) && fit.record.id === id) return HttpResponse.json(fit.record);
      const meta = calibrationItems()?.find((r) => r.id === id);
      if (meta && record && !isError(record)) return HttpResponse.json({ ...record, ...meta });
      return reply(isError(record) ? record : undefined);
    }),
    http.post(`${API}/api/calibration`, async ({ request }) => {
      await kit.record(request);
      const result = state.calibrationImport;
      if (result && !isError(result)) {
        const items = calibrationItems();
        if (items) for (const r of result.imported) if (!items.some((x) => x.id === r.id)) items.push(structuredClone(r));
      }
      return reply(result);
    }),
    http.post(`${API}/api/calibration/fit`, async ({ request }) => {
      const { body } = await kit.record(request);
      const fit = state.calibrationFit;
      if (!fit || isError(fit)) return reply(fit);
      const req = body as CalibrationFitRequest;
      const record: CalibrationRecord = { ...fit.record, target_key: req.target, active: Boolean(req.activate) };
      const answer: CalibrationFitResponse = { ...fit, record };
      state.calibrationFit = answer;
      const items = calibrationItems();
      if (items) {
        const { entry: _entry, ...meta } = record;
        void _entry;
        const at = items.findIndex((r) => r.id === record.id);
        if (at === -1) items.push(meta);
        else items[at] = meta;
      }
      return HttpResponse.json(answer);
    }),
    ...(['activate', 'deactivate'] as const).map((verb) =>
      http.post(`${API}/api/calibration/:id/${verb}`, async ({ request, params }) => {
        await kit.record(request);
        if (state.calibrationActivateError) return reply(state.calibrationActivateError);
        const items = calibrationItems();
        const id = Number(params.id);
        const target = items?.find((r) => r.id === id);
        if (!items || !target) return reply(errorReply(404, 'not_found', `No calibration record ${id}.`));
        if (verb === 'activate') {
          for (const r of items) if (r.target_key === target.target_key && r.snapshot === target.snapshot) r.active = false;
          target.active = true;
        } else if (target.active) {
          target.active = false;
          // Like the gateway (D55): deactivating an imported or fitted record brings back the
          // target's newest file record.
          if (target.origin !== 'file') {
            const file = items
              .filter((r) => r.origin === 'file' && r.target_key === target.target_key && r.snapshot === target.snapshot)
              .sort((a, b) => b.id - a.id)[0];
            if (file) file.active = true;
          }
        }
        const fit = state.calibrationFit;
        if (fit && !isError(fit) && fit.record.id === id) fit.record.active = target.active;
        return HttpResponse.json(target);
      }),
    ),
  ];
}

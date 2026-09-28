/**
 * The question and the answer as the model gave it (SPEC §3.3 "Trace detail" 2, left). Inline
 * detector and claim marks arrive with src/domain/segmentClaims.ts; in S0 the text is plain.
 */
import type { TraceView } from './traceView';

export function AnswerSection({ view }: { view: Pick<TraceView, 'question' | 'answer' | 'summary'> }) {
  const { question, answer, summary } = view;
  return (
    <section aria-labelledby="answer-title" className="space-y-3">
      <h2 id="answer-title" className="text-base font-semibold text-fg">
        Answer
      </h2>
      {summary.redacted ? (
        <p className="text-sm text-fg-muted">Hidden: this key can't see prompts and answers.</p>
      ) : (
        <div className="space-y-3 rounded-lg border border-border bg-surface p-4">
          {question ? (
            <div className="space-y-0.5">
              <p className="text-xs font-medium text-fg-muted">Question</p>
              <p className="text-sm whitespace-pre-wrap text-fg">{question}</p>
            </div>
          ) : null}
          <div className="space-y-0.5">
            {question ? <p className="text-xs font-medium text-fg-muted">Answer</p> : null}
            {answer ? (
              <p className="text-md whitespace-pre-wrap text-fg">{answer.text}</p>
            ) : (
              <p className="text-sm text-fg-muted">The answer text is not stored for this request.</p>
            )}
            {answer?.preview ? (
              <p className="text-xs text-fg-muted">Preview: the full text is not stored for this request.</p>
            ) : null}
          </div>
        </div>
      )}
    </section>
  );
}

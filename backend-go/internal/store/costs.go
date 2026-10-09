package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// The cost ledger's rows (SPEC §2.3 cost_events, §7.4; D45, D46, D51): one row per
// decision-model attempt (hedge losers and late attempts included) and one per LLM call the engine
// reports in a CostRecord. internal/costs buffers them and writes them in batches; the reads here
// seed the in-memory caps after a restart and fill the status counters.

// Cost kinds (cost_events.kind).
const (
	CostKindDecision = "decision"
	CostKindLLM      = "llm"
)

// costSourceSimulated is cost_events.cost_source of a mock call (decision.CostSimulated): nothing
// was spent, so it never counts toward a cap or the spend seed (D46).
const costSourceSimulated = "simulated"

// costSourceUnknown is cost_events.cost_source of a call no price covered (decision.CostUnknown):
// its cost is NULL, and the LLM cap counts its tokens at the reference price (D91).
const costSourceUnknown = "unknown"

// maxCostUSD is the largest cost cost_events.cost_usd (NUMERIC(14,10)) holds.
const maxCostUSD = 9999.9999999999

// CostEvent is one cost_events row. Empty strings and nil pointers are stored as NULL.
type CostEvent struct {
	// OccurredAt is when the call ended (an LLM row is stamped when its audit completes, D45).
	OccurredAt time.Time
	TraceID    string
	// AuditID links an LLM row to its deep audit (D45); empty for decision rows.
	AuditID  string
	Route    string
	KeyLabel string
	// Component is what the call was for (gate, probe, studio_test, dry_run, playground,
	// deep_audit_decompose, deep_audit_verify, studio_draft, kb_ingest); Kind is decision or llm.
	Component string
	Kind      string
	Provider  string
	Model     string
	// Attempt is the 0-based attempt within a decision call (0 for LLM rows).
	Attempt      int
	InputTokens  *int64
	OutputTokens *int64
	// CostUSD is nil when no price covers the call (cost_source unknown).
	CostUSD           *float64
	CostSource        string
	PriceTableVersion string
	LatencyMs         *int64
	Status            string
	Questions         *int
	Selection         string
	// Hedge marks a hedge attempt, Late an attempt that ended after its caller stopped waiting,
	// Estimated a cost the gateway estimated because no usage came back (D45).
	Hedge, Late, Estimated bool
}

// InsertCostEvents writes rows in one transaction (a pgx batch): all of them or, on an error,
// none. A cost above what cost_usd holds is stored as that maximum.
func (s *PostgresStore) InsertCostEvents(ctx context.Context, rows []CostEvent) error {
	if len(rows) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i := range rows {
			queueCostEvent(batch, &rows[i])
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert cost events: %w", err)
		}
		return nil
	})
}

// queueCostEvent queues the insert of one cost_events row (InsertCostEvents, and an audit's rows
// in the audit's own transaction).
func queueCostEvent(batch *pgx.Batch, r *CostEvent) {
	occurred := r.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	batch.Queue(`
		INSERT INTO cost_events (occurred_at, trace_id, audit_id, route, key_label, component, kind, provider,
			model, attempt, input_tokens, output_tokens, cost_usd, cost_source, price_table_version, latency_ms,
			status, questions, selection, hedge, late, estimated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		occurred.UTC(), nilIfEmpty(r.TraceID), nilIfEmpty(r.AuditID), nilIfEmpty(r.Route), nilIfEmpty(r.KeyLabel),
		r.Component, r.Kind, nilIfEmpty(r.Provider), nilIfEmpty(r.Model), clampInt16(r.Attempt),
		clampInt32Ptr(r.InputTokens), clampInt32Ptr(r.OutputTokens), storedCost(r.CostUSD),
		defaultString(r.CostSource, "unknown"), nilIfEmpty(r.PriceTableVersion), clampInt32Ptr(r.LatencyMs),
		nilIfEmpty(r.Status), clampQuestions(r.Questions), nilIfEmpty(r.Selection), r.Hedge, r.Late, r.Estimated,
	)
}

// SpendTotals is the real spend of one UTC day (D46: simulated rows never count), by kind, with
// the decision spend of each ingest key (the per-key cap).
type SpendTotals struct {
	DecisionUSD float64
	LLMUSD      float64
	// DecisionByKey is the decision spend of each key label that has some.
	DecisionByKey map[string]float64
	// LLMUnpricedInputTokens and LLMUnpricedOutputTokens are the tokens of today's LLM rows no
	// price covered (cost_source unknown, no cost): the ledger counts them at the reference price
	// toward the LLM cap (D91).
	LLMUnpricedInputTokens  int64
	LLMUnpricedOutputTokens int64
}

// SpendToday reads the real spend since dayStart (the start of a UTC day): every component counts
// toward the caps (D50), simulated rows and rows without a cost never do (D46).
func (s *PostgresStore) SpendToday(ctx context.Context, dayStart time.Time) (SpendTotals, error) {
	out := SpendTotals{DecisionByKey: map[string]float64{}}
	rows, err := s.pool.Query(ctx, `
		SELECT kind = 'decision', COALESCE(key_label, ''), COALESCE(sum(cost_usd), 0)::FLOAT8
		FROM cost_events
		WHERE occurred_at >= $1 AND cost_source <> $2 AND cost_usd IS NOT NULL
		GROUP BY 1, 2`, dayStart.UTC(), costSourceSimulated)
	if err != nil {
		return out, fmt.Errorf("spend today: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			isDecision bool
			key        string
			usd        float64
		)
		if err := rows.Scan(&isDecision, &key, &usd); err != nil {
			return out, fmt.Errorf("spend today: %w", err)
		}
		if !isDecision {
			out.LLMUSD += usd
			continue
		}
		out.DecisionUSD += usd
		if key != "" {
			out.DecisionByKey[key] += usd
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("spend today: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(GREATEST(input_tokens, 0)), 0)::BIGINT, COALESCE(sum(GREATEST(output_tokens, 0)), 0)::BIGINT
		FROM cost_events
		WHERE occurred_at >= $1 AND kind <> 'decision' AND cost_source = $2`,
		dayStart.UTC(), costSourceUnknown).Scan(&out.LLMUnpricedInputTokens, &out.LLMUnpricedOutputTokens); err != nil {
		return out, fmt.Errorf("spend today: unpriced LLM tokens: %w", err)
	}
	return out, nil
}

// CapCrossedAt is when today's stored real spend of one cap first reached capUSD: the occurred_at
// of the row whose running cap cost (rows in time order) crossed it, or nil when it has not. It
// counts the rows SpendToday counts: decision rows for the decision cap (decisionCap), the other
// kinds for the LLM cap, never simulated ones (D46). An LLM row no price covered (cost_source
// unknown, no cost) counts at refInPerMTok and refOutPerMTok, the reference judge's price (D91).
func (s *PostgresStore) CapCrossedAt(ctx context.Context, dayStart time.Time, decisionCap bool, capUSD, refInPerMTok, refOutPerMTok float64) (*time.Time, error) {
	if capUSD <= 0 {
		return nil, nil
	}
	if decisionCap {
		refInPerMTok, refOutPerMTok = 0, 0 // the decision cap counts priced rows only
	}
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT occurred_at FROM (
			SELECT occurred_at, id, sum(c) OVER (ORDER BY occurred_at, id ROWS UNBOUNDED PRECEDING) AS running
			FROM (
				SELECT occurred_at, id, CASE
					WHEN cost_usd IS NOT NULL THEN GREATEST(cost_usd, 0)::FLOAT8
					WHEN cost_source = $5 THEN (GREATEST(COALESCE(input_tokens, 0), 0) * $6::FLOAT8
						+ GREATEST(COALESCE(output_tokens, 0), 0) * $7::FLOAT8) / 1e6
					ELSE 0 END AS c
				FROM cost_events
				WHERE occurred_at >= $1 AND cost_source <> $2 AND (kind = 'decision') = $3
			) priced
		) totals
		WHERE running >= $4::FLOAT8 - 1e-9
		ORDER BY occurred_at, id
		LIMIT 1`,
		dayStart.UTC(), costSourceSimulated, decisionCap, capUSD, costSourceUnknown, refInPerMTok, refOutPerMTok).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cap crossed at: %w", err)
	}
	at = at.UTC()
	return &at, nil
}

// DecisionDay is the decision model's activity over the 24 hours before a moment, from the ledger
// (DecisionModelStatus.calls_24h and cost_24h_usd): it survives a restart, unlike the client's own
// counters.
type DecisionDay struct {
	// Calls counts decision calls (their first attempts), simulated ones included.
	Calls int64
	// CostUSD is the real cost of every attempt (D46: simulated rows cost nothing).
	CostUSD float64
}

// Status24h reads the decision model's calls and cost in the 24 hours before now.
func (s *PostgresStore) Status24h(ctx context.Context, now time.Time) (DecisionDay, error) {
	var d DecisionDay
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE attempt = 0),
			COALESCE(sum(cost_usd) FILTER (WHERE cost_source <> $2), 0)::FLOAT8
		FROM cost_events
		WHERE occurred_at >= $1 AND kind = 'decision'`, now.UTC().Add(-24*time.Hour), costSourceSimulated).Scan(&d.Calls, &d.CostUSD)
	if err != nil {
		return d, fmt.Errorf("decision model status 24h: %w", err)
	}
	return d, nil
}

// LastDecisionSnapshot is the snapshot that last answered model on backend in a stored gate
// decision ("" when none did): the decision client starts from it, so the first answer after a
// restart reports a snapshot change only when there was one.
func (s *PostgresStore) LastDecisionSnapshot(ctx context.Context, backend, model string) (string, error) {
	var snap string
	err := s.pool.QueryRow(ctx, `
		SELECT decision_snapshot FROM gate_decisions
		WHERE decision_backend = $1 AND decision_model_requested = $2 AND decision_snapshot <> ''
		ORDER BY created_at DESC LIMIT 1`, backend, model).Scan(&snap)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("last decision snapshot: %w", err)
	}
	return snap, nil
}

// storedCost is the cost_usd value of a row: NULL when unknown or not a finite number, $0 below
// $0, and at most what the column holds. An infinite cost is no amount at all, as it is for the
// caps (costs.RealCostUSD): clamped to the column's maximum it would read back as $9,999.99 of
// spend and exhaust a cap the next time the ledger seeds from the stored rows.
func storedCost(usd *float64) *float64 {
	if usd == nil || math.IsNaN(*usd) || math.IsInf(*usd, 0) {
		return nil
	}
	v := min(max(*usd, 0), maxCostUSD)
	return &v
}

func clampInt32Ptr(p *int64) *int32 {
	if p == nil {
		return nil
	}
	v := clampInt32(*p)
	return &v
}

func clampInt16(v int) int16 {
	return int16(min(max(v, 0), math.MaxInt16)) // #nosec G115 -- clamped to the int16 range first
}

func clampQuestions(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := clampInt32(int64(*p))
	return &v
}

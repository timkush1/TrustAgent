package store

import (
	"context"
	"fmt"
	"time"
)

// GET /api/gate/reliability (SPEC §2.4 endpoint 17): the labelled predictions of one evaluator, the
// input of its reliability table. A sample pairs the evaluator's raw risk with whether it should
// have fired:
//
//   - human labels (the labels table, label_source human or public_dataset): should_fire and
//     should_not_fire on the evaluator's target, with the risk_raw frozen when the label was given;
//   - uniform-sample deep audits (a proxy label from the LLM judge): the attempt-0 decision's
//     risk_raw for the evaluator, labelled should_fire when the audit found the answer
//     HALLUCINATED and should_not_fire when it found it GROUNDED. Other verdicts carry no label.

// Reliability label sources (ReliabilityResponse.n_by_source keys).
const (
	LabelSourceHuman         = "human"
	LabelSourcePublicDataset = "public_dataset"
	LabelSourceUniformAudit  = "uniform_audit"
)

// ReliabilityFilter selects the labelled samples of one evaluator.
type ReliabilityFilter struct {
	// Evaluator is an evaluator ref ("<id>@<version>") or a bare id (every version).
	Evaluator string
	// Snapshot keeps the samples of one decision-model snapshot; "" keeps every snapshot.
	Snapshot string
	// Route keeps the samples of one route's traces (a calibration fit of an evaluator with
	// params: its target key depends on the route's params); "" keeps every route.
	Route string
	// From and To bound the time of the label (human) or of the decision (uniform audits).
	From, To time.Time
	// Labels includes the labels table; UniformAudits the uniform-sample deep audits.
	Labels, UniformAudits bool
	// IncludeMock keeps uniform audits of simulated traffic (test-mode answers, the mock decision
	// backend, a mock judge); they are left out otherwise.
	IncludeMock bool
}

// ReliabilitySample is one labelled prediction.
type ReliabilitySample struct {
	// Source is LabelSourceHuman, LabelSourcePublicDataset or LabelSourceUniformAudit.
	Source string
	// Risk is the evaluator's raw risk; Fire whether it should have fired.
	Risk float64
	Fire bool
}

// ReliabilitySamples returns the labelled samples of f.Evaluator in time order: the labels first,
// then the uniform audits.
func (s *PostgresStore) ReliabilitySamples(ctx context.Context, f ReliabilityFilter) ([]ReliabilitySample, error) {
	out := []ReliabilitySample{}
	if f.Labels {
		rows, err := s.pool.Query(ctx, `
			SELECT l.label_source, l.risk_raw::float8, l.label = 'should_fire'
			FROM labels l
			WHERE (l.target = $1 OR split_part(l.target, '@', 1) = $1)
				AND l.label IN ('should_fire', 'should_not_fire') AND l.risk_raw IS NOT NULL
				AND l.created_at >= $2 AND l.created_at < $3 AND ($4 = '' OR l.snapshot = $4)
				AND ($5 = '' OR EXISTS (SELECT 1 FROM traces t WHERE t.trace_id = l.trace_id AND t.route = $5))
			ORDER BY l.created_at, l.trace_id, l.labeled_by`, f.Evaluator, f.From, f.To, f.Snapshot, f.Route)
		if err != nil {
			return nil, fmt.Errorf("reliability labels: %w", err)
		}
		if out, err = collectSamples(rows, out); err != nil {
			return nil, fmt.Errorf("reliability labels: %w", err)
		}
	}
	if f.UniformAudits {
		rows, err := s.pool.Query(ctx, `
			SELECT '`+LabelSourceUniformAudit+`', (e.value->>'risk_raw')::float8, a.verdict = 'HALLUCINATED'
			FROM gate_decisions gd
			JOIN traces t ON t.trace_id = gd.trace_id
			JOIN audits a ON a.audit_id = t.audit_id
			CROSS JOIN LATERAL jsonb_each(`+decisionEvaluatorsSQL+`) e
			WHERE gd.attempt = 0 AND (e.key = $1 OR split_part(e.key, '@', 1) = $1)
				AND jsonb_typeof(e.value->'risk_raw') = 'number'
				AND a.selection = 'uniform' AND a.verdict IN ('HALLUCINATED', 'GROUNDED')
				AND gd.created_at >= $2 AND gd.created_at < $3 AND ($4 = '' OR gd.decision_snapshot = $4)
				AND ($5::BOOLEAN OR NOT `+mockTraceSQL+`) AND ($6 = '' OR gd.route = $6)
			ORDER BY gd.created_at, gd.trace_id, e.key`, f.Evaluator, f.From, f.To, f.Snapshot, f.IncludeMock, f.Route)
		if err != nil {
			return nil, fmt.Errorf("reliability uniform audits: %w", err)
		}
		if out, err = collectSamples(rows, out); err != nil {
			return nil, fmt.Errorf("reliability uniform audits: %w", err)
		}
	}
	return out, nil
}

// sampleRows is the part of pgx.Rows collectSamples reads.
type sampleRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

func collectSamples(rows sampleRows, out []ReliabilitySample) ([]ReliabilitySample, error) {
	defer rows.Close()
	for rows.Next() {
		var smp ReliabilitySample
		if err := rows.Scan(&smp.Source, &smp.Risk, &smp.Fire); err != nil {
			return nil, err
		}
		out = append(out, smp)
	}
	return out, rows.Err()
}

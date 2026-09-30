package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// MaxTraces is the most traces one recording holds (SPEC §2.4 endpoint 66: limit <= 200).
const MaxTraces = 200

// Filter selects the traces to record: the newest Limit traces created in [Since, Until),
// optionally of one TraceSource and one route. Nil times and empty strings match everything.
type Filter struct {
	Since, Until *time.Time
	Source       string
	Route        string
	Limit        int
}

// TraceStore is the part of the store a DBSource reads (store.PostgresStore).
type TraceStore interface {
	ListTraces(ctx context.Context, f store.TraceFilter) ([]store.TraceRecord, string, error)
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
	TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error)
	GetAudit(ctx context.Context, auditID string) (*store.AuditRecord, error)
}

// DBSource reads traces from the store, building each detail as GET /api/traces/:id does for a
// caller with the content scope. cmd/export-replay reads with bodies (it writes the trace files);
// GET /api/export/replay serves the manifest alone, which needs none.
type DBSource struct {
	Store TraceStore
	Now   func() time.Time
	// Bodies reads each trace's stored body into its detail; without it Detail.Body stays zero.
	Bodies bool
}

// Traces returns the newest traces matching f, newest first, at most f.Limit (and MaxTraces).
func (d *DBSource) Traces(ctx context.Context, f Filter) ([]Trace, error) {
	limit := min(f.Limit, MaxTraces)
	if limit <= 0 {
		limit = MaxTraces
	}
	var out []Trace
	cursor := ""
	for len(out) < limit {
		recs, next, err := d.Store.ListTraces(ctx, store.TraceFilter{
			Limit: limit - len(out), Cursor: cursor, Since: f.Since, Until: f.Until, Source: f.Source, Route: f.Route,
		})
		if err != nil {
			return nil, err
		}
		for i := range recs {
			t, err := d.detail(ctx, &recs[i])
			if err != nil {
				return nil, fmt.Errorf("trace %s: %w", recs[i].TraceID, err)
			}
			out = append(out, t)
		}
		if next == "" || len(recs) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

func (d *DBSource) detail(ctx context.Context, rec *store.TraceRecord) (Trace, error) {
	decisions, err := d.Store.TraceDecisions(ctx, rec.TraceID)
	if err != nil {
		return Trace{}, err
	}
	t := Trace{Detail: store.TraceDetail{
		TraceSummary: rec.Summary(), Decisions: decisions, Labels: []store.LabelView{},
	}}
	if d.Bodies {
		body, err := d.Store.GetTraceBody(ctx, rec.TraceID)
		switch {
		case err == nil:
			now := time.Now
			if d.Now != nil {
				now = d.Now
			}
			t.Detail.Body = body.View(now())
		case errors.Is(err, store.ErrNotFound):
			t.Detail.Body = store.UnavailableBody(store.BodyNotStored)
		default:
			return Trace{}, err
		}
	}
	if rec.AuditID != nil {
		audit, err := d.Store.GetAudit(ctx, *rec.AuditID)
		switch {
		case err == nil:
			detail := audit.Detail()
			t.Detail.AuditDetail = &detail
			t.LLMUSD = audit.CostUSD
		case !errors.Is(err, store.ErrNotFound): // a queued audit has no row yet
			return Trace{}, err
		}
	}
	return t, nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/store"
)

// maxTraces is the most traces one recording holds (SPEC §2.4 endpoint 66: limit <= 200).
const maxTraces = 200

// filter selects the traces to export.
type filter struct {
	since, until *time.Time
	source       string // TraceSource
	route        string
	limit        int
}

// traceSource reads trace details: the newest traces matching f, at most f.limit.
type traceSource interface {
	Traces(ctx context.Context, f filter) ([]recording.Trace, error)
}

// traceStore is the part of the store the database source reads.
type traceStore interface {
	ListTraces(ctx context.Context, f store.TraceFilter) ([]store.TraceRecord, string, error)
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
	TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error)
	GetAudit(ctx context.Context, auditID string) (*store.AuditRecord, error)
}

// dbSource reads traces from Postgres, building each detail as GET /api/traces/:id does for a
// caller with the content scope.
type dbSource struct {
	store traceStore
	now   func() time.Time
}

func (d *dbSource) Traces(ctx context.Context, f filter) ([]recording.Trace, error) {
	var out []recording.Trace
	cursor := ""
	for len(out) < f.limit {
		recs, next, err := d.store.ListTraces(ctx, store.TraceFilter{
			Limit: min(f.limit-len(out), maxTraces), Cursor: cursor, Since: f.since, Until: f.until, Source: f.source, Route: f.route,
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

func (d *dbSource) detail(ctx context.Context, rec *store.TraceRecord) (recording.Trace, error) {
	decisions, err := d.store.TraceDecisions(ctx, rec.TraceID)
	if err != nil {
		return recording.Trace{}, err
	}
	t := recording.Trace{Detail: store.TraceDetail{
		TraceSummary: rec.Summary(), Decisions: decisions, Labels: []store.LabelView{},
	}}
	body, err := d.store.GetTraceBody(ctx, rec.TraceID)
	switch {
	case err == nil:
		t.Detail.Body = body.View(d.now())
	case errors.Is(err, store.ErrNotFound):
		t.Detail.Body = store.UnavailableBody(store.BodyNotStored)
	default:
		return recording.Trace{}, err
	}
	if rec.AuditID != nil {
		audit, err := d.store.GetAudit(ctx, *rec.AuditID)
		switch {
		case err == nil:
			detail := audit.Detail()
			t.Detail.AuditDetail = &detail
			t.LLMUSD = audit.CostUSD
		case !errors.Is(err, store.ErrNotFound): // a queued audit has no row yet
			return recording.Trace{}, err
		}
	}
	return t, nil
}

// apiSource reads traces through the gateway's REST API (GET /api/traces and
// GET /api/traces/:id). The key needs the read and content scopes.
type apiSource struct {
	base   *url.URL
	key    string
	client *http.Client
}

// maxAPIResponseBytes bounds one API response (a trace detail with bodies and an audit).
const maxAPIResponseBytes = 32 << 20

func newAPISource(base, key string, client *http.Client) (*apiSource, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("--api %q is not an http(s) URL", base)
	}
	return &apiSource{base: u, key: key, client: client}, nil
}

func (a *apiSource) Traces(ctx context.Context, f filter) ([]recording.Trace, error) {
	var out []recording.Trace
	cursor := ""
	for len(out) < f.limit {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(min(f.limit-len(out), maxTraces)))
		if f.since != nil {
			q.Set("since", f.since.UTC().Format(time.RFC3339Nano))
		}
		if f.until != nil {
			q.Set("until", f.until.UTC().Format(time.RFC3339Nano))
		}
		if f.source != "" {
			q.Set("source", f.source)
		}
		if f.route != "" {
			q.Set("route", f.route)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items []struct {
				TraceID string `json:"trace_id"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := a.get(ctx, "/api/traces", q, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			var d store.TraceDetail
			if err := a.get(ctx, "/api/traces/"+url.PathEscape(it.TraceID), nil, &d); err != nil {
				return nil, err
			}
			if r := d.Body.UnavailableReason; r != nil && *r == store.BodyScope {
				return nil, errors.New("the API key lacks the content scope, so trace bodies are hidden: use an admin key")
			}
			out = append(out, recording.Trace{Detail: d})
		}
		if page.NextCursor == nil || *page.NextCursor == "" || len(page.Items) == 0 {
			break
		}
		cursor = *page.NextCursor
	}
	return out, nil
}

func (a *apiSource) get(ctx context.Context, path string, q url.Values, v any) error {
	u := *a.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	if len(b) > maxAPIResponseBytes {
		return fmt.Errorf("GET %s: response too large", path)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("GET %s: %d %s: %s", path, resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("GET %s: invalid response: %w", path, err)
	}
	return nil
}

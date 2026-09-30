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

// traceSource reads trace details: the newest traces matching f, at most f.Limit.
type traceSource interface {
	Traces(ctx context.Context, f recording.Filter) ([]recording.Trace, error)
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

func (a *apiSource) Traces(ctx context.Context, f recording.Filter) ([]recording.Trace, error) {
	var out []recording.Trace
	cursor := ""
	for len(out) < f.Limit {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(min(f.Limit-len(out), recording.MaxTraces)))
		if f.Since != nil {
			q.Set("since", f.Since.UTC().Format(time.RFC3339Nano))
		}
		if f.Until != nil {
			q.Set("until", f.Until.UTC().Format(time.RFC3339Nano))
		}
		if f.Source != "" {
			q.Set("source", f.Source)
		}
		if f.Route != "" {
			q.Set("route", f.Route)
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

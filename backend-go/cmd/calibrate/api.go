package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

// maxAPIResponseBytes bounds the fit response (a record with a 101-point table and bins).
const maxAPIResponseBytes = 8 << 20

// fitRequest is the body of POST /api/calibration/fit (SPEC §2.4 endpoint 45).
type fitRequest struct {
	Target   string   `json:"target"`
	Route    string   `json:"route,omitempty"`
	Snapshot string   `json:"snapshot,omitempty"`
	Sources  []string `json:"sources"`
	Method   string   `json:"method"`
	Activate bool     `json:"activate"`
}

// calibrationRecord is api.ts CalibrationRecord.
type calibrationRecord struct {
	ID         int64                         `json:"id"`
	CreatedAt  time.Time                     `json:"created_at"`
	Origin     string                        `json:"origin"`
	Backend    string                        `json:"backend"`
	Snapshot   string                        `json:"snapshot"`
	Family     string                        `json:"family"`
	TargetKind string                        `json:"target_kind"`
	TargetKey  string                        `json:"target_key"`
	Quality    string                        `json:"quality"`
	Active     bool                          `json:"active"`
	Entry      decision.CalibrationFileEntry `json:"entry"`
}

// fitResponse is the answer of POST /api/calibration/fit: {record, reliability, metrics,
// recommended}; the entry inside the record carries the rest.
type fitResponse struct {
	Record calibrationRecord `json:"record"`
}

// apiError is the ApiErrorBody of a failed call.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// fitViaAPI asks a running gateway to fit a calibration from its label store.
func fitViaAPI(ctx context.Context, client *http.Client, base, key string, req fitRequest) (*calibrationRecord, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/api/calibration/fit")
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("--api %q is not an http(s) URL", base)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	if key != "" {
		hr.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAPIResponseBytes {
		return nil, errors.New("the fit response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		var e apiError
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("POST /api/calibration/fit: %d %s: %s", resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return nil, fmt.Errorf("POST /api/calibration/fit: %s", resp.Status)
	}
	var fr fitResponse
	if err := json.Unmarshal(b, &fr); err != nil {
		return nil, fmt.Errorf("POST /api/calibration/fit: invalid response: %w", err)
	}
	rec := &fr.Record
	if rec.Snapshot == "" || rec.Backend == "" {
		return nil, errors.New("POST /api/calibration/fit: the record names no snapshot or backend")
	}
	if rec.Entry.Target.Key() != rec.TargetKey {
		return nil, fmt.Errorf("POST /api/calibration/fit: record target_key %q does not match its entry (%q)", rec.TargetKey, rec.Entry.Target.Key())
	}
	return rec, nil
}

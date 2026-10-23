package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// knowledgeDoc is a document the knowledge base must hold for a scenario (scenarios.v1 knowledge).
type knowledgeDoc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// knowledgeSet is the selection --seed-knowledge uses when --scenarios is not given: the scenarios
// that need the knowledge add-on.
const knowledgeSet = "knowledge"

// knowledgeSource is the metadata source of a seeded document.
const knowledgeSource = "contracts/demo/scenarios.v1.json"

// knowledgeDocs collects the knowledge documents of scenarios, once per id in first-seen order. A
// document id two scenarios give different texts is an error: the knowledge base holds one.
func knowledgeDocs(scenarios []scenario) ([]knowledgeDoc, error) {
	seen := map[string]string{}
	var out []knowledgeDoc
	for _, s := range scenarios {
		for _, d := range s.Knowledge {
			if d.ID == "" || strings.TrimSpace(d.Text) == "" {
				return nil, fmt.Errorf("scenario %s: a knowledge document needs an id and a text", s.ID)
			}
			if text, ok := seen[d.ID]; ok {
				if text != d.Text {
					return nil, fmt.Errorf("knowledge document %s has two different texts", d.ID)
				}
				continue
			}
			seen[d.ID] = d.Text
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the selected scenarios have no knowledge documents (scenarios.v1 knowledge)")
	}
	return out, nil
}

// readAdminKey reads the admin key from path: its first non-empty line, so a key file may end with
// a newline. The key is never printed.
func readAdminKey(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the key file (--admin-key-file)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k := strings.TrimSpace(line); k != "" {
			return k, nil
		}
	}
	return "", fmt.Errorf("%s holds no key", path)
}

// seedResult is the part of the POST /api/kb/documents answer the seed reports.
type seedResult struct {
	DocumentsIngested int `json:"documents_ingested"`
	ClaimsAccepted    int `json:"claims_accepted"`
	ClaimsQuarantined int `json:"claims_quarantined"`
	ConflictsDetected int `json:"conflicts_detected"`
}

// readyPoll is how often waitEngineReady asks the gateway again; a variable so tests can shorten it.
var readyPoll = 2 * time.Second

// waitEngineReady waits until the gateway's GET /health/ready reports its engine link up and the
// knowledge component not down. A stack that `compose up --wait` reported healthy can still be
// opening the engine's gRPC server (the knowledge engine loads its embedding model first), and a
// seed sent in that window fails with HTTP 500. It returns nil as soon as the link is up, and an
// error naming the last components it saw when the time runs out.
func (g *generator) waitEngineReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := "no answer yet"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.opts.baseURL+"/health/ready", nil)
		if err != nil {
			return err
		}
		if resp, err := g.client.Do(req); err != nil {
			last = err.Error()
		} else {
			var ready struct {
				Components map[string]string `json:"components"`
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if json.Unmarshal(b, &ready) == nil && ready.Components != nil {
				engine, knowledge := ready.Components["engine"], ready.Components["knowledge"]
				if engine == "up" && knowledge != "down" {
					return nil
				}
				last = fmt.Sprintf("engine %q, knowledge %q", engine, knowledge)
			} else {
				last = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the gateway's engine link was not up within %s (last: %s)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// seedKnowledge uploads docs to the gateway's knowledge base (POST /api/kb/documents, the admin
// scope): one JSON file of {content, metadata{doc_id, title, source}}, the upload the dashboard
// sends, so the engine ingests each under its scenario id.
func (g *generator) seedKnowledge(ctx context.Context, docs []knowledgeDoc, adminKey string) (*seedResult, error) {
	type upload struct {
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata"`
	}
	payload := make([]upload, len(docs))
	for i, d := range docs {
		payload[i] = upload{Content: d.Text, Metadata: map[string]string{"doc_id": d.ID, "title": d.ID, "source": knowledgeSource}}
	}
	file, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "trafficgen-knowledge.json")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(file); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.opts.baseURL+"/api/kb/documents", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-TrustAgent-Key", adminKey)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /api/kb/documents: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("POST /api/kb/documents: HTTP %d: the key in --admin-key-file is not an admin key (%s)", resp.StatusCode, snippet(b))
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusServiceUnavailable:
		return nil, fmt.Errorf("POST /api/kb/documents: HTTP %d: is the knowledge add-on on (TRUTHTABLE_KNOWLEDGE_ENABLED=true, an engine with the knowledge profile)? %s",
			resp.StatusCode, snippet(b))
	default:
		return nil, fmt.Errorf("POST /api/kb/documents: HTTP %d: %s", resp.StatusCode, snippet(b))
	}
	var res seedResult
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("POST /api/kb/documents: not an upload result: %w", err)
	}
	return &res, nil
}

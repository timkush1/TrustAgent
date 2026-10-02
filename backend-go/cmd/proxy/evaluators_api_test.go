package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

const (
	evalAdminKey = "eval-admin-key-for-tests"

	schemaEvaluatorVersion  = "api.v1.schema.json#/$defs/EvaluatorVersion"
	schemaEvaluatorMeta     = "api.v1.schema.json#/$defs/EvaluatorMeta"
	schemaEvaluatorValidate = "api.v1.schema.json#/$defs/EvaluatorValidateResult"
)

// toneSpec is the off_brand_tone template as a dashboard evaluator id@version (a valid spec).
func toneSpec(t *testing.T, id string, version int, edit ...func(map[string]any)) map[string]any {
	t.Helper()
	b, err := os.ReadFile(config.RepoPath("contracts/evaluators/off_brand_tone.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["id"], m["version"], m["builtin"], m["name"] = id, version, false, "House tone "+id
	for _, e := range edit {
		e(m)
	}
	return m
}

// call sends a JSON request with key (none when empty) and extra header pairs.
func (e *testEnv) call(method, path, key string, body any, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rd)
	if key != "" {
		req.Header.Set(authz.HeaderKey, key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// apiErr decodes an ApiErrorBody.
type apiErr struct {
	Error struct {
		Code        string         `json:"code"`
		Message     string         `json:"message"`
		FieldErrors []fieldError   `json:"field_errors"`
		Details     map[string]any `json:"details"`
	} `json:"error"`
}

func decodeErr(t *testing.T, body []byte) apiErr {
	t.Helper()
	var e apiErr
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("not an error body: %s", body)
	}
	contractstest.ValidateJSON(t, schemaError, body)
	return e
}

// evalEnv is a server on Postgres with an admin key and a file evaluator (house_style), and a
// pool on its schema for the assertions.
func evalEnv(t *testing.T, mut func(*envOpts)) (*testEnv, *pgxpool.Pool) {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(toneSpec(t, "house_style", 1))
	if err := os.WriteFile(filepath.Join(dir, "house_style.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	o := envOpts{adminKey: evalAdminKey, mutate: func(c *config.Config) {
		c.EvaluatorsDir = dir
		c.PublicURL = dashOrigin
	}}
	if mut != nil {
		mut(&o)
	}
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	o.store = pg
	return newTestEnv(t, o), schema.Pool(t)
}

func TestEvaluatorValidateAPI(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	resp, body := env.call(http.MethodPost, "/api/evaluators/validate", testKey, map[string]any{"spec": toneSpec(t, "house_tone", 1)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaEvaluatorValidate, body)
	var res evaluatorValidateResult
	_ = json.Unmarshal(body, &res)
	if len(res.Errors) != 0 || res.QuestionSHA256 == nil || res.CalibrationKey == nil || res.QuestionTokens == nil || *res.QuestionTokens < 20 {
		t.Errorf("clean spec: %s", body)
	}
	for _, l := range res.Lints {
		if l.Severity == "error" {
			t.Errorf("the template breaks a hard rule: %+v", l)
		}
	}

	// A hard-rule violation and a schema error: findings, no hashes.
	bad := toneSpec(t, "house_tone", 1, func(m map[string]any) {
		m["signal"] = map[string]any{"risk_weights": map[string]any{"no_such_option": 1}}
		m["unexpected_member"] = 7
	})
	resp, body = env.call(http.MethodPost, "/api/evaluators/validate", testKey, map[string]any{"spec": bad})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaEvaluatorValidate, body)
	res = evaluatorValidateResult{}
	_ = json.Unmarshal(body, &res)
	if len(res.Errors) == 0 || res.QuestionSHA256 != nil || res.QuestionTokens != nil ||
		!slices.ContainsFunc(res.Lints, func(l decision.Lint) bool { return l.Code == "unknown_risk_weight_option" }) {
		t.Errorf("bad spec: %s", body)
	}
	if resp, _ := env.call(http.MethodPost, "/api/evaluators/validate", "", map[string]any{"spec": bad}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous validate: %d", resp.StatusCode)
	}
	if resp, body := env.call(http.MethodPost, "/api/evaluators/validate", testKey, map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no spec: %d %s", resp.StatusCode, body)
	}
	// Writes need a database.
	resp, body = env.call(http.MethodPost, "/api/evaluators", testKey, map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": "x"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read key writes: %d %s", resp.StatusCode, body)
	}
}

func TestEvaluatorWritesNeedADatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: evalAdminKey})
	resp, body := env.call(http.MethodPost, "/api/evaluators", evalAdminKey, map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": "x"})
	if resp.StatusCode != http.StatusServiceUnavailable || decodeErr(t, body).Error.Code != codePersistenceDisabled {
		t.Errorf("no database: %d %s", resp.StatusCode, body)
	}
}

func TestEvaluatorWritesAPI(t *testing.T) {
	env, db := evalEnv(t, nil)
	post := func(path string, body any) (*http.Response, []byte) {
		return env.call(http.MethodPost, path, evalAdminKey, body)
	}

	// #31: create.
	resp, body := post("/api/evaluators", map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": "  First cut  "})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaEvaluatorVersion, body)
	var ver evaluatorVersion
	_ = json.Unmarshal(body, &ver)
	if ver.EvaluatorID != "house_tone" || ver.Version != 1 || ver.CreatedBy != "admin" || ver.Changelog != "First cut" ||
		ver.SpecSHA256 == "" || ver.CalibrationKey == "" || ver.CreatedAt.IsZero() {
		t.Errorf("created version %s", body)
	}
	// The registry already resolves it: GET and an immediate policy save that attaches it.
	if code, body := env.get("/api/evaluators/house_tone", testKey); code != http.StatusOK || !strings.Contains(string(body), `"origin":"dashboard"`) {
		t.Errorf("get after create: %d %s", code, body)
	}
	resp, _ = env.call(http.MethodGet, "/api/policies", evalAdminKey, nil)
	etag := resp.Header.Get("ETag")
	doc := "version: 1\ndefaults: {mode: observe}\nroutes:\n  - name: tone-route\n    match: {headers: {X-TrustAgent-Route: tone-route}}\n" +
		"    mode: flag\n    evaluators: [{ref: house_tone@1}]\n"
	if resp, body := env.call(http.MethodPut, "/api/policies", evalAdminKey, map[string]any{"yaml": doc, "message": "attach"}, "If-Match", etag); resp.StatusCode != http.StatusOK {
		t.Fatalf("attach the new evaluator right away: %d %s", resp.StatusCode, body)
	}

	for _, tc := range []struct {
		name, id string
		status   int
		code     string
	}{
		{"again", "house_tone", http.StatusConflict, codeConflict},
		{"a built-in id", "groundedness", http.StatusConflict, codeConflict},
		{"a file id", "house_style", http.StatusConflict, codeConflict},
	} {
		resp, body := post("/api/evaluators", map[string]any{"spec": toneSpec(t, tc.id, 1), "changelog": ""})
		if resp.StatusCode != tc.status || decodeErr(t, body).Error.Code != tc.code {
			t.Errorf("create %s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
	for _, tc := range []struct {
		name string
		spec map[string]any
		path string
	}{
		{"hard rule", toneSpec(t, "other_tone", 1, func(m map[string]any) {
			m["signal"] = map[string]any{"risk_weights": map[string]any{"no_such_option": 1}}
		}), "spec.signal.risk_weights.no_such_option"},
		{"schema", toneSpec(t, "other_tone", 1, func(m map[string]any) { delete(m, "question") }), "spec"},
		{"built-in claim", toneSpec(t, "other_tone", 1, func(m map[string]any) { m["builtin"] = true }), "spec.builtin"},
		{"version 2", toneSpec(t, "other_tone", 2), "spec.version"},
		{"studio draft id", toneSpec(t, "studio_draft", 1), "spec.id"},
	} {
		resp, body := post("/api/evaluators", map[string]any{"spec": tc.spec, "changelog": ""})
		e := decodeErr(t, body)
		if resp.StatusCode != http.StatusUnprocessableEntity || e.Error.Code != codeValidationFailed ||
			!slices.ContainsFunc(e.Error.FieldErrors, func(f fieldError) bool { return strings.HasPrefix(f.Path, tc.path) }) {
			t.Errorf("create with a %s problem: %d %s", tc.name, resp.StatusCode, body)
		}
	}

	// #32: new versions.
	resp, body = post("/api/evaluators/groundedness/versions", map[string]any{"spec": toneSpec(t, "groundedness", 2), "changelog": "", "base_version": 1})
	if resp.StatusCode != http.StatusForbidden || decodeErr(t, body).Error.Code != codeForbidden {
		t.Errorf("built-in version: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/house_style/versions", map[string]any{"spec": toneSpec(t, "house_style", 2), "changelog": "", "base_version": 1})
	if resp.StatusCode != http.StatusConflict || decodeErr(t, body).Error.Code != codeFileManaged {
		t.Errorf("file version: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/no_such/versions", map[string]any{"spec": toneSpec(t, "no_such", 2), "changelog": "", "base_version": 1})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown evaluator: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/house_tone/versions", map[string]any{"spec": toneSpec(t, "house_tone", 2), "changelog": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no base_version: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/house_tone/versions", map[string]any{"spec": toneSpec(t, "house_tone", 3), "changelog": "", "base_version": 1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("version is not base+1: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/house_tone/versions", map[string]any{"spec": toneSpec(t, "other_tone", 2), "changelog": "", "base_version": 1})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("another id: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/house_tone/versions", map[string]any{"spec": toneSpec(t, "house_tone", 2), "changelog": "Stricter", "base_version": 1})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("version 2: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaEvaluatorVersion, body)
	resp, body = post("/api/evaluators/house_tone/versions", map[string]any{"spec": toneSpec(t, "house_tone", 2), "changelog": "", "base_version": 1})
	if e := decodeErr(t, body); resp.StatusCode != http.StatusPreconditionFailed || e.Error.Code != codeStaleVersion || e.Error.Details["latest_version"] != 2.0 {
		t.Errorf("stale base: %d %s", resp.StatusCode, body)
	}

	// #33: archive. house_tone is attached to tone-route; a second evaluator is not.
	resp, body = post("/api/evaluators/house_tone/archive", map[string]any{"archived": true})
	if e := decodeErr(t, body); resp.StatusCode != http.StatusConflict || e.Error.Code != codeConflict || !strings.Contains(string(body), `"routes":["tone-route"]`) {
		t.Errorf("archive an attached evaluator: %d %s", resp.StatusCode, body)
	}
	if resp, body := post("/api/evaluators", map[string]any{"spec": toneSpec(t, "spare_tone", 1), "changelog": ""}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create spare_tone: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/api/evaluators/spare_tone/archive", map[string]any{"archived": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaEvaluatorMeta, body)
	if !strings.Contains(string(body), `"archived":true`) {
		t.Errorf("archive answer %s", body)
	}
	if _, body := env.get("/api/evaluators", testKey); strings.Contains(string(body), `"spare_tone"`) {
		t.Error("an archived evaluator is listed by default")
	}
	if _, body := env.get("/api/evaluators?include_archived=true", testKey); !strings.Contains(string(body), `"spare_tone"`) {
		t.Error("include_archived does not list the archived evaluator")
	}
	if resp, body := post("/api/evaluators/spare_tone/archive", map[string]any{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("archive without a flag: %d %s", resp.StatusCode, body)
	}
	if resp, body := post("/api/evaluators/spare_tone/archive", map[string]any{"archived": false}); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"archived":false`) {
		t.Errorf("restore: %d %s", resp.StatusCode, body)
	}

	// Each write left its audit row, and the new versions and the archive changes reached the WebSocket.
	var actions []string
	rows, err := db.Query(t.Context(), `SELECT action || ' ' || target FROM admin_audit_log WHERE action LIKE 'evaluator.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	want := []string{"evaluator.create house_tone@1", "evaluator.version house_tone@2", "evaluator.create spare_tone@1",
		"evaluator.archive spare_tone", "evaluator.archive spare_tone"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("audit rows %q, want %q", actions, want)
	}
	var updates []string
	for _, f := range env.wsFrames(t, 200) {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				ID      string `json:"id"`
				Version int    `json:"version"`
				Origin  string `json:"origin"`
			} `json:"data"`
		}
		if json.Unmarshal(f, &ev) == nil && ev.Type == "evaluator_updated" {
			contractstest.ValidateJSON(t, schemaWsEvent, f)
			updates = append(updates, ev.Data.ID+"@"+string(rune('0'+ev.Data.Version))+" "+ev.Data.Origin)
		}
	}
	wantWS := []string{"house_tone@1 dashboard", "house_tone@2 dashboard", "spare_tone@1 dashboard", "spare_tone@1 dashboard", "spare_tone@1 dashboard"}
	if strings.Join(updates, ",") != strings.Join(wantWS, ",") {
		t.Errorf("evaluator_updated events %q, want %q", updates, wantWS)
	}
}

// TestEvaluatorVersionRaceAPI: concurrent saves from the same base: one 201, the rest 412.
func TestEvaluatorVersionRaceAPI(t *testing.T) {
	env, _ := evalEnv(t, nil)
	if resp, body := env.call(http.MethodPost, "/api/evaluators", evalAdminKey, map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": ""}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	const writers = 5
	codes := make([]int, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := env.call(http.MethodPost, "/api/evaluators/house_tone/versions", evalAdminKey,
				map[string]any{"spec": toneSpec(t, "house_tone", 2), "changelog": "", "base_version": 1})
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusPreconditionFailed:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Errorf("%d writers got 201, want 1 (codes %v)", created, codes)
	}
}

func TestEvaluatorWritesReadonlyAndCSRF(t *testing.T) {
	env, _ := evalEnv(t, func(o *envOpts) { o.readonly = true })
	resp, body := env.call(http.MethodPost, "/api/evaluators", evalAdminKey, map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": ""})
	if resp.StatusCode != http.StatusForbidden || decodeErr(t, body).Error.Code != "readonly" {
		t.Errorf("read-only instance: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodPost, "/api/evaluators/validate", testKey, map[string]any{"spec": toneSpec(t, "house_tone", 1)}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("read-only validate: %d %s", resp.StatusCode, body)
	}

	// CONFIG_LOCK pins the policy to its file, not the evaluators (D57).
	locked, _ := evalEnv(t, func(o *envOpts) {
		inner := o.mutate
		o.mutate = func(c *config.Config) { inner(c); c.ConfigLock = true }
	})
	if resp, body := locked.call(http.MethodPost, "/api/evaluators", evalAdminKey, map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": ""}); resp.StatusCode != http.StatusCreated {
		t.Errorf("create under CONFIG_LOCK: %d %s", resp.StatusCode, body)
	}

	// A session cookie needs the CSRF header on writes.
	env, _ = evalEnv(t, nil)
	resp, body = env.call(http.MethodPost, "/api/session", evalAdminKey, map[string]string{"key": evalAdminKey}, csrf...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %d %s", resp.StatusCode, body)
	}
	cookie := sessionCookie(t, resp)
	write := func(headers ...string) (*http.Response, []byte) {
		b, _ := json.Marshal(map[string]any{"spec": toneSpec(t, "house_tone", 1), "changelog": ""})
		req, _ := http.NewRequest(http.MethodPost, env.http.URL+"/api/evaluators", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		req.AddCookie(cookie)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		out, _ := io.ReadAll(r.Body)
		return r, out
	}
	if resp, body := write(); resp.StatusCode != http.StatusForbidden || decodeErr(t, body).Error.Code != codeCSRFFailed {
		t.Errorf("session write without CSRF: %d %s", resp.StatusCode, body)
	}
	if resp, body := write(csrf...); resp.StatusCode != http.StatusCreated {
		t.Errorf("session write with CSRF: %d %s", resp.StatusCode, body)
	}
}

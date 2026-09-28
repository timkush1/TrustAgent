package authz

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestAuthenticate(t *testing.T) {
	a := New([]Key{
		{Label: "app", Secret: "ta-app-secret", Scopes: APIKeyScopes},
		{Label: "ro", Secret: "ta-read", Scopes: []Scope{ScopeRead, ScopeContent}},
		{Label: "empty", Secret: ""},
	}, "")
	if a.IngestMode() != IngestKeys {
		t.Errorf("mode %s: an empty mode means keys", a.IngestMode())
	}
	id, ok := a.Authenticate("ta-app-secret")
	if !ok || id.Label != "app" || !id.Has(ScopeIngest) || !id.Has(ScopeRead) || id.Has(ScopeAdmin) {
		t.Errorf("app identity %+v", id)
	}
	for _, bad := range []string{"", "ta-app-secre", "ta-app-secretx", "TA-APP-SECRET"} {
		if _, ok := a.Authenticate(bad); ok {
			t.Errorf("%q authenticated", bad)
		}
	}
	if !a.IsProxyKey("ta-read") || a.IsProxyKey("sk-upstream") {
		t.Error("IsProxyKey")
	}
	if !a.HasIngestKeys() || New([]Key{{Label: "ro", Secret: "x", Scopes: []Scope{ScopeRead}}}, IngestKeys).HasIngestKeys() {
		t.Error("HasIngestKeys")
	}
}

func TestScopes(t *testing.T) {
	admin := &Identity{Scopes: []Scope{ScopeAdmin}}
	content := &Identity{Scopes: []Scope{ScopeContent}}
	for _, s := range []Scope{ScopeIngest, ScopeRead, ScopeContent, ScopeAdmin} {
		if !admin.Has(s) {
			t.Errorf("admin lacks %s", s)
		}
	}
	if !content.Has(ScopeRead) || content.Has(ScopeIngest) || content.Has(ScopeAdmin) {
		t.Error("content scope implications")
	}
	if (*Identity)(nil).Has(ScopeRead) {
		t.Error("nil identity has no scopes")
	}
}

type denial struct {
	status        int
	code, message string
}

func serve(a *Authenticator, header string) (*httptest.ResponseRecorder, *denial, string) {
	var d *denial
	label := "<not reached>"
	r := gin.New()
	r.POST("/v1/chat/completions", a.Ingest(func(c *gin.Context, status int, code, message string) {
		d = &denial{status, code, message}
		c.Status(status)
	}), func(c *gin.Context) {
		label = KeyLabel(c)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if header != "" {
		req.Header.Set(HeaderKey, header)
	}
	req.Header.Set("Authorization", "Bearer sk-upstream")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, d, label
}

func TestIngestMiddleware(t *testing.T) {
	keys := []Key{{Label: "trafficgen", Secret: "ta-tg", Scopes: APIKeyScopes}, {Label: "ro", Secret: "ta-ro", Scopes: []Scope{ScopeRead}}}

	w, d, label := serve(New(keys, IngestKeys), "ta-tg")
	if w.Code != http.StatusOK || d != nil || label != "trafficgen" {
		t.Errorf("valid key: %d %+v label %q", w.Code, d, label)
	}
	// Authorization is the upstream credential in passthrough mode, never a TrustAgent key.
	if _, d, _ = serve(New(keys, IngestKeys), ""); d == nil || d.status != http.StatusUnauthorized || d.code != CodeUnauthorized {
		t.Errorf("missing key: %+v", d)
	}
	if _, d, _ = serve(New(keys, IngestKeys), "nope"); d == nil || d.message != "Invalid X-TrustAgent-Key." {
		t.Errorf("wrong key: %+v", d)
	}
	if _, d, _ = serve(New(keys, IngestKeys), "ta-ro"); d == nil {
		t.Error("a key without the ingest scope must be refused")
	}
	if _, d, _ = serve(New(nil, IngestKeys), ""); d == nil || d.message == "" || d.message[:2] != "No" {
		t.Errorf("no keys configured: %+v", d)
	}

	w, d, label = serve(New(keys, IngestOpen), "")
	if w.Code != http.StatusOK || d != nil || label != "" {
		t.Errorf("open mode without a key: %d %+v %q", w.Code, d, label)
	}
	if _, d, _ = serve(New(keys, IngestOpen), "nope"); d == nil {
		t.Error("open mode still refuses a wrong key")
	}
}

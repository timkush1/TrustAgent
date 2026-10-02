package authz_test

// This test lives in the external authz_test package because upstream depends on runtimecfg,
// which depends on authz (the master key): an in-package test importing upstream would be an
// import cycle.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/authz/authztest"
	"github.com/truthtable/backend-go/internal/upstream"
)

// TestB1ProxyKeyNeverForwarded: the upstream client strips every TrustAgent key (an env key and a
// minted one) wherever a client put it, and passes the client's own credential through in
// passthrough mode.
func TestB1ProxyKeyNeverForwarded(t *testing.T) {
	const appKey = "ta-app-secret-0001"
	a := authz.NewAuthenticator(authz.Options{
		Keys:       []authz.Key{{Label: "app", Secret: appKey, Scopes: authz.APIKeyScopes}},
		IngestAuth: authz.IngestKeys, Store: authztest.New(),
		SessionKey: []byte(strings.Repeat("k", 32)),
	})
	k, err := a.CreateKey(context.Background(), "ci", []authz.Scope{authz.ScopeIngest}, "test")
	if err != nil {
		t.Fatal(err)
	}
	minted := k.Secret
	up, err := upstream.New(upstream.Config{BaseURL: "https://api.openai.com"}, a.IsProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{appKey, minted} {
		out := up.OutboundHeaders(http.Header{
			"Authorization":    {"Bearer " + key},
			"X-Api-Key":        {key},
			"X-Trustagent-Key": {key},
			"Cookie":           {authz.CookieName + "=abc"},
		})
		for name, values := range out {
			for _, v := range values {
				if strings.Contains(v, key) {
					t.Errorf("%s forwards a proxy key", name)
				}
			}
		}
	}
	out := up.OutboundHeaders(http.Header{"Authorization": {"Bearer sk-upstream-1234567890"}, "X-Trustagent-Key": {appKey}})
	if out.Get("Authorization") != "Bearer sk-upstream-1234567890" || out.Get("X-Trustagent-Key") != "" {
		t.Errorf("passthrough: authorization kept %v, trustagent key dropped %v",
			out.Get("Authorization") == "Bearer sk-upstream-1234567890", out.Get("X-Trustagent-Key") == "")
	}
}

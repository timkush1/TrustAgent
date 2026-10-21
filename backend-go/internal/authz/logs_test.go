package authz

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestNoSecretInLogs drives every authz path that logs (successful and failed key checks, a
// database outage, sessions, tickets, the setup claim) with a debug logger and checks that no key,
// session cookie, ticket or setup token appears in the output.
func TestNoSecretInLogs(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Keys = o.Keys[:2] })
	ctx := context.Background()
	var out bytes.Buffer
	_, _, err := f.a.Setup().Announce(ctx, &out, "")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(strings.TrimSpace(out.String()), SetupLinePrefix+"http://localhost:3000/app/setup#token=")
	_, adminSecret, err := f.a.Setup().Claim(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	minted, _ := f.mint(t, "ci", ScopeIngest)
	cookie, sess := f.login(t, appKey)
	ticket, _ := f.a.Tickets().Issue(sess)
	for _, r := range []*http.Request{
		request(http.MethodGet, "/api/traces", "", HeaderKey, appKey),
		request(http.MethodGet, "/api/traces", "", HeaderKey, "wrong-"+appKey),
		request(http.MethodGet, "/api/traces", cookie),
		request(http.MethodPost, "/api/keys", cookie),
		request(http.MethodGet, "/ws?ticket="+ticket, ""),
		request(http.MethodGet, "/ws?ticket="+ticket, ""),
		request(http.MethodGet, "/api/traces", "", "Authorization", "Bearer "+adminSecret),
	} {
		f.a.Resolve(r, SurfaceOf(r.URL.Path))
	}
	f.clock.Advance(time.Hour)
	f.st.SetErr(errors.New("connection refused"))
	f.a.Resolve(request(http.MethodGet, "/api/traces", "", HeaderKey, minted), SurfaceAPI)
	f.a.Resolve(request(http.MethodGet, "/api/traces", cookie), SurfaceAPI)
	_, _ = f.a.AuthenticateContext(ctx, adminSecret)

	f.a.Wait() // background last-used updates log their failures too
	logs := f.logs.String()
	if !strings.Contains(logs, "api key lookup failed") {
		t.Fatalf("the outage was not logged:\n%s", logs)
	}
	for name, secret := range map[string]string{
		"env key": appKey, "viewer key": viewerKey, "setup token": token, "admin key": adminSecret,
		"minted key": minted, "session cookie": cookie, "ticket": ticket,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the %s appears in the logs:\n%s", name, logs)
		}
	}
}

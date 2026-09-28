package authz

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestCSRF: cookie-authenticated writes need X-TrustAgent-CSRF: 1 and an allow-listed Origin;
// reads and key-authenticated writes do not.
func TestCSRF(t *testing.T) {
	f := newFixture(t, nil)
	cookie, _ := f.login(t, rootKey)
	post := func(headers ...string) Result {
		return f.a.Resolve(request(http.MethodPost, "/api/keys", cookie, headers...), SurfaceAPI)
	}
	for name, res := range map[string]Result{
		"missing header":  post("Origin", "http://localhost:3000"),
		"header not 1":    post(HeaderCSRF, "true", "Origin", "http://localhost:3000"),
		"missing Origin":  post(HeaderCSRF, "1"),
		"wrong Origin":    post(HeaderCSRF, "1", "Origin", "https://evil.example"),
		"null Origin":     post(HeaderCSRF, "1", "Origin", "null"),
		"scheme mismatch": post(HeaderCSRF, "1", "Origin", "https://localhost:3000"),
	} {
		if res.Err == nil || res.Err.Status != http.StatusForbidden || res.Err.Code != CodeCSRFFailed || res.Identity != nil {
			t.Errorf("%s: %+v", name, res)
		}
	}
	for name, origin := range map[string]string{
		"public URL":            "http://localhost:3000",
		"allowed origin":        "http://localhost:5173",
		"case and default port": "HTTP://LOCALHOST:5173",
	} {
		if res := post(HeaderCSRF, "1", "Origin", origin); res.Err != nil || res.Identity == nil {
			t.Errorf("%s: %+v", name, res)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if res := f.a.Resolve(request(m, "/api/traces", cookie), SurfaceAPI); res.Err != nil || res.Identity == nil {
			t.Errorf("%s needs no CSRF: %+v", m, res)
		}
	}
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if res := f.a.Resolve(request(m, "/api/policies", cookie), SurfaceAPI); res.Err == nil || res.Err.Code != CodeCSRFFailed {
			t.Errorf("%s through a cookie needs CSRF: %+v", m, res)
		}
	}
	if res := f.a.Resolve(request(http.MethodPost, "/api/keys", cookie, HeaderKey, rootKey), SurfaceAPI); res.Err != nil {
		t.Errorf("a key header needs no CSRF: %+v", res)
	}

	// Login: the header is required; an Origin, when present, must be allowed.
	for name, c := range map[string]struct {
		headers []string
		ok      bool
	}{
		"no header":           {nil, false},
		"header, no Origin":   {[]string{HeaderCSRF, "1"}, true},
		"header, good Origin": {[]string{HeaderCSRF, "1", "Origin", "http://localhost:3000"}, true},
		"header, bad Origin":  {[]string{HeaderCSRF, "1", "Origin", "http://evil.example"}, false},
	} {
		err := f.a.CheckLoginCSRF(request(http.MethodPost, "/api/session", "", c.headers...))
		if (err == nil) != c.ok {
			t.Errorf("login %s: %v", name, err)
		}
	}
	for in, want := range map[string]string{
		"http://localhost:80": "http://localhost", "https://x.example:443/app": "https://x.example",
		"http://[::1]:3000": "http://[::1]:3000", "ftp://x": "", "localhost:3000": "", "": "",
	} {
		if got := normalizeOrigin(in); got != want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWSTicketSingleUseAndExpiry: a ticket works once, only before its TTL, only on /ws.
func TestWSTicketSingleUseAndExpiry(t *testing.T) {
	f := newFixture(t, nil)
	_, sess := f.login(t, viewerKey)
	tickets := f.a.Tickets()
	if tickets.TTL() != 30*time.Second {
		t.Errorf("ttl %v", tickets.TTL())
	}
	tk, err := tickets.Issue(sess)
	if err != nil || len(tk) < 40 {
		t.Fatalf("ticket %q %v", tk, err)
	}
	res := f.a.Resolve(request(http.MethodGet, "/ws?ticket="+tk, ""), SurfaceWS)
	if res.Identity == nil || res.Identity.Label != "viewer" || res.Identity.Kind != KindTicket || !res.Identity.Has(ScopeContent) {
		t.Fatalf("redeem: %+v", res)
	}
	if res := f.a.Resolve(request(http.MethodGet, "/ws?ticket="+tk, ""), SurfaceWS); res.Identity != nil || res.Err == nil || res.Err.Status != http.StatusUnauthorized {
		t.Errorf("a ticket works once: %+v", res)
	}

	expiring, _ := tickets.Issue(sess)
	f.clock.Advance(30 * time.Second)
	if _, ok := tickets.Consume(expiring); ok {
		t.Error("a ticket expires after 30 s")
	}
	onAPI, _ := tickets.Issue(sess)
	if res := f.a.Resolve(request(http.MethodGet, "/api/traces?ticket="+onAPI, ""), SurfaceAPI); res.Identity != nil {
		t.Error("tickets only work on /ws")
	}
	if _, ok := tickets.Consume(onAPI); !ok {
		t.Error("the ticket presented to /api was not consumed")
	}
	if _, err := tickets.Issue(nil); err == nil {
		t.Error("a ticket needs an identity")
	}

	// Concurrent redemption: exactly one wins.
	race, _ := tickets.Issue(sess)
	var wg sync.WaitGroup
	wins := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := tickets.Consume(race)
			wins <- ok
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for ok := range wins {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d concurrent redemptions succeeded", n)
	}

	// A full store sweeps expired tickets before refusing.
	small := NewTickets(time.Second, f.clock.Now)
	for i := 0; i < maxTickets; i++ {
		if _, err := small.Issue(sess); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, err := small.Issue(sess); err != ErrTooManyTickets {
		t.Errorf("full: %v", err)
	}
	f.clock.Advance(time.Second)
	if _, err := small.Issue(sess); err != nil {
		t.Errorf("expired tickets are swept: %v", err)
	}
}

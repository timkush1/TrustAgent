package jev

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTransportSettings(t *testing.T) {
	tr := NewTransport()
	if tr.MaxIdleConnsPerHost < 64 || tr.IdleConnTimeout != 5*time.Minute || !tr.ForceAttemptHTTP2 || tr.TLSClientConfig.MinVersion == 0 {
		t.Errorf("transport %+v", tr)
	}
}

// TestTransportSpeaksHTTP2 checks that the dedicated transport negotiates HTTP/2 over TLS and that
// a second request reuses the warm connection (connect_ms 0).
func TestTransportSpeaksHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"d","model":"typesafe/jev-1.13-20260917","answers":{"q":{"type":"noul","noul":0.1}},"usage":{"input_tokens":300}}`)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tr := NewTransport()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr.TLSClientConfig.RootCAs = pool
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("protocol %s, want HTTP/2", resp.Proto)
	}

	hc := DefaultHTTPConfig()
	hc.Transport, hc.KeepAlive = tr, 0
	c, err := New(Config{Backend: BackendCompatible, JevAPIKey: "k", BaseURL: srv.URL, Model: "typesafe-ai/jev", HTTP: &hc})
	if err != nil {
		t.Fatal(err)
	}
	req := &Request{State: map[string]any{"x": "y"}, Questions: map[string]any{"q": map[string]any{"type": "noul", "instructions": "i"}}}
	for i := range 2 {
		r, err := decide(t, c, req, CallOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if r.ConnectMs == nil || *r.ConnectMs != 0 {
			t.Errorf("call %d: connect_ms %v, want 0 on the warm HTTP/2 connection", i, r.ConnectMs)
		}
	}
}

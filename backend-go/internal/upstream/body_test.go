package upstream

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// sseServer serves an event stream: every event after gap, flushed; stall, when set, then waits
// for the request to go away instead of ending the stream. aborted reports that it did.
func sseServer(t *testing.T, events []string, gap time.Duration, stall bool, aborted *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range events {
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				aborted.Store(true)
				return
			}
			_, _ = io.WriteString(w, ev)
			w.(http.Flusher).Flush()
		}
		if stall {
			select {
			case <-r.Context().Done():
				aborted.Store(true)
			case <-time.After(10 * time.Second):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func timedClient(t *testing.T, base string, total, idle time.Duration) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: base, Timeout: total, IdleTimeout: idle}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func events(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"
	}
	out[n-1] = "data: [DONE]\n\n"
	return out
}

// TestStreamIdleTimeout: a streamed body is bound by the idle timeout between reads, not by the
// total timeout: a slow, steady stream that outlasts the total timeout arrives whole, a stalled
// one is cut off after the idle timeout with a timeout error, and the upstream request is
// cancelled.
func TestStreamIdleTimeout(t *testing.T) {
	var aborted atomic.Bool
	steady := sseServer(t, events(12), 25*time.Millisecond, false, &aborted)
	c := timedClient(t, steady.URL, 100*time.Millisecond, 400*time.Millisecond)
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body := c.StreamBody(resp, cancel)
	b, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || strings.Count(string(b), "data: ") != 12 {
		t.Fatalf("a steady stream longer than the total timeout: %d events, %v", strings.Count(string(b), "data: "), err)
	}

	stalled := sseServer(t, events(2), 0, true, &aborted)
	c = timedClient(t, stalled.URL, time.Minute, 150*time.Millisecond)
	resp, cancel, err = c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body = c.StreamBody(resp, cancel)
	defer body.Close()
	began := time.Now()
	b, err = io.ReadAll(body)
	if !IsTimeout(err) || !errors.Is(err, errIdleTimeout) || strings.Count(string(b), "data: ") != 2 {
		t.Fatalf("a stalled stream: %q, %v", b, err)
	}
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("cut off after %s", d)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !aborted.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !aborted.Load() {
		t.Error("the stalled upstream request was not cancelled")
	}
}

// TestDoTotalTimeoutBoundsJSON: the total timeout covers the wait for the answer and the read of a
// JSON body, with an error IsTimeout reports (504, not 502).
func TestDoTotalTimeoutBoundsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // then the server notices the client going away
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"choices":`)
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	c := timedClient(t, srv.URL, 150*time.Millisecond, time.Minute)

	// No answer at all.
	_, _, err := c.Do(context.Background(), http.MethodPost, "/v1/messages", "", http.Header{}, []byte(`{}`))
	if !IsTimeout(err) || !errors.Is(err, errTotalTimeout) {
		t.Errorf("no headers in time: %v", err)
	}
	// Headers, then a body that stops.
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer resp.Body.Close()
	if _, _, err := ReadBody(resp, 1<<20); !IsTimeout(err) || !errors.Is(err, errTotalTimeout) {
		t.Errorf("a JSON body that stops: %v", err)
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestStreamBodyDecodesGzipSSE (B2): a gzip-encoded event stream arrives decoded, whether the
// transport negotiated gzip itself or the encoding is stray (the transport did not ask for it).
func TestStreamBodyDecodesGzipSSE(t *testing.T) {
	stream := strings.Join(events(3), "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipped(t, stream))
	}))
	defer srv.Close()
	for _, stray := range []bool{false, true} {
		cfg := Config{BaseURL: srv.URL, IdleTimeout: time.Second}
		if stray {
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.DisableCompression = true
			cfg.Transport = tr
		}
		c, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{"Accept-Encoding": {"br"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		body := c.StreamBody(resp, cancel)
		b, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || string(b) != stream || resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("stray %v: %q %v %v", stray, b, err, resp.Header)
		}
	}
}

// TestDecodeBody: gzip, zlib and raw deflate decode; an unknown encoding and broken encoded bytes
// fail with ErrUndecodable; a body cut short keeps its own error; a second call is a no-op.
func TestDecodeBody(t *testing.T) {
	const text = "data: {\"x\":1}\n\n"
	var zl, raw bytes.Buffer
	zw := zlib.NewWriter(&zl)
	_, _ = zw.Write([]byte(text))
	_ = zw.Close()
	fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	_, _ = fw.Write([]byte(text))
	_ = fw.Close()
	gz := gzipped(t, text)

	resp := func(enc string, body []byte) *http.Response {
		return &http.Response{Header: http.Header{"Content-Encoding": {enc}, "Content-Length": {"9"}}, ContentLength: 9,
			Body: io.NopCloser(bytes.NewReader(body))}
	}
	for _, c := range []struct {
		enc  string
		body []byte
	}{{"gzip", gz}, {"x-gzip", gz}, {"GZIP", gz}, {"deflate", zl.Bytes()}, {"deflate", raw.Bytes()}, {"identity", []byte(text)}} {
		r := resp(c.enc, c.body)
		b, err := io.ReadAll(DecodeBody(r))
		if err != nil || string(b) != text || r.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: %q %v %v", c.enc, b, err, r.Header)
		}
		if c.enc != "identity" && (r.Header.Get("Content-Length") != "" || r.ContentLength != -1 || !r.Uncompressed) {
			t.Errorf("%s: the length and encoding describe the decoded body: %v %d", c.enc, r.Header, r.ContentLength)
		}
	}
	for _, c := range []struct {
		enc  string
		body []byte
	}{{"br", []byte("whatever")}, {"gzip", []byte("not gzip at all")}, {"deflate", []byte{0x78, 0x9c, 0xff, 0xff, 0xff}}} {
		if _, err := io.ReadAll(DecodeBody(resp(c.enc, c.body))); !errors.Is(err, ErrUndecodable) {
			t.Errorf("%s %q: %v, want ErrUndecodable", c.enc, c.body, err)
		}
	}
	if _, err := io.ReadAll(DecodeBody(resp("gzip", gz[:len(gz)-6]))); err == nil || errors.Is(err, ErrUndecodable) {
		t.Errorf("a body cut short is not undecodable: %v", err)
	}
	r := resp("gzip", gz)
	first := DecodeBody(r)
	if second := DecodeBody(r); second != first {
		t.Error("DecodeBody twice decodes once")
	}
	if b, err := io.ReadAll(first); err != nil || string(b) != text {
		t.Errorf("%q %v", b, err)
	}
	plain := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(text))}
	if DecodeBody(plain) != plain.Body {
		t.Error("an unencoded body is returned as it is")
	}
}

// upstreamSamples is the upstream_seconds sample count of format.
func upstreamSamples(t *testing.T, format string) uint64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "trustagent_gateway_upstream_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "format" && l.GetValue() == format {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

// TestUpstreamSecondsOncePerAnswer: every answer Do fetched is observed once under its API format,
// when its body ends (EOF, a read error or Close), JSON and streamed alike.
func TestUpstreamSecondsOncePerAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	c := timedClient(t, srv.URL, time.Minute, time.Minute)
	chat, messages := upstreamSamples(t, "openai_chat"), upstreamSamples(t, "anthropic_messages")

	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", http.Header{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBody(resp, 1024); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	resp, cancel, err = c.Do(context.Background(), http.MethodPost, "/v1/messages", "", http.Header{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	body := c.StreamBody(resp, cancel)
	if got := StartedAt(resp); got.IsZero() || got.After(began) {
		t.Errorf("StartedAt %v, stream taken over at %v", got, began)
	}
	_, _ = io.ReadAll(body)
	_ = body.Close()
	_ = body.Close()
	if got := upstreamSamples(t, "openai_chat") - chat; got != 1 {
		t.Errorf("openai_chat observed %d times", got)
	}
	if got := upstreamSamples(t, "anthropic_messages") - messages; got != 1 {
		t.Errorf("anthropic_messages observed %d times", got)
	}
	if !StartedAt(&http.Response{Body: io.NopCloser(strings.NewReader(""))}).IsZero() || !StartedAt(nil).IsZero() {
		t.Error("a response Do did not make has no start")
	}
}

// TestStreamBodyWithoutDo: a response Do did not make still gets the idle timeout; the timer
// cancels it and closes the body, which ends the waiting read. Close calls cancel once.
func TestStreamBodyWithoutDo(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	var cancels atomic.Int32
	c := timedClient(t, "http://upstream.invalid", 0, 100*time.Millisecond)
	resp := &http.Response{Header: http.Header{}, Body: pr}
	body := c.StreamBody(resp, func() { cancels.Add(1) })
	go func() { _, _ = io.WriteString(pw, "data: 1\n\n") }()
	buf := make([]byte, 64)
	if n, err := body.Read(buf); err != nil || string(buf[:n]) != "data: 1\n\n" {
		t.Fatalf("%q %v", buf[:n], err)
	}
	if _, err := body.Read(buf); !IsTimeout(err) {
		t.Errorf("a stalled pipe: %v", err)
	}
	_ = body.Close()
	_ = body.Close()
	if n := cancels.Load(); n != 2 { // once by the idle timer, once by Close
		t.Errorf("cancel called %d times", n)
	}
}

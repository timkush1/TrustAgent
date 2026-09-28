package llmapi

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// readAll reads every event of r, failing the test on an error other than io.EOF.
func readAll(t *testing.T, r *Reader) []Event {
	t.Helper()
	var out []Event
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
		if len(out) > 10000 {
			t.Fatal("reader does not terminate")
		}
	}
}

// chunked returns a reader that hands out src in pieces of the given sizes (cycled).
type chunked struct {
	src   []byte
	sizes []int
	i     int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.src) == 0 {
		return 0, io.EOF
	}
	n := c.sizes[c.i%len(c.sizes)]
	c.i++
	n = min(n, len(p), len(c.src))
	copy(p, c.src[:n])
	c.src = c.src[n:]
	return n, nil
}

// view is the comparable part of an event.
type view struct {
	Name       string
	Data       string
	HasData    bool
	ID         string
	HasID      bool
	Retry      int
	HasRetry   bool
	Comments   []string
	Incomplete bool
}

// viewOf returns the comparable part of the events, without the field-less events a stream may end
// with (their bytes depend on how the stream was cut; see Reader.Next).
func viewOf(evs []Event) []view {
	for len(evs) > 0 {
		e := evs[len(evs)-1]
		if e.HasData || e.HasID || e.HasRetry || e.Name != "" || len(e.Comments) > 0 || e.Incomplete {
			break
		}
		evs = evs[:len(evs)-1]
	}
	out := make([]view, len(evs))
	for i, e := range evs {
		out[i] = view{e.Name, string(e.Data), e.HasData, e.ID, e.HasID, e.Retry, e.HasRetry, e.Comments, e.Incomplete}
	}
	return out
}

func TestReaderParsesTheEventStreamFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []view
	}{
		{"lf", "data: a\n\ndata: b\n\n", []view{{Data: "a", HasData: true}, {Data: "b", HasData: true}}},
		{"crlf", "data: a\r\n\r\nevent: x\r\ndata: b\r\n\r\n", []view{{Data: "a", HasData: true}, {Name: "x", Data: "b", HasData: true}}},
		{"cr", "data: a\r\rdata: b\r\r", []view{{Data: "a", HasData: true}, {Data: "b", HasData: true}}},
		{"mixed", "data: a\r\ndata: b\rdata: c\n\r\n", []view{{Data: "a\nb\nc", HasData: true}}},
		{"multi-line data", "data: {\"a\":\ndata: 1}\n\n", []view{{Data: "{\"a\":\n1}", HasData: true}}},
		{"no space after colon", "data:x\nevent:y\n\n", []view{{Name: "y", Data: "x", HasData: true}}},
		{"only one space is stripped", "data:  two\n\n", []view{{Data: " two", HasData: true}}},
		{"field without colon", "data\n\n", []view{{Data: "", HasData: true}}},
		{"empty data lines", "data:\ndata:\n\n", []view{{Data: "\n", HasData: true}}},
		{"comments", ": OPENROUTER PROCESSING\n\n:no-space\n\n", []view{{Comments: []string{"OPENROUTER PROCESSING"}}, {Comments: []string{"no-space"}}}},
		{"comment inside an event", "event: e\n: note\ndata: d\n\n", []view{{Name: "e", Data: "d", HasData: true, Comments: []string{"note"}}}},
		{"id and retry", "id: 7\nretry: 1500\ndata: d\n\n", []view{{Data: "d", HasData: true, ID: "7", HasID: true, Retry: 1500, HasRetry: true}}},
		{"bad retry and NUL id ignored", "retry: 1.5\nid: a\x00b\ndata: d\n\n", []view{{Data: "d", HasData: true}}},
		{"stray blank lines", "\n\n\ndata: a\n\n\n\n", []view{{Data: "a", HasData: true}}},
		{"bom", "\xEF\xBB\xBFdata: a\n\n", []view{{Data: "a", HasData: true}}},
		{"bom then blank line", "\xEF\xBB\xBF\ndata: a\n\n", []view{{Data: "a", HasData: true}}},
		{"only the first bom", "data: a\n\n\xEF\xBB\xBFdata: b\n\ndata: c\n\n", []view{{Data: "a", HasData: true}, {}, {Data: "c", HasData: true}}},
		{"unknown field then data", "foo: bar\n\ndata: a\n\n", []view{{}, {Data: "a", HasData: true}}},
		{"incomplete tail", "data: a\n\ndata: b\n", []view{{Data: "a", HasData: true}, {Data: "b", HasData: true, Incomplete: true}}},
		{"unterminated last line", "data: a\n\ndata: [DONE]", []view{{Data: "a", HasData: true}, {Data: "[DONE]", HasData: true, Incomplete: true}}},
		{"last event name wins", "event: a\nevent: b\ndata: d\n\n", []view{{Name: "b", Data: "d", HasData: true}}},
		{"empty stream", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Whole, one byte at a time, and in odd chunks: the result must not depend on how the
			// stream was cut.
			readers := map[string]func() io.Reader{
				"whole":    func() io.Reader { return strings.NewReader(tc.in) },
				"one byte": func() io.Reader { return iotest.OneByteReader(strings.NewReader(tc.in)) },
				"chunks":   func() io.Reader { return &chunked{src: []byte(tc.in), sizes: []int{3, 1, 7, 2}} },
				"half":     func() io.Reader { return iotest.HalfReader(strings.NewReader(tc.in)) },
			}
			for rname, mk := range readers {
				evs := readAll(t, NewReader(mk()))
				got := viewOf(evs)
				if tc.want == nil && len(got) == 0 {
					if raw := EncodeEvents(evs); string(raw) != tc.in {
						t.Errorf("%s: raw %q", rname, raw)
					}
					continue
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: got %+v\nwant %+v", rname, got, tc.want)
				}
				if raw := EncodeEvents(evs); string(raw) != tc.in {
					t.Errorf("%s: raw bytes %q do not reproduce the input %q", rname, raw, tc.in)
				}
			}
		})
	}
}

func TestReaderDispatchesACRTerminatedEventWithoutWaiting(t *testing.T) {
	// The blank line of the first event is a lone CR at the end of what has arrived: the event
	// must come out before the next byte does (a relay would otherwise stall a whole event).
	pr, pw := io.Pipe()
	r := NewReader(pr)
	go func() { _, _ = pw.Write([]byte("data: a\r\r")) }()
	ev, err := r.Next()
	if err != nil || string(ev.Data) != "a" {
		t.Fatalf("got %+v, %v", ev, err)
	}
	// The LF that follows belongs to the CR before it.
	go func() {
		_, _ = pw.Write([]byte("\ndata: b\r\n\r\n"))
		_ = pw.Close()
	}()
	ev, err = r.Next()
	if err != nil || string(ev.Data) != "b" || ev.Incomplete {
		t.Fatalf("got %+v, %v", ev, err)
	}
	if string(ev.Raw) != "\ndata: b\r\n\r\n" {
		t.Errorf("raw %q", ev.Raw)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("err %v, want EOF", err)
	}
}

func TestReaderRawReproducesFixturesUnderRandomChunking(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "streams", "*.sse"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v", err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, variant := range lineEndings(src) {
			want := viewOf(readAll(t, NewReader(bytes.NewReader(variant))))
			for range 20 {
				sizes := []int{1 + rng.IntN(9), 1 + rng.IntN(64), 1 + rng.IntN(3)}
				evs := readAll(t, NewReader(&chunked{src: variant, sizes: sizes}))
				if !bytes.Equal(EncodeEvents(evs), variant) {
					t.Fatalf("%s sizes %v: raw bytes differ from the input", f, sizes)
				}
				if got := viewOf(evs); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s sizes %v: events differ", f, sizes)
				}
			}
		}
	}
}

// lineEndings returns src with LF, CRLF and CR line endings.
func lineEndings(src []byte) [][]byte {
	return [][]byte{
		src,
		bytes.ReplaceAll(src, []byte("\n"), []byte("\r\n")),
		bytes.ReplaceAll(src, []byte("\n"), []byte("\r")),
	}
}

func TestReaderSizeLimit(t *testing.T) {
	r := NewReader(strings.NewReader("data: " + strings.Repeat("x", 100) + "\n\n"))
	r.SetMaxEventBytes(32)
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("err %v, want ErrEventTooLarge", err)
	}
	// Events under the limit pass, however many there are.
	r = NewReader(strings.NewReader(strings.Repeat("data: 0123456789\n\n", 50)))
	r.SetMaxEventBytes(32)
	if n := len(readAll(t, r)); n != 50 {
		t.Errorf("got %d events", n)
	}
	r.SetMaxEventBytes(0)
	if r.max != DefaultMaxEventBytes {
		t.Error("SetMaxEventBytes(0) restores the default")
	}
}

type failAfter struct {
	data []byte
	err  error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestReaderReadErrors(t *testing.T) {
	boom := errors.New("connection reset")
	r := NewReader(&failAfter{data: []byte("data: a\n\ndata: {\"trunc"), err: boom})
	ev, err := r.Next()
	if err != nil || string(ev.Data) != "a" {
		t.Fatalf("first: %+v %v", ev, err)
	}
	ev, err = r.Next()
	if err != nil || !ev.Incomplete || string(ev.Data) != `{"trunc` {
		t.Fatalf("partial: %+v %v", ev, err)
	}
	if _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("err %v, want the read error", err)
	}
	if _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("the error is sticky, got %v", err)
	}

	// A reader that keeps returning (0, nil) is reported instead of spinning forever.
	r = NewReader(iotest.ErrReader(nil))
	if _, err := r.Next(); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("err %v, want io.ErrNoProgress", err)
	}

	// Data and an error in the same Read are both used.
	r = NewReader(iotest.DataErrReader(strings.NewReader("data: x\n\n")))
	if ev, err := r.Next(); err != nil || string(ev.Data) != "x" {
		t.Fatalf("got %+v %v", ev, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v", err)
	}
}

func TestEventEncodeRoundTrips(t *testing.T) {
	evs := []Event{
		DataEvent("message_start", []byte(`{"a":1}`)),
		DataEvent("", []byte("line1\nline2\r\nline3\rline4")),
		CommentEvent("trustagent {\"x\":1}\nsecond line"),
		{Name: "n\ne", ID: "i\rd", HasID: true, Retry: 10, HasRetry: true, Data: []byte(""), HasData: true},
	}
	got := readAll(t, NewReader(bytes.NewReader(EncodeEvents(evs))))
	want := []view{
		{Name: "message_start", Data: `{"a":1}`, HasData: true},
		{Data: "line1\nline2\nline3\nline4", HasData: true},
		{Comments: []string{`trustagent {"x":1}`, "second line"}},
		{Name: "n e", ID: "i d", HasID: true, Retry: 10, HasRetry: true, HasData: true},
	}
	if !reflect.DeepEqual(viewOf(got), want) {
		t.Errorf("got %+v\nwant %+v", viewOf(got), want)
	}
	if b := DataEvent("x", []byte("d")).Encode(); string(b) != "event: x\ndata: d\n\n" {
		t.Errorf("encode %q", b)
	}
	read := got[0]
	if !bytes.Equal(read.Bytes(), read.Raw) {
		t.Error("a read event's bytes are its Raw")
	}
}

func TestEventType(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{DataEvent("message_stop", []byte(`{"type":"other"}`)), "message_stop"},
		{DataEvent("", []byte(`{"type":"message_stop"}`)), "message_stop"},
		{DataEvent("", []byte(`[DONE]`)), ""},
		{CommentEvent("x"), ""},
	}
	for _, tc := range cases {
		if got := tc.ev.Type(); got != tc.want {
			t.Errorf("%+v: type %q, want %q", tc.ev, got, tc.want)
		}
	}
}

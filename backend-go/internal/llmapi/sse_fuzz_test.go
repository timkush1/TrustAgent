package llmapi

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

// referenceEvents parses src the plain way the event-stream format describes it, with the whole
// input at hand: strip one leading BOM, split lines at CRLF, LF or CR, and dispatch at each blank
// line. Blocks of unknown fields only carry nothing and are left out; a block the input ends in
// without its blank line is Incomplete.
func referenceEvents(src []byte) []view {
	src = bytes.TrimPrefix(src, utf8BOM)
	var (
		out  []view
		cur  *view
		data []byte
	)
	emit := func(incomplete bool) {
		if cur == nil {
			return
		}
		if cur.HasData {
			cur.Data = string(bytes.TrimSuffix(data, []byte("\n")))
		}
		cur.Incomplete = incomplete
		if cur.HasData || cur.Name != "" || len(cur.Comments) > 0 || cur.HasID || cur.HasRetry || incomplete {
			out = append(out, *cur)
		}
		cur, data = nil, nil
	}
	apply := func(line []byte) {
		if cur == nil {
			cur = &view{}
		}
		if line[0] == ':' {
			cur.Comments = append(cur.Comments, string(trimOneSpace(line[1:])))
			return
		}
		field, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], trimOneSpace(line[i+1:])
		}
		switch string(field) {
		case "event":
			cur.Name = string(value)
		case "data":
			cur.HasData = true
			data = append(append(data, value...), '\n')
		case "id":
			if bytes.IndexByte(value, 0) < 0 {
				cur.ID, cur.HasID = string(value), true
			}
		case "retry":
			if n, ok := atoiDigits(value); ok {
				cur.Retry, cur.HasRetry = n, true
			}
		}
	}
	for len(src) > 0 {
		i := bytes.IndexAny(src, "\r\n")
		if i < 0 {
			apply(src)
			break
		}
		line, next := src[:i], i+1
		if src[i] == '\r' && next < len(src) && src[next] == '\n' {
			next++
		}
		src = src[next:]
		if len(line) == 0 {
			emit(false)
			continue
		}
		apply(line)
	}
	emit(true)
	return out
}

// meaningful drops the events that carry nothing (stray bytes, unknown fields only).
func meaningful(evs []Event) []view {
	var out []view
	for _, v := range viewOf(evs) {
		if v.HasData || v.Name != "" || len(v.Comments) > 0 || v.HasID || v.HasRetry || v.Incomplete {
			out = append(out, v)
		}
	}
	return out
}

// FuzzReader checks the Reader against referenceEvents, whatever the input and however it is cut:
// the events agree, and their Raw bytes reproduce the input. Run it longer with
// go test -run '^$' -fuzz FuzzReader ./internal/llmapi/.
func FuzzReader(f *testing.F) {
	for _, seed := range []string{
		"data: a\n\n", "data: a\r\n\r\n", "data: a\r\rdata: b\r\r", "data: a\r\ndata: b\rdata: c\n\r\n",
		"\xEF\xBB\xBFdata: a\n\n", "\xEF\xBB\xBF\r\n", ": c\n\nevent: e\ndata\n\n", "id: 1\nretry: 20\ndata:x\n\n",
		"data: [DONE]", "data: x\r", "\n\n\ndata: a\n\n\n", "event: message_stop\ndata: {}\r\n\r\n",
	} {
		f.Add([]byte(seed), uint8(1), uint8(3))
	}
	f.Fuzz(func(t *testing.T, src []byte, a, b uint8) {
		if len(src) > 1<<12 {
			return
		}
		want := referenceEvents(src)
		for _, sizes := range [][]int{{len(src) + 1}, {1 + int(a%16), 1 + int(b%16)}, {1}} {
			var evs []Event
			r := NewReader(&chunked{src: src, sizes: sizes})
			for {
				ev, err := r.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("sizes %v: %v", sizes, err)
				}
				evs = append(evs, ev)
				if len(evs) > len(src)+1 {
					t.Fatalf("sizes %v: the reader does not terminate", sizes)
				}
			}
			if raw := EncodeEvents(evs); !bytes.Equal(raw, src) {
				t.Fatalf("sizes %v: raw %q, input %q", sizes, raw, src)
			}
			if got := meaningful(evs); !reflect.DeepEqual(got, want) {
				t.Fatalf("sizes %v, input %q:\n got %+v\nwant %+v", sizes, src, got, want)
			}
		}
	})
}

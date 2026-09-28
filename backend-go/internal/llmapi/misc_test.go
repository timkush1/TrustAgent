package llmapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// flushFailer is a ResponseWriter whose Flush reports that the client went away.
type flushFailer struct{ *httptest.ResponseRecorder }

func (f flushFailer) FlushError() error { return errClientGone }

func TestStreamWriterFlushError(t *testing.T) {
	sw := NewStreamWriter(flushFailer{httptest.NewRecorder()})
	if err := sw.WriteEvent(DataEvent("", []byte("x"))); !errors.Is(err, errClientGone) {
		t.Fatalf("err %v", err)
	}
	if err := sw.Commit(); !errors.Is(err, errClientGone) {
		t.Errorf("the flush error is sticky: %v", err)
	}
	sw = NewStreamWriter(flushFailer{httptest.NewRecorder()})
	if err := sw.Commit(); !errors.Is(err, errClientGone) {
		t.Errorf("commit: %v", err)
	}
}

func TestStreamErrorShapes(t *testing.T) {
	cases := []struct {
		chunk string
		want  StreamError
		msg   string
	}{
		{`{"error":"upstream timed out"}`, StreamError{Message: "upstream timed out"}, "llmapi: upstream stream error: upstream timed out"},
		{`{"error":{"message":"m","code":"rate_limited"}}`, StreamError{Code: "rate_limited", Message: "m"}, "llmapi: upstream stream error rate_limited: m"},
		{`{"error":{"code":{"nested":true}}}`, StreamError{Code: `{"nested":true}`}, `llmapi: upstream stream error {"nested":true}`},
		{`{"error":{}}`, StreamError{}, "llmapi: upstream stream error"},
	}
	for _, tc := range cases {
		asm, _ := NewStreamAssembler(FormatOpenAICompletions)
		if k := asm.Add(DataEvent("", []byte(tc.chunk))); k != EventError {
			t.Errorf("%s: kind %v", tc.chunk, k)
		}
		if got := asm.Err(); got == nil || *got != tc.want || got.Error() != tc.msg {
			t.Errorf("%s: %+v %q", tc.chunk, got, got.Error())
		}
	}
	// "error": null is no error.
	asm, _ := NewStreamAssembler(FormatOpenAIChat)
	if k := asm.Add(DataEvent("", []byte(`{"error":null,"choices":[]}`))); k != EventOther || asm.Err() != nil {
		t.Errorf("null error: %v %v", k, asm.Err())
	}
	if asm.Format() != FormatOpenAIChat {
		t.Error("format")
	}
}

func TestObjectEditing(t *testing.T) {
	obj, err := parseObject([]byte(` { "a" : 1 ,"b":[1, 2],"a":{"x": "<y>"}, "c":null } `))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := obj.get("a"); !ok || string(v) != `{"x": "<y>"}` {
		t.Errorf("get returns the last duplicate: %s", v)
	}
	obj.set("a", []byte(`"new"`)) // replaces the first, drops the second
	obj.set("d", []byte(`true`))  // appended
	if got := string(obj.bytes()); got != `{"a":"new","b":[1, 2],"c":null,"d":true}` {
		t.Errorf("edited %s", got)
	}
	if obj.del("zzz") || !obj.del("b", "c") {
		t.Error("del reports removals")
	}
	if got := string(obj.bytes()); got != `{"a":"new","d":true}` {
		t.Errorf("after del %s", got)
	}
	if _, ok := obj.get("b"); ok {
		t.Error("b is gone")
	}
	empty, err := parseObject([]byte(`{}`))
	if err != nil || string(empty.bytes()) != `{}` {
		t.Errorf("empty %v", err)
	}
	for _, bad := range []string{`null`, `"s"`, `{"a":1,}`, `{1:2}`, `{"a":1`} {
		if _, err := parseObject([]byte(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
	if !strings.Contains(string(joinArray(nil)), "[]") {
		t.Error("empty array")
	}
}

func TestRetryField(t *testing.T) {
	evs := readAll(t, NewReader(strings.NewReader("retry: 99999999999999999999999\ndata: x\n\nretry:\ndata: y\n\n")))
	if len(evs) != 2 || evs[0].HasRetry || evs[1].HasRetry {
		t.Errorf("an overflowing or empty retry is ignored: %+v", evs)
	}
}

func TestGatedWithoutChoiceZero(t *testing.T) {
	r := &ChatResponse{Choices: []ChatChoice{{Index: 2, Text: "b"}, {Index: 1, Text: "a"}}}
	if c, ok := r.Gated(); !ok || c.Text != "b" {
		t.Errorf("gated %+v", c)
	}
}

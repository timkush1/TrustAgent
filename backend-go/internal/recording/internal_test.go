package recording

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

func TestWindowAndCalibrationState(t *testing.T) {
	for span, want := range map[time.Duration]string{0: "15m", 15 * time.Minute: "15m", time.Hour: "1h", 2 * time.Hour: "24h", 72 * time.Hour: "7d", 40 * 24 * time.Hour: "30d"} {
		if got := window(span); got != want {
			t.Errorf("window(%v) = %s, want %s", span, got, want)
		}
	}
	dec := func(states ...string) Trace {
		evs := map[string]any{}
		for i, s := range states {
			evs[string(rune('a'+i))+"@1"] = map[string]any{"stage": "active", "internal": false, "status": "assessed", "calibration_state": s}
		}
		evs["shadow@1"] = map[string]any{"stage": "shadow", "status": "assessed", "calibration_state": "none"}
		b, _ := json.Marshal(map[string]any{"evaluators": evs})
		return Trace{Detail: store.TraceDetail{Decisions: []json.RawMessage{b}}}
	}
	for _, c := range []struct {
		traces []Trace
		want   string
	}{
		{[]Trace{dec("measured", "provisional"), dec("measured")}, "provisional"},
		{[]Trace{dec("measured")}, "measured"}, // the shadow evaluator is ignored
		{[]Trace{dec("mock", "family")}, "mock"},
		{[]Trace{dec("bogus", "measured")}, "none"},
		{nil, "none"},
	} {
		if got := calibrationState(c.traces); got != c.want {
			t.Errorf("calibrationState = %s, want %s", got, c.want)
		}
	}
}

func TestJudgeHostAndHostOf(t *testing.T) {
	for _, c := range []struct{ provider, base, want string }{
		{"openrouter", "", "openrouter.ai"},
		{"openrouter", "https://openrouter.ai", "openrouter.ai"},
		{"OpenRouter", "http://mock-upstream:8090/", "mock-upstream"},
		{"anthropic", "http://mock-upstream:8090", "api.anthropic.com"},
		{"openai", "", "api.openai.com"},
		{"ollama", "", ""},
		{"mock", "", ""},
		{"none", "", ""},
	} {
		if got := JudgeHost(c.provider, c.base); got != c.want {
			t.Errorf("JudgeHost(%q, %q) = %q, want %q", c.provider, c.base, got, c.want)
		}
	}
	for in, want := range map[string]string{"https://OpenRouter.ai/api": "openrouter.ai", "http://127.0.0.1:18090": "127.0.0.1", "": "", "::bad": ""} {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

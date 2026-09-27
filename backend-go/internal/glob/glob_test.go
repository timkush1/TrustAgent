package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "", true},
		{"", "a", false},
		{"*", "", true},
		{"*", "/v1/chat/completions", true},
		{"/v1/*", "/v1/chat/completions", true},
		{"/v1/*/completions", "/v1/chat/completions", true},
		{"/v1/chat/completions", "/v1/chat/completions", true},
		{"/v1/chat/completions", "/v1/completions", false},
		{"gpt-4o*", "gpt-4o-mini", true},
		{"gpt-4o*", "gpt-4", false},
		{"web_*", "web_search", true},
		{"web_*", "get_order", false},
		{"get_?rder", "get_order", true},
		{"get_?rder", "get_rder", false},
		{"*_order", "get_order", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"**", "anything", true},
		{"?", "é", true},
		{"caf?", "café", true},
		{"*é", "café", true},
		{"[a]", "a", false}, // no character classes: brackets are literal
		{"[a]", "[a]", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestAny(t *testing.T) {
	if !Any([]string{"x", "web_*"}, "web_fetch") {
		t.Error("Any should match the second pattern")
	}
	if Any(nil, "web_fetch") {
		t.Error("Any with no patterns should not match")
	}
}

package decision

import (
	"math"
	"strings"
	"testing"
)

func TestEst(t *testing.T) {
	ideograph := string(rune(0x9000))
	cases := []struct {
		name string
		v    any
		cpt  float64
		want TokenCount
	}{
		{"quotes count", "", 4, TokenCount{Narrow: 2, Est: 1}},
		{"ascii", "Hello, world", 4, TokenCount{Narrow: 14, Est: 4}},
		{"wide", strings.Repeat(ideograph, 3), 4, TokenCount{Narrow: 2, Wide: 3, Est: 4}},
		{"U+2FFF is narrow", string(rune(0x2fff)), 4, TokenCount{Narrow: 3, Est: 1}},
		{"U+3000 is wide", string(rune(0x3000)), 4, TokenCount{Narrow: 2, Wide: 1, Est: 2}},
		{"object", map[string]any{"a": 1.0}, 2.5, TokenCount{Narrow: 7, Est: 3}},
	}
	for _, c := range cases {
		got, err := CountTokens(c.v, c.cpt)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: CountTokens = %+v, want %+v", c.name, got, c.want)
		}
		if e, _ := Est(c.v, c.cpt); e != c.want.Est {
			t.Errorf("%s: Est = %d, want %d", c.name, e, c.want.Est)
		}
	}
	for _, cpt := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := Est("x", cpt); err == nil {
			t.Errorf("Est with cpt %v succeeded, want an error", cpt)
		}
	}
	if _, err := Est(math.NaN(), 4); err == nil {
		t.Error("Est(NaN) succeeded, want an error")
	}
}

func TestUpdateCPT(t *testing.T) {
	cases := []struct {
		name               string
		cpt                float64
		chars, inputTokens int
		want               float64
	}{
		{"moves toward the observation", 4, 5000, 275 + 1000, 0.8*4 + 0.2*5},
		{"clamped to the maximum", 6, 100000, 275 + 1000, MaxCPT},
		{"clamped to the minimum", 2.5, 1000, 275 + 1000, MinCPT},
		{"no tokens beyond the overhead", 4, 5000, 275, 4},
		{"nothing sent", 4, 0, 1000, 4},
	}
	for _, c := range cases {
		if got := UpdateCPT(c.cpt, c.chars, c.inputTokens); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: UpdateCPT = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestR2(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.06, "0.06"}, {0.98, "0.98"}, {0.125, "0.13"}, {0.994999, "0.99"}, {0.995, "1.00"}, {0, "0.00"}, {1, "1.00"},
	}
	for _, c := range cases {
		if got := FormatRisk(c.in); got != c.want {
			t.Errorf("FormatRisk(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

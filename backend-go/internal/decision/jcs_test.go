package decision

import (
	"encoding/json"
	"math"
	"testing"
)

// backslash is built from its code point so the escapes below are spelled out explicitly.
var backslash = string(rune(92))

func TestCanonicalizeNumbers(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{1, "1"},
		{-1.5, "-1.5"},
		{0.1, "0.1"},
		{1e-6, "0.000001"},
		{1e-7, "1e-7"},
		{1.5e-7, "1.5e-7"},
		{1e20, "100000000000000000000"},
		{1e21, "1e+21"},
		{-2.5e300, "-2.5e+300"},
		{5e-324, "5e-324"},
		{0.30000000000000004, "0.30000000000000004"},
		{123456789012345680000, "123456789012345680000"},
	}
	for _, c := range cases {
		got, err := Canonicalize(c.in)
		if err != nil {
			t.Fatalf("%v: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Errorf("Canonicalize(%v) = %s, want %s", c.in, got, c.want)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := Canonicalize(bad); err == nil {
			t.Errorf("Canonicalize(%v) succeeded, want an error", bad)
		}
	}
}

func TestCanonicalizeStrings(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"html is literal", "<a>&</a>", `"<a>&</a>"`},
		{"quote and backslash", `a"b` + backslash + "c/", `"a` + backslash + `"b` + backslash + backslash + `c/"`},
		{"short escapes", "\b\f\n\r\t", `"` + backslash + "b" + backslash + "f" + backslash + "n" + backslash + "r" + backslash + `t"`},
		{"other controls", string(rune(0)) + string(rune(0x1f)) + string(rune(0x7f)), `"` + backslash + "u0000" + backslash + "u001f" + string(rune(0x7f)) + `"`},
		{"separators", "a" + string(rune(0x2028)) + "b" + string(rune(0x2029)), `"a` + backslash + "u2028b" + backslash + `u2029"`},
		{"astral literal", string(rune(0x1f600)), `"` + string(rune(0x1f600)) + `"`},
		{"invalid utf-8", "x\xffy", `"x` + string(rune(0xfffd)) + `y"`},
	}
	for _, c := range cases {
		got, err := Canonicalize(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s: Canonicalize(%q) = %s, want %s", c.name, c.in, got, c.want)
		}
	}
}

func TestCanonicalizeObjectsSortByUTF16(t *testing.T) {
	fullwidthA := string(rune(0xff21))
	emoji := string(rune(0x1f600))
	in := map[string]any{fullwidthA: 1.0, emoji: 2.0, "b": []any{true, nil, "x"}, "a": map[string]any{"z": 1.0, "y": 2.0}}
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatal(err)
	}
	// UTF-16: "a" < "b" < U+D83D (emoji) < U+FF21; UTF-8 byte order would swap the last two.
	want := `{"a":{"y":2,"z":1},"b":[true,null,"x"],"` + emoji + `":2,"` + fullwidthA + `":1}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	if CompareUTF16(emoji, fullwidthA) >= 0 || CompareUTF16("b", "a") <= 0 || CompareUTF16("a", "a") != 0 {
		t.Error("CompareUTF16 does not order by UTF-16 code units")
	}
}

func TestCanonicalizeTypedValues(t *testing.T) {
	type item struct {
		Text string `json:"text"`
		ID   string `json:"id"`
		N    int    `json:"n"`
	}
	got, err := Canonicalize(map[string]any{"items": []item{{Text: "<t>", ID: "a", N: 2}}, "raw": json.RawMessage(`{"b": 1.0, "a": [ ]}`), "num": json.Number("1.50")})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":[{"id":"a","n":2,"text":"<t>"}],"num":1.5,"raw":{"a":[],"b":1}}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	var nilItem *item
	if got, err := Canonicalize(nilItem); err != nil || string(got) != "null" {
		t.Errorf("nil pointer: %s, %v", got, err)
	}
}

func TestCanonicalizeJSONAndSHA256(t *testing.T) {
	got, err := CanonicalizeJSON([]byte(` { "b" : -0 , "a" : 1.0 } `))
	if err != nil || string(got) != `{"a":1,"b":0}` {
		t.Errorf("CanonicalizeJSON = %s, %v", got, err)
	}
	if _, err := CanonicalizeJSON([]byte(`{`)); err == nil {
		t.Error("CanonicalizeJSON accepted invalid JSON")
	}
	// sha256("[]"), the question-set hash of a compile that asks nothing.
	sum, err := SHA256Hex([]any{})
	if err != nil || sum != "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945" {
		t.Errorf("SHA256Hex([]) = %s, %v", sum, err)
	}
}

package jev

import (
	"net/url"
	"testing"
)

// The credential binding compares canonical origins (P4-PLAN D78).
func TestCanonicalOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://OpenRouter.ai/api/v1/decisions": "https://openrouter.ai",
		"https://openrouter.ai:443/api":          "https://openrouter.ai",
		"http://localhost:80/x":                  "http://localhost",
		"http://mock-upstream:8090/api":          "http://mock-upstream:8090",
		"HTTPS://gw.example.com:8443":            "https://gw.example.com:8443",
		"http://[::1]:8080/v1":                   "http://[::1]:8080",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := CanonicalOrigin(u); got != want {
			t.Errorf("CanonicalOrigin(%s) = %s, want %s", raw, got, want)
		}
	}
}

func TestEstimateUSD(t *testing.T) {
	if _, ok := EstimateUSD(Info{Backend: BackendMock, Model: "typesafe/jev-1.13"}, 1000); ok {
		t.Error("the mock backend has no paid estimate")
	}
	if _, ok := EstimateUSD(Info{Backend: BackendOff}, 1000); ok {
		t.Error("the off backend has no paid estimate")
	}
	info := Info{Backend: BackendOpenRouterDecisions, Model: "typesafe/jev-1.13"}
	got, ok := EstimateUSD(info, 1_000_000)
	want := priceFor(0, info.Backend, info.Model).perMTok
	if !ok || got != want {
		t.Errorf("EstimateUSD = %v, %v; want %v (one million tokens at the table price)", got, ok, want)
	}
}

// Config.Prices replaces the embedded table for computed costs; the env override still wins.
func TestPriceOf(t *testing.T) {
	prices := func(backend, model string) (float64, string, bool) {
		if model == "typesafe/jev-1.13" {
			return 0.5, "override", true
		}
		return 0, "", false
	}
	c := &httpClient{info: Info{Backend: BackendOpenRouterDecisions}, prices: prices}
	if p := c.priceOf("typesafe/jev-1.13"); p.perMTok != 0.5 || p.version != "override" {
		t.Errorf("priceOf with Prices = %+v", p)
	}
	if p := c.priceOf("unknown/model"); p.perMTok != DefaultPricePerMTok || p.version != "default" {
		t.Errorf("priceOf without a match = %+v", p)
	}
	c.cfg.PricePerMTok = 2
	if p := c.priceOf("typesafe/jev-1.13"); p.perMTok != 2 || p.version != "override" {
		t.Errorf("TRUTHTABLE_JEV_PRICE_PER_MTOK must win: %+v", p)
	}
	c.cfg.PricePerMTok, c.prices = 0, nil
	if p, want := c.priceOf("typesafe/jev-1.13"), priceFor(0, BackendOpenRouterDecisions, "typesafe/jev-1.13"); p != want {
		t.Errorf("nil Prices = %+v, want the embedded table %+v", p, want)
	}
}

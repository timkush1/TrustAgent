package studio

import "testing"

func TestSpend(t *testing.T) {
	s := NewSpend(1)
	if !s.Reserve(0.4) || !s.Reserve(0.5) {
		t.Fatal("reservations within the cap refused")
	}
	if s.Reserve(0.2) || !s.Stopped() {
		t.Fatal("a reservation over the cap was accepted")
	}
	if s.Reserve(0.01) {
		t.Fatal("the budget accepted a reservation after it was exhausted")
	}
	s.Settle(0.4, 0.3)
	s.Settle(0.5, 0.25)
	if got := s.Total(); got != 0.55 {
		t.Errorf("total = %v", got)
	}
	if s.Cap() != 1 {
		t.Errorf("cap = %v", s.Cap())
	}
	unlimited := NewSpend(0)
	for range 100 {
		if !unlimited.Reserve(1e6) {
			t.Fatal("an uncapped budget refused a reservation")
		}
	}
	var none *Spend
	if !none.Reserve(1e9) || none.Stopped() || none.Total() != 0 || none.Cap() != 0 {
		t.Fatal("a nil Spend is not unlimited")
	}
	none.Settle(1, 1)
}

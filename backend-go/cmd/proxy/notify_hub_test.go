package main

import (
	"slices"
	"sync"
	"testing"
)

func TestNotifyHubFansOutByPrefix(t *testing.T) {
	h := newNotifyHub()
	var mu sync.Mutex
	var got []string
	record := func(name string) func(string) {
		return func(p string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, name+"<"+p)
		}
	}
	h.Subscribe("evaluator", record("eval"))
	cancelSettings := h.Subscribe("settings:", record("settings"))
	h.Subscribe("", record("all"))

	h.Dispatch("evaluator_versions:groundedness@2")
	h.Dispatch("settings:judge")
	h.Dispatch("calibrations:7")
	cancelSettings()
	h.Dispatch("settings:budgets")

	want := []string{
		"eval<evaluator_versions:groundedness@2", "all<evaluator_versions:groundedness@2",
		"settings<settings:judge", "all<settings:judge",
		"all<calibrations:7",
		"all<settings:budgets",
	}
	if !slices.Equal(got, want) {
		t.Errorf("dispatched\n%v\nwant\n%v", got, want)
	}

	var nilHub *notifyHub
	nilHub.Dispatch("settings:x") // a nil hub drops payloads
}

// A subscriber may subscribe or cancel from inside its callback without deadlocking.
func TestNotifyHubCallbackMayResubscribe(t *testing.T) {
	h := newNotifyHub()
	n := 0
	var cancel func()
	cancel = h.Subscribe("x", func(string) {
		n++
		cancel()
		h.Subscribe("y", func(string) { n += 10 })
	})
	h.Dispatch("x1")
	h.Dispatch("x2")
	h.Dispatch("y1")
	if n != 11 {
		t.Errorf("n = %d, want 11", n)
	}
}

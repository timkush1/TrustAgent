package main

import (
	"slices"
	"strings"
	"sync"
)

// notifyHub fans the config NOTIFY payloads out to their subscribers (D84). There is one
// LISTEN connection: the policy engine's listener (policy/engine.go), whose OnNotify calls
// Dispatch with every payload that is not a policy version. Subscribers register a payload prefix:
// "evaluator" (the evaluator registry reload), "settings:" and "credentials:" (the runtime
// configuration), "calibrations:" (the calibration store).
type notifyHub struct {
	mu   sync.RWMutex
	next int
	subs map[int]notifySub
}

type notifySub struct {
	prefix string
	fn     func(payload string)
}

func newNotifyHub() *notifyHub { return &notifyHub{subs: map[int]notifySub{}} }

// Subscribe calls fn with every dispatched payload that starts with prefix ("" matches all) and
// returns the function that removes the subscription. fn runs on the listener goroutine, so it
// must not block: hand the work to a loop of its own.
func (h *notifyHub) Subscribe(prefix string, fn func(payload string)) (cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.next
	h.next++
	h.subs[id] = notifySub{prefix: prefix, fn: fn}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, id)
	}
}

// Dispatch calls the subscribers whose prefix payload starts with, in subscription order. A nil
// hub drops the payload.
func (h *notifyHub) Dispatch(payload string) {
	if h == nil {
		return
	}
	h.mu.RLock()
	ids := make([]int, 0, len(h.subs))
	for id, s := range h.subs {
		if strings.HasPrefix(payload, s.prefix) {
			ids = append(ids, id)
		}
	}
	fns := make([]func(string), 0, len(ids))
	slices.Sort(ids)
	for _, id := range ids {
		fns = append(fns, h.subs[id].fn)
	}
	h.mu.RUnlock()
	for _, fn := range fns {
		fn(payload)
	}
}

package main

import (
	"context"
	"sync"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/websocket"
)

// publishEngineStatus logs an engine health transition and broadcasts it (WS status event).
func (s *server) publishEngineStatus(h grpc.EngineHealth) {
	if h.Reachable() {
		s.log.Info("audit engine health", "state", h.State, "detail", h.Detail)
	} else {
		s.log.Warn("audit engine health", "state", h.State, "detail", h.Detail)
	}
	ev, err := websocket.NewEvent("status", "", engineStatusEvent(h), nil)
	if err != nil {
		return
	}
	s.hub.Publish(ev)
}

// startStatusWatch starts the loop on wg that broadcasts decision-model status transitions
// (breaker, budget) until ctx ends (API-9). None yet.
func (s *server) startStatusWatch(ctx context.Context, wg *sync.WaitGroup) {}

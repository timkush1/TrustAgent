package main

import (
	"context"
	"log/slog"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
)

// wrapTraceWriter puts the batched trace writer in front of st when TRUTHTABLE_TRACE_FLUSH is
// async (P4-PLAN D66, GGW) and returns the store to use and the flush to call at shutdown, after
// the HTTP drain. Step-0 stub: writes stay inline, the store is returned as it is, and flush has
// nothing to do.
func wrapTraceWriter(cfg *config.Config, st store.Store, log *slog.Logger) (store.Store, func(context.Context) error) {
	if cfg.TraceFlush != "sync" && st != nil {
		log.Info("trace writes run inline; the batched TRUTHTABLE_TRACE_FLUSH=async writer lands in P4")
	}
	return st, func(context.Context) error { return nil }
}

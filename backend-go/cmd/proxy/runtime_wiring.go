package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/status"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
)

// runtimeBootReload bounds the first read of the settings store in newServer.
const runtimeBootReload = 10 * time.Second

// runtimeParts are the runtime configuration's components (P4, GSEC): the Manager every consumer
// subscribes to, the settings store behind it, and the NOTIFY channel its writes use.
type runtimeParts struct {
	manager *runtimecfg.Manager
	// store holds the settings and credential rows; nil without a database (every write then
	// answers 503 persistence_disabled, D31).
	store *store.PostgresStore
	// channel is the NOTIFY channel of settings and credential writes ("" = store.ConfigChannel,
	// the channel the policy engine's listener hears and dispatches through s.notify, D84).
	channel string
}

// initRuntime builds the runtime configuration (D31-D33): a Manager over the env and, with a
// database, the stored settings and credentials, read once now so the gateway starts with them.
// Writers call s.rt.manager.Reload after a write; other replicas hear the write's NOTIFY through
// the hub ("settings:" and "credentials:", D84), and the poll in startRuntimeLoops backs both up.
func (s *server) initRuntime(d serverDeps) {
	opts := runtimecfg.Options{Config: d.cfg, Master: d.master, Redactor: d.redactor, Log: s.log}
	if d.pg != nil {
		opts.Store = d.pg
	}
	m := runtimecfg.New(opts)
	s.rt = &runtimeParts{manager: m, store: d.pg}
	// /api/status reads the credential store's state, the decision credential and the runtime
	// warnings from the snapshot in effect (D32, D77, D79).
	s.status.runtime = m.Current
	s.status.credentials = func() statusCredentials {
		snap := m.Current()
		return statusCredentials{Store: snap.StoreState, Undecryptable: append([]string{}, snap.Undecryptable...)}
	}
	if d.pg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeBootReload)
	defer cancel()
	_ = m.Reload(ctx) // a failure is logged once and shows as the settings_unreadable warning
	for _, prefix := range []string{"settings:", "credentials:"} {
		s.notify.Subscribe(prefix, func(string) { m.Kick() })
	}
}

// mountSecurityAPI mounts the settings endpoints (SPEC §2.4 rows 52 and 53, settings_api.go).
func (s *server) mountSecurityAPI(api *gin.RouterGroup, g apiGuards) {
	s.mountSettings(api, g)
}

// processRedactor is the Redactor every REST error message passes through (apiRedact, D42):
// the process Redactor, which learns each secret the runtime configuration decrypts or receives.
// nil leaves only the built-in credential patterns.
var processRedactor atomic.Pointer[config.Redactor]

// patternRedactor masks the credential patterns while no process Redactor is installed.
var patternRedactor = config.NewRedactor()

func init() {
	apiRedact = func(text string) string {
		if r := processRedactor.Load(); r != nil {
			return r.Redact(text)
		}
		return patternRedactor.Redact(text)
	}
}

// installRedaction makes REST error messages (abortAPI, field errors) pass through r (D42) and
// returns the function that restores the previous Redactor. main installs the process Redactor
// once at startup.
func installRedaction(r *config.Redactor) (restore func()) {
	prev := processRedactor.Swap(r)
	return func() { processRedactor.Store(prev) }
}

// redactErr is the text of err as it may leave the process (D42): a gRPC status error gives its
// status message (without the "rpc error: code = ..." prefix), and the text passes through the
// redactor, so a secret an engine or a vendor echoes never reaches a client or the WS stream.
func (s *server) redactErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if st, ok := status.FromError(err); ok {
		msg = st.Message()
	}
	return apiRedact(s.redact(msg))
}

// startRuntimeLoops runs the runtime configuration's reload loop until ctx ends: a reload on
// every hub notification and every TRUTHTABLE_POLICY_RELOAD_INTERVAL (D84). Without a database
// there is nothing to reload.
func (s *server) startRuntimeLoops(ctx context.Context, wg *sync.WaitGroup) {
	if s.rt == nil || s.rt.store == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.rt.manager.Run(ctx, s.cfg.PolicyReloadInterval)
	}()
}

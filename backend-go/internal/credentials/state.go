package credentials

import "github.com/truthtable/backend-go/internal/authz"

// States of the credential store (api.ts SystemStatus.credentials.store, D32).
const (
	// StateReady: a database and a durable master key (env, file or generated); dashboard
	// credentials can be stored and read.
	StateReady = "ready"
	// StateUnavailable: a database, but only an ephemeral master key. Nothing is ever sealed with
	// an ephemeral key (it dies with the process); stored rows cannot be opened, and writes answer
	// 503 credentials_unavailable. Env credentials keep working.
	StateUnavailable = "unavailable"
	// StateNoDatabase: no database, so no dashboard credentials. Env credentials keep working.
	StateNoDatabase = "no_database"
)

// StoreState is the credential store's state for a gateway with or without a database and with
// master key m (nil counts as ephemeral, as serverDeps.master does).
func StoreState(hasDatabase bool, m *authz.MasterKey) string {
	switch {
	case !hasDatabase:
		return StateNoDatabase
	case !Durable(m):
		return StateUnavailable
	}
	return StateReady
}

// Durable reports a master key that outlives the process (env, file or generated), the only kind
// the store seals with.
func Durable(m *authz.MasterKey) bool {
	if m == nil {
		return false
	}
	switch m.Source {
	case authz.MasterFromEnv, authz.MasterFromFile, authz.MasterGenerated:
		return true
	}
	return false
}

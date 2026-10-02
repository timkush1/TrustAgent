package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/store"
)

// Store is what a database-backed Manager reads and writes (store.PostgresStore).
type Store interface {
	// RuntimeState reads the version, the epoch, the runtime settings rows and every credential
	// row in one consistent snapshot.
	RuntimeState(ctx context.Context) (*store.SettingsRows, []store.CredentialRecord, error)
	// TouchCredential records a dashboard credential's use (last_used_at).
	TouchCredential(ctx context.Context, name string, at time.Time) error
}

// Options configure a Manager.
type Options struct {
	// Config is the loaded configuration: the env seed, env credentials and env precedence.
	Config *config.Config
	// Store is the settings store; nil means no database (D31: version 0, a random epoch, only
	// env credentials).
	Store Store
	// Master opens stored credentials; nil or ephemeral leaves the store unavailable (D32).
	Master *authz.MasterKey
	// Redactor learns every secret the Manager loads, before any consumer can send it (D42).
	Redactor *config.Redactor
	Log      *slog.Logger
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

// touchEvery throttles last_used_at writes per credential (authz.go does the same for api keys).
const touchEvery = time.Minute

// loadedState is what the last successful store read produced.
type loadedState struct {
	version int64
	epoch   string
	rows    map[string]json.RawMessage
	// dashboard holds the opened dashboard credentials (shadowed rows excluded), undecryptable the
	// rows that did not open, stored every row's name.
	dashboard     CredentialSet
	undecryptable []string
	stored        []string
}

// New builds a Manager. Its first snapshot comes from the env and the built-in defaults; with a
// Store, call Reload once at boot (and Run for the reload loop) to read the dashboard settings.
func New(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Manager{
		opts:       opts,
		fp:         credentials.NewFingerprinter(opts.Master),
		localEpoch: newEpoch(),
		subs:       map[int]func(*Snapshot){},
		kick:       make(chan struct{}, 1),
		touched:    map[string]time.Time{},
	}
	if opts.Store != nil {
		if c, err := credentials.New(opts.Master); err == nil {
			m.cipher = c
		}
	}
	m.cur = m.build()
	m.addSecrets(m.cur)
	return m
}

// StoreState is the credential store's state (D32).
func (m *Manager) StoreState() string {
	return credentials.StoreState(m.opts.Store != nil, m.opts.Master)
}

// Cipher seals new dashboard credentials. It fails with credentials.ErrUnavailable unless the
// store is ready (D32: never seal with an ephemeral key; no database, nowhere to store).
func (m *Manager) Cipher() (*credentials.Cipher, error) {
	if m.cipher == nil {
		return nil, credentials.ErrUnavailable
	}
	return m.cipher, nil
}

// Fingerprint returns the D30 fingerprint of secret under this instance's master key ("" without
// one). Env credentials and received keys use it.
func (m *Manager) Fingerprint(secret string) string { return m.fp.Of(secret) }

// Reload reads the settings store and publishes the resulting snapshot when it differs from the
// current one. It runs at boot, after each local write (so the writer's next read sees it), on
// every settings: and credentials: notification and on the poll (D84). A failed read keeps the
// last good state in effect, adds the settings_unreadable warning and returns the error. Without
// a store it only republishes the env snapshot.
func (m *Manager) Reload(ctx context.Context) error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if m.opts.Store == nil {
		m.publishLocked(m.build())
		return nil
	}
	rows, creds, err := m.opts.Store.RuntimeState(ctx)
	if err != nil {
		msg := "the settings store could not be read"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			msg = "reading the settings store timed out"
		}
		if m.loadErr == "" {
			m.opts.Log.Warn("runtime settings: "+msg+"; the last settings stay in effect", "err", err)
		}
		m.loadErr = msg
		m.publishLocked(m.build())
		return fmt.Errorf("runtimecfg: reload: %w", err)
	}
	if m.loadErr != "" {
		m.opts.Log.Info("runtime settings: the settings store is readable again")
	}
	m.loadErr = ""
	m.loaded = m.open(rows, creds)
	m.publishLocked(m.build())
	return nil
}

// Kick asks the reload loop (Run) for a reload without waiting for it; requests made while one
// is pending coalesce. It never blocks, so a notification callback may call it.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run reloads on every Kick and every interval (0: kicks only) until ctx ends, then waits for
// the background last_used_at writes. Each reload is bounded by a timeout.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	defer m.Wait()
	if m.opts.Store == nil {
		<-ctx.Done()
		return
	}
	var tick <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		case <-tick:
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = m.Reload(rctx) // logged once per outage inside Reload
		cancel()
	}
}

// Wait blocks until the background last_used_at writes finish (shutdown and tests).
func (m *Manager) Wait() { m.touchWG.Wait() }

// open decrypts the stored rows. A row that an env credential of the same name shadows is not
// opened (env wins, D33); a row that does not open is listed as undecryptable (D32).
func (m *Manager) open(rows *store.SettingsRows, creds []store.CredentialRecord) *loadedState {
	ls := &loadedState{version: rows.Version, epoch: rows.Epoch, rows: rows.Rows, dashboard: CredentialSet{},
		undecryptable: []string{}, stored: []string{}}
	env := m.envCredentials()
	orBase := ""
	if m.opts.Config != nil {
		orBase = m.opts.Config.Jev.OpenRouterBaseURL
	}
	for _, rec := range creds {
		ls.stored = append(ls.stored, rec.Name)
		if _, shadowed := env[rec.Name]; shadowed {
			continue
		}
		spec, known := credentials.Lookup(rec.Name)
		if !known || m.cipher == nil {
			ls.undecryptable = append(ls.undecryptable, rec.Name)
			continue
		}
		secret, err := m.cipher.Open(credentials.Sealed{Name: rec.Name, Provider: rec.Provider, Ciphertext: rec.Ciphertext,
			Nonce: rec.Nonce, KeyID: rec.KeyID}, rec.AllowedHosts)
		if err != nil {
			ls.undecryptable = append(ls.undecryptable, rec.Name)
			continue
		}
		// Vendor origins are recomputed at every load; the stored column is trusted (and bound
		// by the AAD) only for compatible and upstream-* (D78).
		origins := slices.Clone(rec.AllowedHosts)
		if !spec.CustomOrigin {
			origins = credentials.VendorOrigins(rec.Name, orBase)
		}
		ls.dashboard[rec.Name] = Credential{Name: rec.Name, Provider: rec.Provider, Source: credentials.SourceDashboard,
			Secret: secret, Masked: rec.Masked, Fingerprint: rec.Fingerprint, AllowedOrigins: origins}
	}
	var before []string
	if m.loaded != nil {
		before = m.loaded.undecryptable
	}
	if len(ls.undecryptable) > 0 && m.cipher != nil && !slices.Equal(before, ls.undecryptable) {
		m.opts.Log.Warn("stored credentials cannot be decrypted with the current master key; paste them again or restore the previous TRUTHTABLE_SECRET_KEY",
			"names", strings.Join(ls.undecryptable, ","))
	}
	return ls
}

// build assembles a snapshot from the configuration, the last store read and the engine's env.
func (m *Manager) build() *Snapshot {
	cfg := m.opts.Config
	m.mu.RLock()
	engineEnv := slices.Clone(m.engineEnv)
	m.mu.RUnlock()

	snap := &Snapshot{Epoch: m.localEpoch, StoreState: m.StoreState(), EngineEnv: engineEnv,
		Credentials: CredentialSet{}, Undecryptable: []string{}, Warnings: []Warning{}}
	var rows map[string]json.RawMessage
	if ls := m.loaded; ls != nil {
		snap.Version, snap.Epoch, rows = ls.version, ls.epoch, ls.rows
		for name, c := range ls.dashboard {
			snap.Credentials[name] = c
		}
		snap.Undecryptable = slices.Clone(ls.undecryptable)
	}
	for name, c := range m.envCredentials() {
		snap.Credentials[name] = c
	}
	persisted, badRows := overlayRows(seedSettings(cfg), rows)
	snap.Persisted = persisted
	snap.Settings, snap.EnvManaged, snap.EnvSources = applyEnv(cfg, persisted, engineEnv)
	sort.Strings(snap.Undecryptable)

	switch {
	case snap.StoreState == credentials.StateUnavailable:
		w := Warning{Code: WarnStoreUnavailable,
			Message: "Dashboard keys can't be stored or read: no durable master key. Set TRUTHTABLE_SECRET_KEY (or TRUTHTABLE_SECRET_KEY_FILE) and restart."}
		if len(snap.Undecryptable) > 0 {
			w.Details = map[string]any{"names": slices.Clone(snap.Undecryptable)}
		}
		snap.Warnings = append(snap.Warnings, w)
	case len(snap.Undecryptable) > 0:
		snap.Warnings = append(snap.Warnings, Warning{Code: WarnMasterKeyChanged,
			Message: "The master key changed, so these stored keys can't be read: " + strings.Join(snap.Undecryptable, ", ") +
				". Paste them again, or restore the previous TRUTHTABLE_SECRET_KEY.",
			Details: map[string]any{"names": slices.Clone(snap.Undecryptable)}})
	}
	if m.loadErr != "" {
		snap.Warnings = append(snap.Warnings, Warning{Code: WarnSettingsUnreadable,
			Message: "Dashboard settings: " + m.loadErr + "; the last settings that were read stay in effect."})
	}
	if len(badRows) > 0 {
		m.opts.Log.Warn("runtime settings: stored rows that are not valid were ignored", "keys", strings.Join(badRows, ","))
	}
	return snap
}

// publishLocked makes snap current and fans it out when it differs from the current snapshot.
// The caller holds reloadMu, which keeps publications in order.
func (m *Manager) publishLocked(snap *Snapshot) {
	m.addSecrets(snap)
	m.mu.Lock()
	if reflect.DeepEqual(m.cur, snap) {
		m.mu.Unlock()
		return
	}
	m.cur = snap
	ids := make([]int, 0, len(m.subs))
	for id := range m.subs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	fns := make([]func(*Snapshot), 0, len(ids))
	for _, id := range ids {
		fns = append(fns, m.subs[id])
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn(snap)
	}
}

// addSecrets registers every secret of snap with the Redactor (D42); the Redactor ignores
// secrets it already holds.
func (m *Manager) addSecrets(snap *Snapshot) {
	if m.opts.Redactor == nil {
		return
	}
	for _, c := range snap.Credentials {
		m.opts.Redactor.Add(c.Secret)
	}
}

// touch records a dashboard credential's use at most once per touchEvery, in the background.
func (m *Manager) touch(name string) {
	if m.opts.Store == nil {
		return
	}
	now := m.opts.Now()
	m.touchMu.Lock()
	if last, ok := m.touched[name]; ok && now.Sub(last) < touchEvery {
		m.touchMu.Unlock()
		return
	}
	m.touched[name] = now
	m.touchMu.Unlock()
	m.touchWG.Add(1)
	go func() {
		defer m.touchWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.opts.Store.TouchCredential(ctx, name, now); err != nil {
			m.opts.Log.Debug("runtime settings: last_used_at not updated", "credential", name, "err", err)
		}
	}()
}

// overlayRows unmarshals each stored row over base (a row holds any subset of its fields) and
// returns the result and the keys whose rows are not valid for their field (those are ignored).
func overlayRows(base RuntimeSettings, rows map[string]json.RawMessage) (RuntimeSettings, []string) {
	out := base
	out.PricingOverrides = slices.Clone(base.PricingOverrides)
	var bad []string
	for _, k := range store.RuntimeSettingKeys {
		raw, ok := rows[k]
		if !ok {
			continue
		}
		var err error
		switch k {
		case "decision_model":
			err = overlay(raw, &out.DecisionModel)
		case "judge":
			err = overlay(raw, &out.Judge)
		case "upstream":
			err = overlay(raw, &out.Upstream)
		case "bindings":
			err = overlay(raw, &out.Bindings)
		case "budgets":
			err = overlay(raw, &out.Budgets)
		case "pricing_overrides":
			err = overlay(raw, &out.PricingOverrides)
		}
		if err != nil {
			bad = append(bad, k)
		}
	}
	if out.PricingOverrides == nil {
		out.PricingOverrides = base.PricingOverrides[:0:0]
	}
	return out, bad
}

// overlay unmarshals raw over *dst, leaving dst unchanged when raw does not fit.
func overlay[T any](raw json.RawMessage, dst *T) error {
	cp := *dst
	if err := json.Unmarshal(raw, &cp); err != nil {
		return err
	}
	*dst = cp
	return nil
}

// ChangedRows returns the settings rows (top-level RuntimeSettings fields, marshalled) whose value
// differs between before and after: what a write that turned before into after stores. Pass
// Snapshot.Persisted as before, so env-managed values are never written into a row.
func ChangedRows(before, after RuntimeSettings) (map[string]json.RawMessage, error) {
	pairs := []struct {
		key  string
		a, b any
	}{
		{"decision_model", before.DecisionModel, after.DecisionModel},
		{"judge", before.Judge, after.Judge},
		{"upstream", before.Upstream, after.Upstream},
		{"bindings", before.Bindings, after.Bindings},
		{"budgets", before.Budgets, after.Budgets},
		{"pricing_overrides", before.PricingOverrides, after.PricingOverrides},
	}
	out := map[string]json.RawMessage{}
	for _, p := range pairs {
		a, err := json.Marshal(p.a)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(p.b)
		if err != nil {
			return nil, err
		}
		if string(a) != string(b) {
			out[p.key] = b
		}
	}
	return out, nil
}

// ChangedFields is ChangedRows reduced to the fields that changed: each object row holds only its
// top-level fields whose value differs (a nested object such as upstream.default whole), and the
// pricing_overrides array is whole. It is the settings patch of a credential write
// (store.PutCredential merges object rows), so a concurrent write to a sibling field is kept.
func ChangedFields(before, after RuntimeSettings) (map[string]json.RawMessage, error) {
	rows, err := ChangedRows(before, after)
	if err != nil {
		return nil, err
	}
	prev, err := ChangedRows(after, before)
	if err != nil {
		return nil, err
	}
	for key, raw := range rows {
		var a, b map[string]json.RawMessage
		if json.Unmarshal(raw, &a) != nil || json.Unmarshal(prev[key], &b) != nil {
			continue // not an object: the whole value
		}
		for f, v := range a {
			if string(b[f]) == string(v) {
				delete(a, f)
			}
		}
		out, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		rows[key] = out
	}
	return rows, nil
}

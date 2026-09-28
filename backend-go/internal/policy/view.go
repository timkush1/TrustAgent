package policy

import (
	"encoding/json"
	"fmt"

	"github.com/truthtable/contracts"
)

// StateView is api.ts PolicyState: the active version with its effective routes, the file state
// and whether the caller may edit it.
type StateView struct {
	Version
	ETag            string           `json:"etag"`
	Locked          bool             `json:"locked"`
	Persisted       bool             `json:"persisted"`
	Editable        bool             `json:"editable"`
	EditableReason  *string          `json:"editable_reason"`
	EffectiveRoutes []EffectiveRoute `json:"effective_routes"`
	File            FileState        `json:"file"`
	Warnings        []string         `json:"warnings"`
}

// View renders st for a caller; admin reports whether the caller holds the admin scope.
func (e *Engine) View(st *State, admin bool) StateView {
	v := StateView{
		Version: st.Version, ETag: st.ETag(), Locked: st.Locked, Persisted: st.Persisted,
		EffectiveRoutes: st.Policy.EffectiveRoutes(e.cfg.Env.Specs, e.cfg.Env, e.Runtime()),
		File:            st.File, Warnings: []string{},
	}
	var reason string
	switch {
	case !st.Persisted:
		reason = ReasonFileManaged
	case st.Locked:
		reason = ReasonLocked
	case !admin:
		reason = ReasonAdminRequired
	}
	if reason == "" {
		v.Editable = true
	} else {
		v.EditableReason = &reason
	}
	v.Warnings = append(v.Warnings, st.Warnings...)
	for _, w := range st.Policy.Warnings {
		v.Warnings = append(v.Warnings, w.String())
	}
	if st.File.Status == FileDiverged {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%s changed after the newest dashboard edit and is not applied: adopt the file or keep the dashboard version", st.File.Path))
	}
	return v
}

// StatusView is SystemStatus.policy.
type StatusView struct {
	Version    *int64  `json:"version"`
	Source     string  `json:"source"`
	Locked     bool    `json:"locked"`
	Persisted  bool    `json:"persisted"`
	Editable   bool    `json:"editable"`
	FileStatus string  `json:"file_status"`
	FileError  *string `json:"file_error"`
}

// Status summarizes the engine for GET /api/status (editable ignores the caller's scopes).
func (e *Engine) Status() StatusView {
	st := e.State()
	id := st.Version.ID
	v := StatusView{Version: &id, Source: st.Policy.Source, Locked: st.Locked, Persisted: st.Persisted,
		Editable: st.Persisted && !st.Locked, FileStatus: st.File.Status}
	if len(st.File.Errors) > 0 {
		msg := st.File.Errors[0].String()
		v.FileError = &msg
	}
	return v
}

// Presets returns contracts/policies/presets.v1.json (the policy presets of the dashboard).
func Presets() (json.RawMessage, error) {
	b, err := contracts.FS.ReadFile("policies/presets.v1.json")
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("presets.v1.json is not valid JSON")
	}
	return b, nil
}

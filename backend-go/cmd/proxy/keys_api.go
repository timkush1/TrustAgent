package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/store"
)

// apiKeyView is api.ts ApiKeyView: never the key or its hash.
type apiKeyView struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Scopes     []string `json:"scopes"`
	Prefix     string   `json:"prefix"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// createdAPIKey is the POST /api/keys response: the view plus the key, shown this once.
type createdAPIKey struct {
	apiKeyView
	Secret string `json:"secret"`
}

func rfc3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func keyView(k *store.APIKeyRecord) apiKeyView {
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return apiKeyView{
		ID: k.ID, Label: k.Label, Scopes: scopes, Prefix: k.Prefix, CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339Nano),
		LastUsedAt: rfc3339(k.LastUsedAt), RevokedAt: rfc3339(k.RevokedAt),
	}
}

// keysUnavailable answers when api_keys need a database that is not configured.
func keysUnavailable(c *gin.Context) {
	abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
		"Dashboard API keys need a database (TRUTHTABLE_DATABASE_URL); env keys (TRUTHTABLE_API_KEYS, _READ_KEYS, _ADMIN_KEYS) work without one.")
}

// listKeys serves GET /api/keys (admin): every api key, newest first, revoked ones included.
func (h *authzAPI) listKeys(c *gin.Context) {
	keys, err := h.auth.ListKeys(c.Request.Context())
	switch {
	case errors.Is(err, authz.ErrNoStore):
		keysUnavailable(c)
		return
	case err != nil:
		h.log.Error("api keys not listed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The keys could not be listed.")
		return
	}
	items := make([]apiKeyView, 0, len(keys))
	for i := range keys {
		items = append(items, keyView(&keys[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// createKey serves POST /api/keys (admin) {label, scopes}: 201 with the key, shown once.
func (h *authzAPI) createKey(c *gin.Context) {
	var body struct {
		Label  string   `json:"label"`
		Scopes []string `json:"scopes"`
	}
	if !decodeAuthzBody(c, &body, "{label, scopes}") {
		return
	}
	var fields []fieldError
	if !authz.ValidLabel(body.Label) {
		fields = append(fields, fieldError{Path: "label", Message: "1-64 characters: letters, digits and . _ : @ -, starting with a letter or digit"})
	}
	scopes, err := authz.NormalizeScopes(body.Scopes)
	if err != nil {
		fields = append(fields, fieldError{Path: "scopes", Message: err.Error()})
	}
	if len(fields) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The key is not valid.", fields...)
		return
	}
	actor := callerIdentity(c).Label
	created, err := h.auth.CreateKey(c.Request.Context(), body.Label, scopes, actor)
	switch {
	case errors.Is(err, authz.ErrNoStore):
		keysUnavailable(c)
		return
	case err != nil:
		h.log.Error("api key not created", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The key could not be created.")
		return
	}
	h.log.Info("api key created", "key_id", created.Record.ID, "label", created.Record.Label, "actor", actor)
	noStore(c)
	c.JSON(http.StatusCreated, createdAPIKey{apiKeyView: keyView(&created.Record), Secret: created.Secret})
}

// revokeKey serves DELETE /api/keys/:id (admin): revoke a key; its sessions end with it.
func (h *authzAPI) revokeKey(c *gin.Context) {
	actor := callerIdentity(c).Label
	rec, err := h.auth.RevokeKey(c.Request.Context(), c.Param("id"), actor)
	switch {
	case errors.Is(err, authz.ErrNoStore):
		keysUnavailable(c)
		return
	case errors.Is(err, authz.ErrKeyNotFound):
		abortAPI(c, http.StatusNotFound, codeNotFound, "No key has this id.")
		return
	case errors.Is(err, authz.ErrLastAdminKey):
		abortAPIDetails(c, http.StatusConflict, codeConflict,
			"This is the last admin key; revoking it would lock everyone out. Create another admin key first.",
			map[string]any{"reason": "last_admin_key"})
		return
	case err != nil:
		h.log.Error("api key not revoked", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The key could not be revoked.")
		return
	}
	h.log.Info("api key revoked", "key_id", rec.ID, "label", rec.Label, "actor", actor)
	c.Status(http.StatusNoContent)
}

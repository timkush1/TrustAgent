package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
)

// credentialList is api.ts CredentialList.
type credentialList struct {
	Items []credentialView `json:"items"`
}

// credentialPutRequest is api.ts CredentialPutRequest (#55).
type credentialPutRequest struct {
	Provider string   `json:"provider"`
	Secret   string   `json:"secret"`
	BaseURL  string   `json:"base_url"`
	Verify   *bool    `json:"verify"`
	Bind     []string `json:"bind"`
}

// credentialVerifyRequest is api.ts CredentialVerifyRequest (#57).
type credentialVerifyRequest struct {
	Bind []string `json:"bind"`
}

// maxCredentialSecret bounds a pasted key (the request body limit bounds the rest).
const maxCredentialSecret = 4096

// handleListCredentials serves GET /api/credentials (SPEC §2.4 row 54, admin): every key the
// gateway knows, from the proxy's env, the engine's env (D80) and the dashboard store, each with
// what uses it and whether the master key can open it. Never a secret: masked and fingerprint
// only. Without a database only the env keys show.
func (s *server) handleListCredentials(c *gin.Context) {
	snap := s.rt.manager.Current()
	var rows []store.CredentialRecord
	if s.rt.store != nil {
		var err error
		if rows, err = s.rt.store.ListCredentials(c.Request.Context()); err != nil {
			s.abortCredential(c, "list credentials", err)
			return
		}
	}
	noStore(c)
	c.JSON(http.StatusOK, credentialList{Items: s.credentialViews(snap, rows)})
}

// handlePutCredential serves PUT /api/credentials/:name (SPEC §2.4 row 55, D75): store a pasted
// key, checked with its provider first unless verify is false, and bind it to the fast check
// and/or the judge when the check passes. The answer is CredentialWriteResult, also when the
// check failed (stored:false when the key in use stays).
func (s *server) handlePutCredential(c *gin.Context) {
	name := c.Param("name")
	if _, ok := credentials.Lookup(name); !ok {
		s.abortCredential(c, "put credential", credentialNotFound(name))
		return
	}
	var body credentialPutRequest
	if !bindJSON(c, &body, false) {
		return
	}
	// A key copied from a terminal or a .env line often carries a newline or spaces around it;
	// as in the env loader, they are not part of the key. Inside a key they never are, and a
	// control character would make every header that carries it invalid.
	secret := strings.TrimSpace(body.Secret)
	switch {
	case secret == "":
		s.abortCredential(c, "put credential", credentialInvalid("secret", "secret is required: paste the key."))
		return
	case len(secret) > maxCredentialSecret:
		s.abortCredential(c, "put credential", credentialInvalid("secret", fmt.Sprintf("secret is longer than %d characters; is that the key?", maxCredentialSecret)))
		return
	case !printableSecret(secret):
		s.abortCredential(c, "put credential", credentialInvalid("secret", "secret contains spaces or non-printable characters; paste only the key."))
		return
	case body.Provider == "":
		s.abortCredential(c, "put credential", credentialInvalid("provider", "provider is required."))
		return
	}
	verify := body.Verify == nil || *body.Verify
	res, err := s.writeCredential(c.Request.Context(), CredentialWrite{
		Name: name, Provider: body.Provider, Secret: secret, BaseURL: body.BaseURL,
		Verify: verify, Bind: body.Bind, Actor: actor(c),
	})
	if err != nil {
		s.abortCredential(c, "put credential", err)
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, res)
}

// printableSecret reports a key made of printable ASCII without spaces, which is what every API
// key the gateway sends in a header is.
func printableSecret(secret string) bool {
	for i := 0; i < len(secret); i++ {
		if b := secret[i]; b <= ' ' || b > '~' {
			return false
		}
	}
	return true
}

// handleVerifyCredential serves POST /api/credentials/:name/verify (SPEC §2.4 row 57, D40, D75):
// check the stored or env key with its provider again, and bind it when asked and it passes. An
// env key's outcome is kept in memory (D80); the engine's env keys are checked with make doctor.
// One check per key name every 10 s.
func (s *server) handleVerifyCredential(c *gin.Context) {
	name := c.Param("name")
	if _, ok := credentials.Lookup(name); !ok {
		s.abortCredential(c, "verify credential", credentialNotFound(name))
		return
	}
	var body credentialVerifyRequest
	if !bindJSON(c, &body, true) {
		return
	}
	res, err := s.writeCredential(c.Request.Context(), CredentialWrite{
		Name: name, Verify: true, Bind: body.Bind, Actor: actor(c), Action: "credential.verify",
	})
	if err != nil {
		s.abortCredential(c, "verify credential", err)
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, res)
}

// handleDeleteCredential serves DELETE /api/credentials/:name (SPEC §2.4 row 56, D36, D77): remove
// a dashboard key and, in the same transaction, every binding that names it. Removing the fast
// check's key while an enforcing route fails closed would block every answer there, so that
// needs ?confirm=true (409 confirmation_required names the routes). Env keys are read-only.
func (s *server) handleDeleteCredential(c *gin.Context) {
	name := c.Param("name")
	spec, ok := credentials.Lookup(name)
	if !ok {
		s.abortCredential(c, "delete credential", credentialNotFound(name))
		return
	}
	snap := s.rt.manager.Current()
	if side := credentialEnvSide(snap, spec); side != "" {
		s.abortCredential(c, "delete credential", credentialEnvManaged(spec, side))
		return
	}
	if e := s.credentialStoreRefusal(snap); e != nil {
		s.abortCredential(c, "delete credential", e)
		return
	}
	// Only a key that is serving the fast check turns it off when removed.
	cur, serving := snap.Credentials[name]
	serving = serving && cur.Source == credentials.SourceDashboard && strval(snap.Settings.Bindings.DecisionModel) == name
	if serving && c.Query("confirm") != "true" {
		if routes := s.failClosedRoutes(); len(routes) > 0 {
			abortAPIDetails(c, http.StatusConflict, codeConfirmationRequired,
				fmt.Sprintf("Removing %s turns the fast check off, and these routes fail closed, so they would block every answer. Send ?confirm=true to remove it anyway.", name),
				map[string]any{"routes": routes})
			return
		}
	}
	ctx := c.Request.Context()
	res, err := s.rt.store.DeleteCredential(ctx, s.rt.channel, name, actor(c))
	if errors.Is(err, store.ErrNotFound) {
		s.abortCredential(c, "delete credential", credentialNotFound(name))
		return
	}
	if err != nil {
		s.abortCredential(c, "delete credential", err)
		return
	}
	s.reloadAfterCredentialWrite(ctx)
	cs := s.apply.creds
	cs.publishCredentialsChanged(name, "delete", res.Record.Fingerprint)
	if len(res.Unbound) > 0 {
		cs.publishSettingsUpdated(res.Version, res.Unbound)
	}
	c.Status(http.StatusNoContent)
}

// failClosedRoutes lists the enforcing fail-closed routes of the policy in effect (D77): mode
// block or fallback with fail: closed, in policy order with the default route last. Without a
// fast check, each of them blocks every answer.
func (s *server) failClosedRoutes() []string {
	if s.policies == nil || s.policies.Engine == nil {
		return nil
	}
	p := s.policies.Engine.Current()
	if p == nil {
		return nil
	}
	var out []string
	for _, r := range p.AllRoutes() {
		if r != nil && r.Fail == decision.FailClosed && (r.Mode == decision.ModeBlock || r.Mode == decision.ModeFallback) {
			out = append(out, r.Name)
		}
	}
	return out
}

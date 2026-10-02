package main

import (
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// connectStartRequest is api.ts ConnectStartRequest (#58).
type connectStartRequest struct {
	ReturnTo string `json:"return_to"`
}

// connectStartResponse is api.ts ConnectStartResponse.
type connectStartResponse struct {
	AuthURL   string `json:"auth_url"`
	Nonce     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
}

// connectCompleteRequest is api.ts ConnectCompleteRequest (#59). A missing bind binds both the
// fast check and the judge; an empty list binds nothing.
type connectCompleteRequest struct {
	Code  string    `json:"code"`
	Nonce string    `json:"nonce"`
	Bind  *[]string `json:"bind"`
}

// connectCompleteResponse is api.ts ConnectCompleteResponse: the credential write's result and
// the dashboard path to return to (the web navigates only to this server-echoed path).
type connectCompleteResponse struct {
	CredentialWriteResult
	ReturnTo string `json:"return_to"`
}

// connectBindDefault is what Connect OpenRouter binds unless the request says otherwise (D38).
var connectBindDefault = []string{runtimecfg.TargetDecisionModel, runtimecfg.TargetJudge}

// handleConnectStart serves POST /api/connect/openrouter/start (SPEC §2.4 row 58, §7.5, D38): it
// starts an OpenRouter sign-in for a self-hosted gateway. The PKCE verifier stays on the server
// under a one-time nonce; the browser keeps only the nonce and opens auth_url, and OpenRouter
// sends it back to <dashboard origin>/connect/callback. The dashboard origin is the request's
// Origin when the gateway allows it (so http://127.0.0.1:3000 works as well as localhost), else
// TRUTHTABLE_PUBLIC_URL; an Origin that is present but not allowed is 409 with details.public_url,
// because the sign-in could not come back there.
func (s *server) handleConnectStart(c *gin.Context) {
	var body connectStartRequest
	if !bindJSON(c, &body, true) {
		return
	}
	snap := s.rt.manager.Current()
	if e := s.connectRefusal(snap); e != nil {
		s.abortCredential(c, "connect start", e)
		return
	}
	returnTo, err := credentials.ValidateReturnTo(body.ReturnTo)
	if err != nil {
		s.abortCredential(c, "connect start", credentialInvalid("return_to", err.Error()+"."))
		return
	}
	origin, err := credentials.ConnectCallbackOrigin(c.GetHeader("Origin"), s.auth.keys.AllowsOrigin, s.cfg.PublicURL)
	if err != nil {
		abortAPIDetails(c, http.StatusConflict, codeConflict,
			"This dashboard address is not one the gateway allows, so OpenRouter could not return here. Open the dashboard at "+
				s.cfg.PublicURL+" and connect again, or add this address to TRUTHTABLE_ALLOWED_ORIGINS.",
			map[string]any{"public_url": s.cfg.PublicURL, "reason": "callback_origin"})
		return
	}
	nonce, pending, err := s.apply.creds.connects.Start(connectActor(c), origin, returnTo)
	if err != nil {
		s.abortCredential(c, "connect start", err)
		return
	}
	authURL, err := credentials.ConnectAuthURL(s.cfg.Jev.OpenRouterBaseURL, pending.CallbackOrigin, credentials.CodeChallenge(pending.Verifier))
	if err != nil {
		s.abortCredential(c, "connect start", err)
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, connectStartResponse{AuthURL: authURL, Nonce: nonce, ExpiresIn: int(credentials.ConnectTTL.Seconds())})
}

// handleConnectComplete serves POST /api/connect/openrouter/complete (SPEC §2.4 row 59, §7.5,
// D38): the callback page sends OpenRouter's code with its nonce. The nonce is used up by the
// first call whatever happens next; an unknown or expired one, another admin's, or one sent from
// another origin than the sign-in returned to is 400 invalid_nonce. The server trades the code
// for a key at OpenRouter (POST /api/v1/auth/keys with the verifier), so the key never reaches
// browser JavaScript, then stores it through the shared credential write (D75): checked first,
// bound to the fast check and the judge only when the check passes, and on 402 stored unbound
// with the no_credits hint.
func (s *server) handleConnectComplete(c *gin.Context) {
	var body connectCompleteRequest
	if !bindJSON(c, &body, false) {
		return
	}
	pending, err := s.apply.creds.connects.Complete(body.Nonce, connectActor(c), c.GetHeader("Origin"))
	if err != nil {
		abortAPI(c, http.StatusBadRequest, codeInvalidNonce,
			"This sign-in is unknown, expired, already used, or was started elsewhere. Start Connect OpenRouter again.")
		return
	}
	if body.Code == "" {
		s.abortCredential(c, "connect complete", credentialInvalid("code", "code is required: OpenRouter sends it to /connect/callback."))
		return
	}
	bind := connectBindDefault
	if body.Bind != nil {
		bind = *body.Bind
	}
	for _, t := range bind {
		if !slices.Contains(connectBindDefault, t) {
			s.abortCredential(c, "connect complete", credentialInvalid("bind", "bind takes decision_model and judge."))
			return
		}
	}
	snap := s.rt.manager.Current()
	if e := s.connectRefusal(snap); e != nil {
		s.abortCredential(c, "connect complete", e)
		return
	}
	ctx := c.Request.Context()
	allowed := credentials.VendorOrigins(credentials.NameOpenRouter, s.cfg.Jev.OpenRouterBaseURL)
	key, err := credentials.ExchangeConnectCode(ctx, s.apply.creds.exchange, s.cfg.Jev.OpenRouterBaseURL, allowed, body.Code, pending.Verifier)
	if err != nil {
		details := map[string]any{"reason": "exchange_failed"}
		var xe *credentials.ExchangeError
		if errors.As(err, &xe) && xe.Status != 0 {
			details["status"] = xe.Status
		}
		s.log.Warn("Connect OpenRouter: the code exchange failed", "err", s.redact(err.Error()))
		abortAPIDetails(c, http.StatusBadGateway, "upstream_error", cutChars(s.redact(err.Error()), maxTestMessage), details)
		return
	}
	// D42: the received key joins the process redactor before its first use.
	if s.redactor != nil {
		s.redactor.Add(key)
	}
	res, err := s.writeCredential(ctx, CredentialWrite{
		Name: credentials.NameOpenRouter, Provider: credentials.ProviderOpenRouter, Secret: key,
		Verify: true, Bind: bind, Actor: actor(c), Action: "credential.connect_openrouter",
	})
	if err != nil {
		s.abortCredential(c, "connect complete", err)
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, connectCompleteResponse{CredentialWriteResult: res, ReturnTo: pending.ReturnTo})
}

// connectRefusal is why Connect OpenRouter cannot run now (D38): OPENROUTER_API_KEY is set (409
// env_managed), or the credential store is not ready (503).
func (s *server) connectRefusal(snap *runtimecfg.Snapshot) *credentialError {
	spec, _ := credentials.Lookup(credentials.NameOpenRouter)
	if side := credentialEnvSide(snap, spec); side != "" {
		return credentialEnvManaged(spec, side)
	}
	return s.credentialStoreRefusal(snap)
}

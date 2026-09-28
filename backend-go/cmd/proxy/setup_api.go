package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// recoveryCommand mints a recovery setup token while an admin credential exists (SPEC §7.5).
const recoveryCommand = "docker compose exec proxy /proxy -new-setup-token -force"

// setupAnnounceTimeout bounds the boot-time setup check and token mint: a database that hangs must
// not keep the proxy from starting (the token can be minted later with -new-setup-token).
var setupAnnounceTimeout = 10 * time.Second

// setupStatusBody is api.ts SetupStatus (GET /api/setup/status, public).
type setupStatusBody struct {
	SetupRequired   bool   `json:"setup_required"`
	IngestAuth      string `json:"ingest_auth"`
	Demo            bool   `json:"demo"`
	Readonly        bool   `json:"readonly"`
	Persistence     bool   `json:"persistence"`
	PublicURL       string `json:"public_url"`
	RecoveryCommand string `json:"recovery_command"`
}

// setupStatus serves GET /api/setup/status: whether the dashboard still needs its first admin.
func (h *authzAPI) setupStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	required, err := h.auth.Setup().Required(ctx)
	if err != nil {
		h.log.Warn("setup status: admin keys cannot be checked", "err", err)
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "The setup state cannot be checked right now: the database is unavailable.")
		return
	}
	c.JSON(http.StatusOK, setupStatusBody{
		SetupRequired: required, IngestAuth: h.auth.IngestMode(), Demo: h.cfg.DemoMode, Readonly: h.cfg.Readonly,
		Persistence: h.auth.HasStore(), PublicURL: strings.TrimRight(h.cfg.PublicURL, "/"), RecoveryCommand: recoveryCommand,
	})
}

// announceSetup prints the one-time setup URL to out ("SETUP: open …/app/setup#token=…") when no
// admin credential exists (SPEC §7.5: printed at every proxy start). Only the stdout line carries
// the token; the log says where to look.
func (s *server) announceSetup(ctx context.Context, out io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, setupAnnounceTimeout)
	defer cancel()
	setup := s.gateway.auth.Setup()
	if !s.gateway.auth.HasStore() {
		if required, _ := setup.Required(ctx); required {
			s.log.Warn("no admin credential and no database: set TRUTHTABLE_ADMIN_KEYS (label=key) to sign in to the dashboard")
		}
		return
	}
	printed, exp, err := setup.Announce(ctx, out, s.cfg.Auth.SetupToken)
	switch {
	case err != nil:
		s.log.Error("the setup token could not be created; mint one later with -new-setup-token (make setup-url)", "err", err)
	case printed:
		s.log.Warn("dashboard setup required: open the SETUP URL printed on stdout (docker compose logs proxy | grep -m1 SETUP)",
			"expires_at", exp.UTC().Format(time.RFC3339), "new_url", "make setup-url")
	}
}

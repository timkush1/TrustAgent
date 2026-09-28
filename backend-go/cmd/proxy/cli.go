package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
)

// Exit codes of the command-line modes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2 // also config.StrictExitCode
)

// bootConfig handles the command-line modes (SPEC §3.1) and strict mode before the proxy starts:
//
//	-check-config            print the effective configuration (secrets masked) and its warnings;
//	                         exit 2 when there are warnings
//	-dump-config-spec        print the proxy's registry keys and their Config fields as JSON
//	-new-setup-token [-force] store a new one-time setup token and print its SETUP line; -force
//	                         mints a recovery token while an admin credential exists
//
// Without a mode it loads the configuration; TRUTHTABLE_STRICT_CONFIG=true with any warning exits
// with code 2 (SPEC §2.7). It returns the configuration to run with, or exit=true and the code.
func bootConfig(args []string, stdout, stderr io.Writer) (cfg *config.Config, code int, exit bool) {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	checkConfig := fs.Bool("check-config", false, "print the effective configuration and its warnings; exit 2 on warnings")
	dumpSpec := fs.Bool("dump-config-spec", false, "print the proxy's configuration keys as JSON")
	newSetupToken := fs.Bool("new-setup-token", false, "store a new one-time dashboard setup token and print its URL")
	force := fs.Bool("force", false, "with -new-setup-token: mint a recovery token although an admin credential exists")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitOK, true
		}
		return nil, exitUsage, true
	}
	modes := 0
	for _, on := range []bool{*checkConfig, *dumpSpec, *newSetupToken} {
		if on {
			modes++
		}
	}
	switch {
	case fs.NArg() > 0:
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0)) //nolint:errcheck // best-effort usage output
		return nil, exitUsage, true
	case modes > 1:
		fmt.Fprintln(stderr, "use one of -check-config, -dump-config-spec and -new-setup-token") //nolint:errcheck // usage
		return nil, exitUsage, true
	case *force && !*newSetupToken:
		fmt.Fprintln(stderr, "-force only applies to -new-setup-token") //nolint:errcheck // usage
		return nil, exitUsage, true
	}

	switch {
	case *dumpSpec:
		b, err := config.DumpSpecJSON(config.MustRegistry())
		if err == nil {
			_, err = stdout.Write(b)
		}
		if err != nil {
			fmt.Fprintln(stderr, "dump-config-spec:", err) //nolint:errcheck // best effort
			return nil, exitFailure, true
		}
		return nil, exitOK, true
	case *checkConfig:
		c := config.Load()
		if err := config.WriteReport(stdout, c); err != nil {
			return nil, exitFailure, true
		}
		if len(c.Diagnostics) > 0 {
			return nil, config.StrictExitCode, true
		}
		return nil, exitOK, true
	case *newSetupToken:
		return nil, runNewSetupToken(config.Load(), *force, stdout, stderr), true
	}

	cfg = config.Load()
	if cfg.StrictFailure() {
		fmt.Fprintf(stderr, "TRUTHTABLE_STRICT_CONFIG: refusing to start with %d configuration warning(s):\n", len(cfg.Diagnostics)) //nolint:errcheck // best effort
		for _, w := range cfg.Warnings {
			fmt.Fprintln(stderr, "  "+w) //nolint:errcheck // best effort
		}
		return nil, config.StrictExitCode, true
	}
	return cfg, exitOK, false
}

// runNewSetupToken is -new-setup-token [-force]: it stores a new setup token (replacing any
// previous one) and prints "SETUP: open <public URL>/app/setup#token=<t>" on stdout.
func runNewSetupToken(cfg *config.Config, force bool, stdout, stderr io.Writer) int {
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "-new-setup-token needs TRUTHTABLE_DATABASE_URL: the token is stored in the database. "+ //nolint:errcheck // best effort
			"Without a database, set TRUTHTABLE_ADMIN_KEYS (label=key) and sign in with that key.")
		return exitFailure
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "the database cannot be opened:", err) //nolint:errcheck // best effort
		return exitFailure
	}
	defer pg.Close()
	auth := newAuthenticator(cfg, pg, nil, slog.New(slog.DiscardHandler))
	token, exp, err := auth.Setup().Mint(ctx, "", force, "cli")
	switch {
	case errors.Is(err, authz.ErrSetupNotRequired):
		fmt.Fprintln(stderr, "An admin credential already exists: sign in with it, or run with -force to mint a recovery token.") //nolint:errcheck // best effort
		return exitFailure
	case err != nil:
		fmt.Fprintln(stderr, "the setup token could not be stored:", err) //nolint:errcheck // best effort
		return exitFailure
	}
	if _, err := fmt.Fprintln(stdout, auth.Setup().Line(token)); err != nil {
		return exitFailure
	}
	fmt.Fprintf(stderr, "The setup URL is valid until %s and works once; it creates a new admin key.\n", exp.UTC().Format(time.RFC3339)) //nolint:errcheck // best effort
	return exitOK
}

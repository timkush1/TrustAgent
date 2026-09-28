// Command mock-upstream is a fake of every vendor the gateway calls (SPEC §3.1): OpenAI and
// Anthropic LLM APIs (JSON and SSE, with latency knobs), and OpenRouter's decision, systemone,
// chat, PKCE and key endpoints, with scenario-keyed faults. It serves the e2e and load stacks
// (compose profiles e2e and load) and never calls anything itself.
//
//	mock-upstream -addr :8090 [-scenarios file] [-latency 0s] [-jev-latency 0s] [-chunk-delay 20ms]
//	              [-fault 529@1] [-snapshot typesafe/jev-1.13-20261015]
//
// See package mockupstream for the routes, the fault names and how a request selects them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mock-upstream:", err)
		os.Exit(2)
	}
}

// options are the command-line flags.
type options struct {
	addr string
	cfg  mockupstream.Config
}

func parseFlags(args []string, stderr io.Writer) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("mock-upstream", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.addr, "addr", envOr("MOCK_UPSTREAM_ADDR", ":8090"), "listen address")
	fs.StringVar(&o.cfg.ScenariosPath, "scenarios", "", "scenario pack (default: the embedded contracts/demo/scenarios.v1.json)")
	fs.DurationVar(&o.cfg.Latency, "latency", 0, "delay before every LLM response")
	fs.DurationVar(&o.cfg.JevLatency, "jev-latency", 0, "delay before every decision response")
	fs.DurationVar(&o.cfg.ChunkDelay, "chunk-delay", 20*time.Millisecond, "delay between SSE events")
	fs.StringVar(&o.cfg.Fault, "fault", "", "default fault spec for every request, e.g. 529 or hang@1")
	fs.StringVar(&o.cfg.Snapshot, "snapshot", "", "model id reported by decision responses")
	fs.StringVar(&o.cfg.DefaultAnswer, "default-answer", "", "LLM answer for requests that match no scenario")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return o, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	o, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	o.cfg.Logger = log
	srv, err := mockupstream.New(o.cfg)
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: o.addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("mock-upstream listening", "addr", o.addr, "fault", o.cfg.Fault, "snapshot", o.cfg.Snapshot)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

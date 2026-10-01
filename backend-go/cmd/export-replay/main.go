// Command export-replay writes a replayable recording (trustagent.recording/v1, SPEC §2.13) of
// gateway traffic: manifest.json plus traces/<trace_id>.json, each a TraceDetail with its bodies,
// gate decisions and deep audit. The static demo replays it (make record-demo).
//
// Traces come from Postgres (--database-url, default TRUTHTABLE_DATABASE_URL; the database is read,
// never migrated) or from a running gateway's REST API (--api with an admin key file). At most 200
// traces are exported, the newest in [--since, --until), optionally one --route or --source.
//
// The label is never chosen by hand. kind recorded ("Recorded {date} · {snapshot} + {judge} ·
// {scripted|generated} answers · replayed, not live") needs a real decision backend, a judge that
// is neither a fallback nor the mock, and an answer_source on every trace; the mock decision
// backend gives kind mock ("Simulated (mock checks + mock judge): not model output"). Anything else
// is refused. Every string is sanitized before it is written: credentials (whole PEM private-key
// blocks included), emails and hosts are masked in place, keeping UTF-16 offsets valid. Human
// labels are left out of the trace files.
//
//	export-replay --out frontend-react/public/demo/recorded --since 2026-10-05T10:00:00Z --source trafficgen
//	export-replay --api http://127.0.0.1:8080 --api-key-file admin.key --out /tmp/rec --route support-bot
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/version"
)

// Exit statuses.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, deps{
		getenv: os.Getenv, now: time.Now, rev: gitRevision,
		openDB: func(ctx context.Context, url string) (recording.TraceStore, func(), error) {
			s, err := store.OpenPostgresStore(ctx, url)
			if err != nil {
				return nil, nil, err
			}
			return s, s.Close, nil
		},
	}))
}

// deps are what run takes from the process, so tests can replace them.
type deps struct {
	getenv func(string) string
	now    func() time.Time
	rev    func() string
	openDB func(ctx context.Context, url string) (recording.TraceStore, func(), error)
	client *http.Client
}

// traceSources are the TraceSource values --source accepts.
var traceSources = []string{"proxy", "direct", "playground", "test", "trafficgen"}

var gitSHAPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	fl := flag.NewFlagSet("export-replay", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var (
		out, databaseURL, api, apiKeyFile, since, until, source, route string
		judgeProvider, judgeModel, gitSHA                              string
		limit                                                          int
		force                                                          bool
	)
	fl.StringVar(&out, "out", "", "output directory for manifest.json and traces/ (required)")
	fl.StringVar(&databaseURL, "database-url", d.getenv("TRUTHTABLE_DATABASE_URL"), "Postgres URL to read (never migrated)")
	fl.StringVar(&api, "api", "", "read through a gateway's REST API at this base URL instead of Postgres")
	fl.StringVar(&apiKeyFile, "api-key-file", "", "file holding an API key with the read and content scopes (for --api)")
	fl.StringVar(&since, "since", "", "export traces created at or after this RFC 3339 time")
	fl.StringVar(&until, "until", "", "export traces created before this RFC 3339 time")
	fl.StringVar(&source, "source", "", "only traces from this source (proxy, direct, playground, test, trafficgen)")
	fl.StringVar(&route, "route", "", "only traces of this route")
	fl.IntVar(&limit, "limit", recording.MaxTraces, "most traces to export (the newest; at most 200)")
	fl.StringVar(&judgeProvider, "judge-provider", "", "judge provider, when no exported trace has a deep audit")
	fl.StringVar(&judgeModel, "judge-model", "", "judge model, when no exported trace has a deep audit")
	fl.StringVar(&gitSHA, "git-sha", "", "git revision recorded in the manifest (default: git rev-parse HEAD)")
	fl.BoolVar(&force, "force", false, "replace an existing recording in --out")
	fl.Usage = func() {
		fmt.Fprintln(stderr, "usage: export-replay --out DIR [--database-url URL | --api URL --api-key-file FILE] [--since T] [--until T] [--source S] [--route R] [--limit N]")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		return exitUsage
	}
	usage := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "export-replay: "+format+"\n", args...)
		return exitUsage
	}
	if fl.NArg() > 0 || out == "" {
		fl.Usage()
		return exitUsage
	}
	f := recording.Filter{Source: source, Route: route, Limit: limit}
	if limit < 1 || limit > recording.MaxTraces {
		return usage("--limit must be between 1 and %d", recording.MaxTraces)
	}
	if source != "" && !slices.Contains(traceSources, source) {
		return usage("unknown --source %q (want one of %s)", source, strings.Join(traceSources, ", "))
	}
	for _, p := range []struct {
		name, v string
		dst     **time.Time
	}{{"since", since, &f.Since}, {"until", until, &f.Until}} {
		if p.v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, p.v)
		if err != nil {
			return usage("--%s: %v", p.name, err)
		}
		*p.dst = &t
	}
	var judge *recording.Judge
	switch {
	case judgeProvider != "" && judgeModel != "":
		judge = &recording.Judge{Provider: judgeProvider, Model: judgeModel}
	case judgeProvider != "" || judgeModel != "":
		return usage("pass both --judge-provider and --judge-model")
	}
	if gitSHA == "" {
		gitSHA = d.rev()
	}
	if !gitSHAPattern.MatchString(gitSHA) {
		return usage("git revision %q is not a hex sha: pass --git-sha", gitSHA)
	}

	src, closeSrc, err := openSource(ctx, d, databaseURL, api, apiKeyFile)
	if err != nil {
		fmt.Fprintf(stderr, "export-replay: %v\n", err)
		if errors.Is(err, errUsage) {
			return exitUsage
		}
		return exitFailed
	}
	defer closeSrc()
	traces, err := src.Traces(ctx, f)
	if err != nil {
		fmt.Fprintf(stderr, "export-replay: %v\n", err)
		return exitFailed
	}
	rec, err := recording.Build(traces, recording.Options{GitSHA: gitSHA, Version: version.Version, Judge: judge})
	if err != nil {
		fmt.Fprintf(stderr, "export-replay: %v\n", err)
		return exitFailed
	}
	san, err := recording.NewSanitizer()
	if err != nil {
		fmt.Fprintf(stderr, "export-replay: %v\n", err)
		return exitFailed
	}
	if err := writeRecording(out, rec, traces, san, force); err != nil {
		fmt.Fprintf(stderr, "export-replay: %v\n", err)
		return exitFailed
	}
	unavailable := 0
	for _, t := range traces {
		if !t.Detail.Body.Available {
			unavailable++
		}
	}
	fmt.Fprintf(stdout, "Wrote a %s recording of %d traces (%d scripted, %d generated answers) to %s\n  Label: %s\n",
		rec.Kind, rec.Totals.Traces, rec.AnswerSources.Scripted, rec.AnswerSources.Generated, out, rec.Label)
	if unavailable > 0 {
		fmt.Fprintf(stdout, "  Note: %d traces have no stored body (expired or not stored); they replay without their texts.\n", unavailable)
	}
	return exitOK
}

// errUsage marks errors the caller fixes with other arguments.
var errUsage = errors.New("usage")

func openSource(ctx context.Context, d deps, databaseURL, api, apiKeyFile string) (traceSource, func(), error) {
	if api != "" {
		key := ""
		if apiKeyFile != "" {
			b, err := os.ReadFile(apiKeyFile) // #nosec G304 -- the key file the operator named on the command line
			if err != nil {
				return nil, nil, err
			}
			key = strings.TrimSpace(string(b))
		}
		client := d.client
		if client == nil {
			client = &http.Client{Timeout: time.Minute}
		}
		s, err := newAPISource(api, key, client)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		return s, func() {}, nil
	}
	if databaseURL == "" {
		return nil, nil, fmt.Errorf("%w: set TRUTHTABLE_DATABASE_URL, --database-url or --api", errUsage)
	}
	st, closeDB, err := d.openDB(ctx, databaseURL)
	if err != nil {
		return nil, nil, err
	}
	return &recording.DBSource{Store: st, Now: d.now, Bodies: true}, closeDB, nil
}

// writeRecording writes manifest.json and traces/<id>.json under dir, sanitized. An existing
// recording (a manifest, or trace files an interrupted export left) is replaced only with force;
// other files in dir are left alone.
func writeRecording(dir string, rec *recording.Recording, traces []recording.Trace, san *recording.Sanitizer, force bool) error {
	manifest := filepath.Join(dir, "manifest.json")
	tracesDir := filepath.Join(dir, "traces")
	_, statErr := os.Stat(manifest)
	existing, readErr := os.ReadDir(tracesDir)
	if statErr == nil || (readErr == nil && len(existing) > 0) {
		if !force {
			return fmt.Errorf("%s already holds a recording: pass --force to replace it", dir)
		}
		if err := os.RemoveAll(tracesDir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(tracesDir, 0o750); err != nil {
		return err
	}
	for _, t := range traces {
		b, err := san.Detail(t.Detail)
		if err != nil {
			return err
		}
		// The trace id was checked to be a UUID, so it is a plain file name.
		if err := writeFile(filepath.Join(tracesDir, t.Detail.TraceID+".json"), b); err != nil {
			return err
		}
	}
	b, err := san.JSON(rec)
	if err != nil {
		return err
	}
	return writeFile(manifest, b)
}

func writeFile(path string, b []byte) error {
	// Recordings are published static assets.
	return os.WriteFile(path, b, 0o644) // #nosec G306 -- a public, sanitized recording
}

// gitRevision is the short HEAD revision, or "" when git cannot tell.
func gitRevision() string {
	// #nosec G204 -- a fixed git command
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Masked is what a redacted secret is replaced with in logs.
const Masked = "[redacted]"

// minRedactLen is the shortest loaded secret the Redactor replaces verbatim: shorter values would
// shred ordinary log text (a 3-character key would mask every "abc").
const minRedactLen = 6

// MaskSecret renders a secret for display: "" stays "", a short value is "****", a longer one
// keeps its first 3 and last 4 characters ("sk-…abcd"), which identify a key without revealing it.
func MaskSecret(v string) string {
	switch {
	case v == "":
		return ""
	case len(v) < 16:
		return "****"
	}
	return v[:3] + "…" + v[len(v)-4:]
}

// DisplayValue is an entry's value as it may be shown: secrets masked (a key_list keeps its labels
// and masks every key).
func (e Entry) DisplayValue() string {
	if !e.Key.Secret || e.Value == "" {
		return e.Value
	}
	if e.Key.Kind != KindKeyList {
		return MaskSecret(e.Value)
	}
	parts := strings.Split(e.Value, ",")
	for i, p := range parts {
		label, key, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			parts[i] = "****"
			continue
		}
		parts[i] = strings.TrimSpace(label) + "=" + MaskSecret(strings.TrimSpace(key))
	}
	return strings.Join(parts, ",")
}

// Secrets returns every loaded secret value: each secret key's value, each key of the key_list
// keys (label=key lists) and credentials read from *_FILE keys, without duplicates.
func (c *Config) Secrets() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, e := range c.entries {
		if !e.Key.Secret || e.Source == SourceDefault {
			continue
		}
		if e.Key.Kind == KindKeyList {
			for _, item := range strings.Split(e.Value, ",") {
				if _, key, ok := strings.Cut(item, "="); ok {
					add(key)
				} else {
					add(item)
				}
			}
			continue
		}
		add(e.Value)
	}
	add(c.Jev.APIKey) // may come from TRUTHTABLE_JEV_API_KEY_FILE, which is not itself secret
	return out
}

// secretPatterns are credential shapes masked wherever they appear (SPEC §7.5 redaction), plus
// TrustAgent's own credentials as a request dump shows them (gin's panic recovery prints the
// request line and headers): minted keys, the X-TrustAgent-Key header, the ta_session cookie and
// a WebSocket ?ticket=.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)(bearer\s+)\S+`),
	regexp.MustCompile(`(?i)(x-api-key:\s*)\S+`),
	regexp.MustCompile(`ta_[A-Za-z0-9_-]{43}`),
	regexp.MustCompile(`(?i)(x-trustagent-key:\s*)\S+`),
	regexp.MustCompile(`(ta_session=)[^;\s]+`),
	regexp.MustCompile(`([?&]ticket=)[^&\s#]+`),
}

// Redactor masks loaded secrets and credential patterns in text and log records. It is safe for
// concurrent use; Add registers secrets learned after startup.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // longest first, so a secret containing another is masked whole
}

// NewRedactor returns a Redactor for the given secrets.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	r.Add(secrets...)
	return r
}

// Redactor returns a Redactor for every secret this configuration loaded.
func (c *Config) Redactor() *Redactor { return NewRedactor(c.Secrets()...) }

// Add registers more secrets.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		if len(s) >= minRedactLen {
			r.secrets = append(r.secrets, s)
		}
	}
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// Redact masks every registered secret and credential pattern in s.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Masked)
		}
	}
	r.mu.RUnlock()
	for _, p := range secretPatterns {
		if p.NumSubexp() > 0 {
			s = p.ReplaceAllString(s, "${1}"+Masked)
		} else {
			s = p.ReplaceAllString(s, Masked)
		}
	}
	return s
}

// Writer returns an io.Writer that redacts everything written through it before passing it on to
// w, for loggers outside slog: gin.DefaultErrorWriter, where gin's panic recovery dumps the request
// headers (it masks only Authorization). Each Write is redacted on its own; the loggers it serves
// write whole records at once.
func (r *Redactor) Writer(w io.Writer) io.Writer { return redactingWriter{w: w, r: r} }

type redactingWriter struct {
	w io.Writer
	r *Redactor
}

func (rw redactingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.r.Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ReplaceAttr is a slog.HandlerOptions.ReplaceAttr that redacts the message and every string,
// error and stringer attribute.
func (r *Redactor) ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		if red := r.Redact(a.Value.String()); red != a.Value.String() {
			a.Value = slog.StringValue(red)
		}
	case slog.KindAny:
		var s string
		switch v := a.Value.Any().(type) {
		case error:
			s = v.Error()
		case fmt.Stringer:
			s = v.String()
		default:
			return a
		}
		if red := r.Redact(s); red != s {
			a.Value = slog.StringValue(red)
		}
	}
	return a
}

// Handler wraps h so that every record's message and attributes are redacted, including
// attributes added with WithAttrs and groups. Prefer ReplaceAttr on handlers you construct.
func (r *Redactor) Handler(h slog.Handler) slog.Handler { return &redactingHandler{next: h, r: r} }

type redactingHandler struct {
	next slog.Handler
	r    *Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *redactingHandler) redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		red := make([]any, 0, len(attrs))
		for _, g := range attrs {
			red = append(red, h.redactAttr(g))
		}
		return slog.Group(a.Key, red...)
	}
	return h.r.ReplaceAttr(nil, a)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		red = append(red, h.redactAttr(a))
	}
	return &redactingHandler{next: h.next.WithAttrs(red), r: h.r}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), r: h.r}
}

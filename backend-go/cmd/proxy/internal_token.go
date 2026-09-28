package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// internalToken returns the token every engine RPC carries as x-trustagent-internal (SPEC §2.2):
// TRUTHTABLE_INTERNAL_TOKEN when set; otherwise the token in TRUTHTABLE_INTERNAL_TOKEN_FILE. The
// proxy generates that file on its first start (the engine reads it from the shared volume) and
// reuses it afterwards. When the token directory does not exist (a local run without the shared
// volume) or the file cannot be written, RPCs carry no token and the engine must accept loopback
// callers.
func internalToken(token, path string, log *slog.Logger) string {
	if token != "" {
		return token
	}
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the token file (TRUTHTABLE_INTERNAL_TOKEN_FILE)
	switch {
	case err == nil:
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	case !errors.Is(err, fs.ErrNotExist):
		log.Warn("internal token: the token file cannot be read; engine RPCs carry no token", "path", path, "err", err)
		return ""
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		log.Info("internal token: no token directory; engine RPCs carry no token", "dir", dir)
		return ""
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		log.Warn("internal token: no randomness; engine RPCs carry no token", "err", err)
		return ""
	}
	t := hex.EncodeToString(raw)
	if err := writeTokenFile(dir, path, t); err != nil {
		log.Warn("internal token: the token file cannot be written; engine RPCs carry no token", "path", path, "err", err)
		return ""
	}
	log.Info("internal token: generated for the engine", "path", path)
	return t
}

// writeTokenFile writes the token atomically (a temporary file renamed over path), readable by the
// owner and its group: the engine shares the proxy's uid and gid in compose.
func writeTokenFile(dir, path, token string) error {
	f, err := os.CreateTemp(dir, ".internal-token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op once renamed
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

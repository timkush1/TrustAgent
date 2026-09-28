package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInternalToken(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()
	path := filepath.Join(dir, "internal.token")

	if got := internalToken("from-env", path, log); got != "from-env" {
		t.Errorf("TRUTHTABLE_INTERNAL_TOKEN wins: %q", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an env token must not write the token file")
	}

	generated := internalToken("", path, log)
	if len(generated) != 64 {
		t.Fatalf("generated token %q", generated)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != generated {
		t.Fatalf("token file %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("token file mode %v", fi.Mode().Perm())
		}
	}
	if again := internalToken("", path, log); again != generated {
		t.Errorf("a restart reuses the token file: %q, want %q", again, generated)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}

	if got := internalToken("", filepath.Join(dir, "missing", "internal.token"), log); got != "" {
		t.Errorf("without the token directory RPCs carry no token: %q", got)
	}
	if got := internalToken("", "", log); got != "" {
		t.Errorf("without a token file RPCs carry no token: %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.openai.com":                         "https://api.openai.com",
		"https://user:secret@llm.example/v1?api-key=abc": "https://user:xxxxx@llm.example/v1",
		"http://ollama:11434":                            "http://ollama:11434",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

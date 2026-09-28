package decision_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision/vectors"
)

// TestVectors runs every golden vector in contracts/vectors against the Go reference (SPEC §7.6).
// Kinds the reference implements must pass in full, draft and frozen alike; kinds without a Go
// runner are skipped with the reason from vectors.Kinds; an unknown kind fails.
func TestVectors(t *testing.T) {
	all, err := vectors.Load(contracts.FS, "vectors")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no vectors found under contracts/vectors")
	}
	byKind := map[string][]*vectors.Vector{}
	for _, v := range all {
		byKind[v.Dir] = append(byKind[v.Dir], v)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	for _, kind := range kinds {
		list := byKind[kind]
		t.Run(kind, func(t *testing.T) {
			k, known := vectors.Kinds[kind]
			if !known {
				t.Fatalf("unknown vector kind %q (%d files)", kind, len(list))
			}
			for _, v := range list {
				if err := v.Check(); err != nil {
					t.Errorf("%s: %v", v.Path, err)
				}
			}
			if k.Skip != "" {
				t.Skipf("%d vectors not run: %s", len(list), k.Skip)
			}
			for _, v := range list {
				t.Run(v.Name, func(t *testing.T) { runVector(t, v) })
			}
		})
	}
}

func runVector(t *testing.T, v *vectors.Vector) {
	t.Helper()
	var expected any
	if err := json.Unmarshal(v.Expected, &expected); err != nil {
		t.Fatalf("%s: expected: %v", v.Path, err)
	}
	if expected == nil {
		t.Fatalf("%s: expected is null; run make vectors to generate it", v.Path)
	}
	got, err := vectors.Run(v.Kind, v.Input, contracts.FS)
	if err != nil {
		t.Fatalf("%s: %v", v.Path, err)
	}
	if diffs := vectors.Compare(expected, got, v.Tol()); len(diffs) > 0 {
		t.Errorf("%s (%s):\n  %s", v.Path, v.Status, strings.Join(diffs, "\n  "))
	}
}

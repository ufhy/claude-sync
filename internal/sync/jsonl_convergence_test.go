package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tawanorg/claude-sync/internal/config"
	"github.com/tawanorg/claude-sync/internal/crypto"
)

// TestParallelSessionConverges simulates two machines editing the SAME session
// file, then a full push/pull round-trip, and asserts both machines end with a
// byte-identical union of all records and no .conflict sidecar is produced.
func TestParallelSessionConverges(t *testing.T) {
	ctx := context.Background()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if err := crypto.GenerateKeyFromPassphrase(keyPath, "test-passphrase"); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEncryptor(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	shared := newMockStorage()

	newMachine := func(name string) (*Syncer, string) {
		base := t.TempDir()
		claudeDir := filepath.Join(base, name, ".claude")
		stateDir := filepath.Join(base, name, ".claude-sync")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		state, _ := LoadStateFromDir(stateDir)
		return &Syncer{
			storage:   shared,
			encryptor: enc,
			state:     state,
			claudeDir: claudeDir,
			quiet:     true,
			cfg:       &config.Config{},
		}, claudeDir
	}

	const sess = "projects/-work-app/1111.jsonl"
	a, aDir := newMachine("A")
	b, bDir := newMachine("B")

	// A creates the session, pushes; B pulls the shared baseline.
	base := `{"uuid":"r","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z","type":"user"}
{"uuid":"a1","parentUuid":"r","timestamp":"2026-07-01T10:01:00Z","type":"assistant"}`
	writeFile(t, aDir, sess, base)
	if _, err := a.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pull(ctx); err != nil {
		t.Fatal(err)
	}

	// Both resume the same session and append DIFFERENT records.
	writeFile(t, aDir, sess, base+
		"\n"+`{"uuid":"a2","parentUuid":"a1","timestamp":"2026-07-01T10:05:00Z","type":"user"}`)
	writeFile(t, bDir, sess, base+
		"\n"+`{"uuid":"b2","parentUuid":"a1","timestamp":"2026-07-01T10:03:00Z","type":"user"}`)

	// Round-trip: A push -> B pull (merges) -> B push -> A pull (merges).
	if _, err := a.Push(ctx); err != nil {
		t.Fatal(err)
	}
	rb, err := b.Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.Conflicts) != 0 {
		t.Fatalf("B produced conflicts, expected merge: %v", rb.Conflicts)
	}
	if _, err := b.Push(ctx); err != nil {
		t.Fatal(err)
	}
	ra, err := a.Pull(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Conflicts) != 0 {
		t.Fatalf("A produced conflicts, expected merge: %v", ra.Conflicts)
	}

	// No .conflict sidecar anywhere.
	for _, dir := range []string{aDir, bDir} {
		filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
			if strings.Contains(p, ".conflict.") {
				t.Fatalf("unexpected conflict file: %s", p)
			}
			return nil
		})
	}

	// Both machines converge to the identical union containing all 4 records.
	fa := readFile(t, aDir, sess)
	fb := readFile(t, bDir, sess)
	if fa != fb {
		t.Fatalf("machines diverged:\nA=%q\nB=%q", fa, fb)
	}
	for _, id := range []string{`"uuid":"r"`, `"uuid":"a1"`, `"uuid":"a2"`, `"uuid":"b2"`} {
		if !strings.Contains(fa, id) {
			t.Fatalf("converged file missing record %s:\n%s", id, fa)
		}
	}
	// Parent-before-child and sibling ordering by timestamp (b2@10:03 < a2@10:05).
	if !(strings.Index(fa, `"a1"`) < strings.Index(fa, `"b2"`) &&
		strings.Index(fa, `"b2"`) < strings.Index(fa, `"a2"`)) {
		t.Fatalf("bad record ordering in converged file:\n%s", fa)
	}
}

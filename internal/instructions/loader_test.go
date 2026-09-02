package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoaderExtendsDefault(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "INSTRUCTION.md")
	extra := filepath.Join(directory, "INSTRUCTION-ADDITIONAL.md")
	if err := os.WriteFile(base, []byte("base rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte("project rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := (Loader{DefaultPath: base, AdditionalPath: extra}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded, "base rules") || !strings.Contains(loaded, "project rules") {
		t.Fatalf("instructions not combined: %q", loaded)
	}
}

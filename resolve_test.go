package wrapper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveBinary(t *testing.T) {
	dir := t.TempDir()
	want := writeExecutable(t, dir, "claude", "#!/bin/sh\n")
	env := []string{"PATH=" + dir}
	got, err := ResolveBinary("claude", env)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestResolveBinaryMissing(t *testing.T) {
	env := []string{"PATH=" + t.TempDir()}
	_, err := ResolveBinary("claude", env)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestResolveBinarySkipsSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine test binary path")
	}
	selfDir := t.TempDir()
	// The wrapper installed on PATH under the agent's name: a symlink to
	// this test binary. It must be skipped.
	if err := os.Symlink(self, filepath.Join(selfDir, "claude")); err != nil {
		t.Fatal(err)
	}
	realDir := t.TempDir()
	want := writeExecutable(t, realDir, "claude", "#!/bin/sh\n")

	got, err := ResolveBinary("claude", []string{"PATH=" + selfDir + string(filepath.ListSeparator) + realDir})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s, want %s (wrapper must not resolve to itself)", got, want)
	}
}

func TestResolveBinaryDirectPath(t *testing.T) {
	dir := t.TempDir()
	want := writeExecutable(t, dir, "claude", "#!/bin/sh\n")
	got, err := ResolveBinary(want, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

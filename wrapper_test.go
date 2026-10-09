package wrapper_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/claude"
	"github.com/wejick/coding-agent-wrapper/pack"
)

func registerAdapter(t *testing.T) {
	t.Helper()
	if _, err := wrapper.Lookup("claude"); err == nil {
		return // already registered by an earlier test in this binary
	}
	wrapper.Register(claude.New())
}

func fakePath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestPrepareEndToEnd(t *testing.T) {
	registerAdapter(t)
	fakePath(t)

	launch, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "claude",
		Args:  []string{"--version"},
		Pack:  pack.Local("examples/pack"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Agent != "claude" {
		t.Fatalf("agent = %q", launch.Agent)
	}
	if !strings.HasSuffix(launch.Binary, "/claude") {
		t.Fatalf("binary = %q", launch.Binary)
	}
	if launch.Args[0] != "--settings" {
		t.Fatalf("args[0] = %q, want --settings", launch.Args[0])
	}
	if launch.Args[len(launch.Args)-1] != "--version" {
		t.Fatalf("user args must be last, got %v", launch.Args)
	}
	if !strings.Contains(launch.Source, "local:") {
		t.Fatalf("source = %q", launch.Source)
	}
	if launch.PackVersion["name"] != "acme-defaults" {
		t.Fatalf("pack version = %v", launch.PackVersion)
	}
}

type fetchFails struct{}

func (fetchFails) Fetch(context.Context, pack.FetchOptions) (*pack.FetchResult, error) {
	return nil, errors.New("fetch called")
}

func (fetchFails) Describe() string { return "git:example.com/acme/defaults" }

func TestPrepareUsesFetchedPack(t *testing.T) {
	registerAdapter(t)
	fakePath(t)

	note := "refresh failed (offline); using cached pack"
	launch, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent:   "claude",
		Pack:    fetchFails{},
		Fetched: &pack.FetchResult{Dir: "examples/pack", From: "cache", Notes: []string{note}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launch.Source != "git:example.com/acme/defaults (cache)" {
		t.Fatalf("source = %q", launch.Source)
	}
	if !slices.Contains(launch.Notes, note) {
		t.Fatalf("notes should carry the fetch's notes, got %v", launch.Notes)
	}
	if launch.PackVersion["name"] != "acme-defaults" {
		t.Fatalf("pack version = %v", launch.PackVersion)
	}
}

func TestPrepareNotesRefreshIgnoredWithFetchedPack(t *testing.T) {
	registerAdapter(t)
	fakePath(t)

	fetched := &pack.FetchResult{Dir: "examples/pack", From: "cache"}
	launch, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent:   "claude",
		Pack:    fetchFails{},
		Fetched: fetched,
		Refresh: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(launch.Notes, "refresh not attempted: the caller passed an already-fetched pack") {
		t.Fatalf("notes should say the refresh was skipped, got %v", launch.Notes)
	}
	if len(fetched.Notes) != 0 {
		t.Fatalf("Prepare must not modify the caller's fetch result, got %v", fetched.Notes)
	}
}

func TestPrepareUnknownAgent(t *testing.T) {
	registerAdapter(t)
	if _, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "nope",
		Pack:  pack.Local("examples/pack"),
	}); err == nil || !strings.Contains(err.Error(), "no adapter") {
		t.Fatalf("expected unknown-adapter error, got %v", err)
	}
}

func TestPrepareRequiresPack(t *testing.T) {
	registerAdapter(t)
	if _, err := wrapper.Prepare(context.Background(), wrapper.Options{Agent: "claude"}); err == nil {
		t.Fatal("expected missing-pack error")
	}
}

func TestPrepareFailsWhenBinaryMissing(t *testing.T) {
	registerAdapter(t)
	t.Setenv("PATH", t.TempDir()) // no claude anywhere
	if _, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "claude",
		Pack:  pack.Local("examples/pack"),
	}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected binary-not-found error, got %v", err)
	}
}

// toolsPack writes a pack whose tools.json holds content.
func toolsPack(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrepareWarnsAboutRequiredTools(t *testing.T) {
	registerAdapter(t)
	fakePath(t)
	bin := filepath.SplitList(os.Getenv("PATH"))[0]
	if err := os.WriteFile(filepath.Join(bin, "stale"), []byte("#!/bin/sh\necho 1.8.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An npm whose global bin directory comes first on PATH, so a planned
	// install would replace the stale copy.
	prefix := t.TempDir()
	npm := "#!/bin/sh\n[ \"$1 $2\" = \"prefix -g\" ] && echo " + prefix + "\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(prefix, "bin")+string(filepath.ListSeparator)+bin)
	dir := toolsPack(t, `{
		"stale":  {"min_version": "1.10.0", "install": {"manager": "npm", "package": "stale"}},
		"absent": {"min_version": "2.0.0"}
	}`)

	launch, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent:        "claude",
		Pack:         pack.Local(dir),
		SetupCommand: "acme init",
	})
	if err != nil {
		t.Fatalf("a tool problem must not block the launch: %v", err)
	}
	want := []string{
		"absent is not installed. Install absent 2.0.0 or newer.",
		"stale 1.8.0 is older than the required 1.10.0. Run `acme init`.",
	}
	if !slices.Equal(launch.Warnings, want) {
		t.Fatalf("warnings = %q, want %q", launch.Warnings, want)
	}
	for _, w := range want {
		if !slices.Contains(launch.Notes, w) {
			t.Fatalf("notes should carry the warning %q, got %v", w, launch.Notes)
		}
	}

	// When the install directory comes after the stale copy on PATH, the
	// setup command would refuse, so the warning says why instead.
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+filepath.Join(prefix, "bin"))
	launch, err = wrapper.Prepare(context.Background(), wrapper.Options{
		Agent:        "claude",
		Pack:         pack.Local(dir),
		SetupCommand: "acme init",
	})
	if err != nil {
		t.Fatal(err)
	}
	refused := "stale 1.8.0 is older than the required 1.10.0. `acme init` cannot install it: stale at " +
		filepath.Join(bin, "stale") + " comes before " + filepath.Join(prefix, "bin") + " on PATH"
	if w := launch.Warnings[1]; !strings.HasPrefix(w, refused) {
		t.Fatalf("refused install warning = %q, want prefix %q", w, refused)
	}

	// Without a setup command, every warning says what to install.
	launch, err = wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "claude",
		Pack:  pack.Local(dir),
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := launch.Warnings[1]; w != "stale 1.8.0 is older than the required 1.10.0. Install stale 1.10.0 or newer." {
		t.Fatalf("warning without SetupCommand = %q", w)
	}

	launch, err = wrapper.Prepare(context.Background(), wrapper.Options{
		Agent:     "claude",
		Pack:      pack.Local(dir),
		SkipTools: true,
	})
	if err != nil || len(launch.Warnings) != 0 {
		t.Fatalf("SkipTools: warnings = %v, err = %v", launch.Warnings, err)
	}
}

func TestPrepareMalformedToolsFileIsAWarning(t *testing.T) {
	registerAdapter(t)
	fakePath(t)
	launch, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "claude",
		Pack:  pack.Local(toolsPack(t, `{"openspec": {"min_version": "v1.10"}}`)),
	})
	if err != nil {
		t.Fatalf("a malformed tools.json must not block the launch: %v", err)
	}
	if len(launch.Warnings) != 1 || !strings.HasPrefix(launch.Warnings[0], "required tools were not checked: tools.json: openspec: min_version") {
		t.Fatalf("warnings = %q", launch.Warnings)
	}
}

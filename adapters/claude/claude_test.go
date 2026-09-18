package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/claude"
	"github.com/wejick/coding-agent-wrapper/pack"
)

// fakeBinary puts an executable "claude" on PATH and returns nothing: the
// adapter must resolve it and never recurse into a wrapper.
func fakeBinary(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func homeWithUserSettings(t *testing.T, settings string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func testPack(t *testing.T) *pack.Pack {
	t.Helper()
	p, err := pack.Load("../../examples/pack", "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func argValue(t *testing.T, args []string, flag string) (string, bool) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func readMerged(t *testing.T, launch *wrapper.Launch) map[string]any {
	t.Helper()
	if len(launch.Files) == 0 {
		t.Fatal("expected a generated settings file")
	}
	data, err := os.ReadFile(launch.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatal(err)
	}
	return merged
}

func TestBuildFullPackLayering(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{"model":"claude-opus-4-1","permissions":{"allow":["Bash(echo:*)"]}}`)
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{Args: []string{"--model", "sonnet"}})
	if err != nil {
		t.Fatal(err)
	}

	// Settings: org defaults under user settings, user wins per key.
	if launch.Args[0] != "--settings" {
		t.Fatalf("first arg = %q, want --settings", launch.Args[0])
	}
	merged := readMerged(t, launch)
	if merged["model"] != "claude-opus-4-1" {
		t.Fatalf("user setting model should win, got %v", merged["model"])
	}
	if merged["includeCoAuthoredBy"] != false {
		t.Fatalf("org default includeCoAuthoredBy should survive, got %v", merged["includeCoAuthoredBy"])
	}
	perms := merged["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if len(allow) != 4 {
		t.Fatalf("permission lists should union (org 3 + user 1), got %v", allow)
	}
	env := merged["env"].(map[string]any)
	if env["BASH_DEFAULT_TIMEOUT_MS"] != "120000" {
		t.Fatalf("org env default should survive, got %v", env)
	}

	// MCP: additive by default.
	if _, ok := argValue(t, launch.Args, "--mcp-config"); !ok {
		t.Fatal("expected --mcp-config")
	}
	if _, ok := argValue(t, launch.Args, "--strict-mcp-config"); ok {
		t.Fatal("--strict-mcp-config must be absent unless StrictMCP is set")
	}

	// Plugin and system prompt.
	pluginDir, ok := argValue(t, launch.Args, "--plugin-dir")
	if !ok {
		t.Fatal("expected --plugin-dir")
	}
	if info, err := os.Stat(pluginDir); err != nil || !info.IsDir() {
		t.Fatalf("plugin dir missing: %s", pluginDir)
	}
	promptPath, ok := argValue(t, launch.Args, "--append-system-prompt-file")
	if !ok {
		t.Fatal("expected --append-system-prompt-file")
	}
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("system prompt file missing: %s", promptPath)
	}

	// User args go last and still apply.
	n := len(launch.Args)
	if launch.Args[n-2] != "--model" || launch.Args[n-1] != "sonnet" {
		t.Fatalf("user args must be last, got %v", launch.Args)
	}

	// Environment: org defaults injected.
	found := false
	for _, kv := range launch.Env {
		if kv == "DISABLE_TELEMETRY=1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected DISABLE_TELEMETRY=1 injected, got %v", launch.Env)
	}
}

func TestBuildProjectLayering(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{"model":"user-model","permissions":{"allow":["Bash(echo:*)"]}}`)
	proj := t.TempDir()
	claudeDir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"model":"project-model","permissions":{"allow":["Bash(npm run *)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"),
		[]byte(`{"model":"local-model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude.New()
	a.CacheDir = t.TempDir()
	a.ProjectDir = proj

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}

	merged := readMerged(t, launch)
	// Native Claude precedence preserved inside the merged file:
	// local project > project > user > org defaults.
	if merged["model"] != "local-model" {
		t.Fatalf("local project settings should win, got %v", merged["model"])
	}
	perms := merged["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	// org 3 + user 1 + project 1, unioned.
	if len(allow) != 5 {
		t.Fatalf("expected 5 unioned allow entries across layers, got %v", allow)
	}
	if merged["includeCoAuthoredBy"] != false {
		t.Fatalf("org default should survive beneath every layer, got %v", merged["includeCoAuthoredBy"])
	}
	sawLayers := false
	for _, note := range launch.Notes {
		if strings.Contains(note, "settings layers (low → high)") && strings.Contains(note, "local project") {
			sawLayers = true
		}
	}
	if !sawLayers {
		t.Fatalf("expected a settings-layers note, got %v", launch.Notes)
	}
}

func TestBuildMergedSettingsAreContentAddressed(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{"model":"user-model"}`)
	a := claude.New()
	a.CacheDir = t.TempDir()

	pack := testPack(t)
	first, err := a.Build(context.Background(), pack, wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Build(context.Background(), pack, wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Files[0] != second.Files[0] {
		t.Fatalf("identical merges must reuse one file: %s vs %s", first.Files[0], second.Files[0])
	}

	homeWithUserSettings(t, `{"model":"other-user-model"}`)
	third, err := a.Build(context.Background(), pack, wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.Files[0] == first.Files[0] {
		t.Fatal("different merges must not share a file (concurrent-launch race)")
	}
	for _, f := range []string{first.Files[0], third.Files[0]} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("merged file should exist: %s: %v", f, err)
		}
	}
}

func TestBuildPrunesOldMergedSettings(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{}`)
	a := claude.New()
	a.CacheDir = t.TempDir()

	old := filepath.Join(a.CacheDir, "settings-deadbeefdead.json")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(a.CacheDir, "settings-cafebabecafe.json")
	if err := os.WriteFile(recent, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(a.CacheDir, "settings.json")
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacy, past, past); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{old, legacy} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale merged file should be pruned, %s, err=%v", stale, err)
		}
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent merged file should be kept: %v", err)
	}
}

func TestBuildAppliesPolicyByDefault(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{}`)
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{
		Env: []string{"DISABLE_TELEMETRY=custom", "PATH=/usr/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	merged := readMerged(t, launch)
	perms := merged["permissions"].(map[string]any)
	deny := perms["deny"].([]any)
	if len(deny) != 3 {
		t.Fatalf("policy deny entries should apply by default, got %v", deny)
	}

	var envValue string
	for _, kv := range launch.Env {
		if strings.HasPrefix(kv, "DISABLE_TELEMETRY=") {
			envValue = strings.TrimPrefix(kv, "DISABLE_TELEMETRY=")
		}
	}
	if envValue != "1" {
		t.Fatalf("policy mode must force env defaults, got DISABLE_TELEMETRY=%s", envValue)
	}
}

func TestBuildSkipPolicySkipsPolicy(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{"model":"opus"}`)
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{
		SkipPolicy: true,
		Env:        []string{"DISABLE_TELEMETRY=custom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	merged := readMerged(t, launch)
	perms, ok := merged["permissions"].(map[string]any)
	if ok {
		if _, has := perms["deny"]; has {
			t.Fatal("policy must not apply with SkipPolicy")
		}
	}
	sawPolicyNote := false
	for _, note := range launch.Notes {
		if strings.Contains(note, "policy") && strings.Contains(note, "skipped") {
			sawPolicyNote = true
		}
	}
	if !sawPolicyNote {
		t.Fatalf("expected a note about the skipped policy layer, got %v", launch.Notes)
	}
	var envValue string
	for _, kv := range launch.Env {
		if strings.HasPrefix(kv, "DISABLE_TELEMETRY=") {
			envValue = strings.TrimPrefix(kv, "DISABLE_TELEMETRY=")
		}
	}
	if envValue != "custom" {
		t.Fatalf("with policy skipped the environment wins, got DISABLE_TELEMETRY=%s", envValue)
	}
}

func TestBuildStrictMCP(t *testing.T) {
	fakeBinary(t)
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{StrictMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := argValue(t, launch.Args, "--strict-mcp-config"); !ok {
		t.Fatal("expected --strict-mcp-config when StrictMCP is set")
	}
}

func TestBuildEmptyPackIsVanilla(t *testing.T) {
	fakeBinary(t)
	empty := t.TempDir()
	p, err := pack.Load(empty, "test")
	if err != nil {
		t.Fatal(err)
	}
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), p, wrapper.BuildOptions{Args: []string{"--version"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.Args) != 1 || launch.Args[0] != "--version" {
		t.Fatalf("vanilla passthrough expected, got %v", launch.Args)
	}
	if len(launch.Files) != 0 {
		t.Fatalf("no files expected, got %v", launch.Files)
	}
	sawVanilla := false
	for _, note := range launch.Notes {
		if strings.Contains(note, "vanilla") {
			sawVanilla = true
		}
	}
	if !sawVanilla {
		t.Fatalf("expected a vanilla-passthrough note, got %v", launch.Notes)
	}
}

func TestBuildPolicyOnlyPack(t *testing.T) {
	fakeBinary(t)
	homeWithUserSettings(t, `{"model":"opus"}`)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude", "policy.json"), []byte(`{"permissions":{"deny":["Read(./.env)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := pack.Load(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), p, wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged := readMerged(t, launch)
	if merged["model"] != "opus" {
		t.Fatalf("user settings should survive a policy-only pack, got %v", merged)
	}
	perms := merged["permissions"].(map[string]any)
	if len(perms["deny"].([]any)) != 1 {
		t.Fatalf("policy deny should apply, got %v", perms)
	}
}

func TestBuildWithoutUserSettings(t *testing.T) {
	fakeBinary(t)
	t.Setenv("HOME", t.TempDir()) // no ~/.claude
	a := claude.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged := readMerged(t, launch)
	if merged["includeCoAuthoredBy"] != false {
		t.Fatalf("org settings should apply as-is, got %v", merged)
	}
}

func TestLocateFailsWithoutBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // empty PATH
	if _, err := claude.New().Locate(); err == nil {
		t.Fatal("expected resolution failure on empty PATH")
	}
}

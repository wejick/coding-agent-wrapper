package opencode_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/opencode"
	"github.com/wejick/coding-agent-wrapper/pack"
)

// fakeBinary puts an executable "opencode" on PATH: the adapter must
// resolve it and never recurse into a wrapper.
func fakeBinary(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func testPack(t *testing.T) *pack.Pack {
	t.Helper()
	p, err := pack.Load("../../examples/pack", "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func emptyPack(t *testing.T) *pack.Pack {
	t.Helper()
	p, err := pack.Load(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func envValue(t *testing.T, env []string, key string) (string, bool) {
	t.Helper()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func readGenerated(t *testing.T, launch *wrapper.Launch) map[string]any {
	t.Helper()
	if len(launch.Files) == 0 {
		t.Fatal("expected a generated config file")
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
	a := opencode.New()
	a.UserConfigPath = write(t, `{
		"model": "anthropic/claude-opus-4-5",
		"mcp": {"user-server": {"type": "remote", "url": "https://user.example.test"}}
	}`)
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{
		Args: []string{"--model", "sonnet"},
		Env:  []string{"PATH=/usr/bin", "HOME=/nonexistent"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// No argv injection: user args pass through untouched.
	if strings.Join(launch.Args, " ") != "--model sonnet" {
		t.Fatalf("args = %v, want the user's args only", launch.Args)
	}

	// Org defaults under the user's global config; user wins per key.
	merged := readGenerated(t, launch)
	if merged["model"] != "anthropic/claude-opus-4-5" {
		t.Fatalf("user global model should win, got %v", merged["model"])
	}
	if merged["share"] != "disabled" {
		t.Fatalf("org default share should survive, got %v", merged["share"])
	}
	mcp := merged["mcp"].(map[string]any)
	if _, ok := mcp["org-handbook"]; !ok {
		t.Fatalf("org MCP server should be present, got %v", mcp)
	}
	if _, ok := mcp["user-server"]; !ok {
		t.Fatalf("user MCP server should survive, got %v", mcp)
	}

	// Injection happens through the environment.
	if path, _ := envValue(t, launch.Env, opencode.EnvConfig); path != launch.Files[0] {
		t.Fatalf("%s = %q, want the generated file %q", opencode.EnvConfig, path, launch.Files[0])
	}
	dir, _ := envValue(t, launch.Env, opencode.EnvConfigDir)
	wantDir := filepath.Join("../../examples/pack", "opencode", "config")
	if dir != wantDir {
		t.Fatalf("%s = %q, want %q", opencode.EnvConfigDir, dir, wantDir)
	}
	content, ok := envValue(t, launch.Env, opencode.EnvConfigContent)
	if !ok {
		t.Fatal("expected OPENCODE_CONFIG_CONTENT with the policy layer")
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(content), &policy); err != nil {
		t.Fatalf("policy content should be JSON: %v\n%s", err, content)
	}
	bash := policy["permission"].(map[string]any)["bash"].(map[string]any)
	if bash["rm -rf *"] != "deny" {
		t.Fatalf("policy permission should be locked in, got %v", bash)
	}
	if v, _ := envValue(t, launch.Env, "DISABLE_TELEMETRY"); v != "1" {
		t.Fatalf("env defaults should be injected, got DISABLE_TELEMETRY=%q", v)
	}

	sawLayers, sawPolicy := false, false
	for _, note := range launch.Notes {
		if strings.Contains(note, "settings layers (low → high)") && strings.Contains(note, "org defaults < user global") {
			sawLayers = true
		}
		if strings.Contains(note, "applied above project config") {
			sawPolicy = true
		}
	}
	if !sawLayers || !sawPolicy {
		t.Fatalf("missing layering or policy notes, got %v", launch.Notes)
	}
}

func TestBuildSkipPolicyKeepsSoftDefaultsOnly(t *testing.T) {
	fakeBinary(t)
	a := opencode.New()
	a.UserConfigPath = write(t, `{}`)
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{
		SkipPolicy: true,
		Env:        []string{"DISABLE_TELEMETRY=custom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envValue(t, launch.Env, opencode.EnvConfigContent); ok {
		t.Fatal("policy must not be injected with SkipPolicy")
	}
	if v, _ := envValue(t, launch.Env, "DISABLE_TELEMETRY"); v != "custom" {
		t.Fatalf("with policy skipped the environment wins, got DISABLE_TELEMETRY=%q", v)
	}
	sawNote := false
	for _, note := range launch.Notes {
		if strings.Contains(note, "policy") && strings.Contains(note, "skipped") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatalf("expected a note about the skipped policy layer, got %v", launch.Notes)
	}
}

func TestBuildReplacesInheritedCustomConfig(t *testing.T) {
	fakeBinary(t)
	custom := write(t, `{"share": "auto", "customMarker": true}`)
	a := opencode.New()
	a.UserConfigPath = write(t, `{"model": "user-model"}`)
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{
		Env: []string{opencode.EnvConfig + "=" + custom},
	})
	if err != nil {
		t.Fatal(err)
	}
	merged := readGenerated(t, launch)
	if merged["customMarker"] != true {
		t.Fatalf("the user's own OPENCODE_CONFIG should be folded in, got %v", merged)
	}
	if merged["share"] != "auto" {
		t.Fatalf("the user's custom config should beat org defaults, got %v", merged["share"])
	}
	if merged["model"] != "user-model" {
		t.Fatalf("global config should beat org defaults, got %v", merged["model"])
	}
	sawNote := false
	for _, note := range launch.Notes {
		if strings.Contains(note, "replaced inherited") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Fatalf("expected a note about the replaced OPENCODE_CONFIG, got %v", launch.Notes)
	}
}

func TestBuildNotesCorruptUserConfig(t *testing.T) {
	fakeBinary(t)
	a := opencode.New()
	a.UserConfigPath = write(t, `{"model":`)
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged := readGenerated(t, launch)
	if merged["share"] != "disabled" {
		t.Fatalf("org defaults should still apply, got %v", merged)
	}
	for _, note := range launch.Notes {
		if strings.Contains(note, "could not be parsed") && strings.Contains(note, "org defaults may override it") {
			return
		}
	}
	t.Fatalf("expected a note about the unreadable global config, got %v", launch.Notes)
}

func TestBuildReadsJSONCGlobalConfig(t *testing.T) {
	fakeBinary(t)
	a := opencode.New()
	a.UserConfigPath = write(t, "{\n  // user preference\n  \"model\": \"user-model\",\n}")
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged := readGenerated(t, launch)
	if merged["model"] != "user-model" {
		t.Fatalf("jsonc user config should win, got %v", merged)
	}
}

func TestBuildResolvesGlobalConfigFromXDG(t *testing.T) {
	fakeBinary(t)
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	dir := filepath.Join(xdg, "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"model":"user-model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	a := opencode.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), testPack(t), wrapper.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged := readGenerated(t, launch)
	if merged["model"] != "user-model" {
		t.Fatalf("config under XDG_CONFIG_HOME should be layered in, got %v", merged)
	}
}

func TestBuildEmptyPackIsVanilla(t *testing.T) {
	fakeBinary(t)
	a := opencode.New()
	a.CacheDir = t.TempDir()

	launch, err := a.Build(context.Background(), emptyPack(t), wrapper.BuildOptions{
		Env: []string{"KEEP=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{opencode.EnvConfig, opencode.EnvConfigDir, opencode.EnvConfigContent} {
		if _, ok := envValue(t, launch.Env, key); ok {
			t.Fatalf("%s must not be set for an empty pack", key)
		}
	}
	if len(launch.Files) != 0 {
		t.Fatalf("no files should be generated, got %v", launch.Files)
	}
	if v, _ := envValue(t, launch.Env, "KEEP"); v != "1" {
		t.Fatalf("environment should pass through, got %v", launch.Env)
	}
	if len(launch.Notes) == 0 || !strings.Contains(launch.Notes[0], "vanilla") {
		t.Fatalf("expected a vanilla note, got %v", launch.Notes)
	}
}

func TestLocateFailsWithoutBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // empty PATH
	if _, err := opencode.New().Locate(); err == nil {
		t.Fatal("expected resolution failure on empty PATH")
	}
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

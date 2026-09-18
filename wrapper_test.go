package wrapper_test

import (
	"context"
	"os"
	"path/filepath"
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
		Pack:  pack.Local("testdata/pack"),
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

func TestPrepareUnknownAgent(t *testing.T) {
	registerAdapter(t)
	if _, err := wrapper.Prepare(context.Background(), wrapper.Options{
		Agent: "nope",
		Pack:  pack.Local("testdata/pack"),
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
		Pack:  pack.Local("testdata/pack"),
	}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected binary-not-found error, got %v", err)
	}
}

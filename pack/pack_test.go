package pack

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMissingDir(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope"), "test"); err == nil {
		t.Fatal("expected error for missing pack dir")
	}
}

func TestLoadExamplePack(t *testing.T) {
	p, err := Load("../testdata/pack", "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.ShortVersion() != "acme-defaults 0.1.0" {
		t.Fatalf("unexpected version: %s", p.ShortVersion())
	}
	var settings map[string]any
	ok, err := p.JSONFile("claude", "settings.json", &settings)
	if err != nil || !ok {
		t.Fatalf("settings.json: ok=%v err=%v", ok, err)
	}
	perms := settings["permissions"].(map[string]any)
	if len(perms["allow"].([]any)) != 3 {
		t.Fatalf("expected 3 allow entries, got %v", perms["allow"])
	}
	exists, err := p.JSONFile("claude", "missing.json", &settings)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing.json should not exist")
	}
	if _, ok := p.Subdir("claude", "plugin"); !ok {
		t.Fatal("plugin dir should exist")
	}
	if _, ok := p.Subdir("claude", "nope"); ok {
		t.Fatal("nope dir should not exist")
	}
	if _, ok := p.File("claude", "plugin"); ok {
		t.Fatal("plugin is a directory, not a file")
	}
}

func TestLocalSource(t *testing.T) {
	dir := t.TempDir()
	res, err := Local(dir).Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "local" || res.Dir != dir {
		t.Fatalf("got %+v", res)
	}
}

func TestLoadUnversionedPack(t *testing.T) {
	p, err := Load(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.ShortVersion() != "(unversioned)" {
		t.Fatalf("got %s", p.ShortVersion())
	}
	if p.Version != nil {
		t.Fatalf("expected nil version, got %v", p.Version)
	}
	if !reflect.DeepEqual(p.Notes, []string(nil)) {
		t.Fatalf("expected no notes, got %v", p.Notes)
	}
}

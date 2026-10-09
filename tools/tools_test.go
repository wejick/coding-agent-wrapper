package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileRequiresNothing(t *testing.T) {
	reqs, err := Load(t.TempDir())
	if err != nil || reqs != nil {
		t.Fatalf("Load = %v, %v; want nil, nil", reqs, err)
	}
}

func TestLoadExamplePack(t *testing.T) {
	reqs, err := Load("../examples/pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Name != "node" || reqs[1].Name != "openspec" {
		t.Fatalf("reqs = %+v", reqs)
	}
	if reqs[0].Install != nil {
		t.Fatalf("node is check-only, got install %+v", reqs[0].Install)
	}
	if in := reqs[1].Install; in == nil || in.Manager != "npm" || in.Package != "@fission-ai/openspec" {
		t.Fatalf("openspec install = %+v", in)
	}
}

func TestParse(t *testing.T) {
	reqs, err := Parse([]byte(`{
		"b": {"min_version": "1.2.3", "future_field": true},
		"a": {"command": "a-cli", "min_version": "0.0.1",
		      "install": {"manager": "go", "package": "example.com/a/cmd/a-cli", "extra": 1}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Name != "a" || reqs[1].Name != "b" {
		t.Fatalf("reqs must be sorted by name, got %+v", reqs)
	}
	if reqs[0].Command != "a-cli" || reqs[1].Command != "b" {
		t.Fatalf("command defaults to the key: %+v", reqs)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"not json":            `{`,
		"not an object":       `["openspec"]`,
		"entry not an object": `{"x": "1.0.0"}`,
		"missing version":     `{"x": {}}`,
		"prefix v":            `{"x": {"min_version": "v1.0.0"}}`,
		"two parts":           `{"x": {"min_version": "1.10"}}`,
		"range":               `{"x": {"min_version": ">=1.0.0"}}`,
		"prerelease":          `{"x": {"min_version": "1.0.0-rc.1"}}`,
		"leading zero":        `{"x": {"min_version": "01.0.0"}}`,
		"command path":        `{"x": {"command": "/usr/bin/x", "min_version": "1.0.0"}}`,
		"command flag":        `{"x": {"command": "-x", "min_version": "1.0.0"}}`,
		"no manager":          `{"x": {"min_version": "1.0.0", "install": {"package": "x"}}}`,
		"no package":          `{"x": {"min_version": "1.0.0", "install": {"manager": "npm"}}}`,
		"package flag":        `{"x": {"min_version": "1.0.0", "install": {"manager": "npm", "package": "--registry=evil"}}}`,
		"package space":       `{"x": {"min_version": "1.0.0", "install": {"manager": "npm", "package": "a b"}}}`,
		"package version":     `{"x": {"min_version": "1.0.0", "install": {"manager": "npm", "package": "@scope/x@2.0.0"}}}`,
	}
	for name, input := range cases {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("%s: Parse(%s) should fail", name, input)
		}
	}
}

func TestParseKeepsUnknownManagerForPlan(t *testing.T) {
	// A newer pack may name a manager this wrapper does not know; the
	// tool is still checked, and Plan reports it.
	reqs, err := Parse([]byte(`{"x": {"min_version": "1.0.0", "install": {"manager": "uv", "package": "x"}}}`))
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Parse = %v, %v", reqs, err)
	}
}

func TestLoadReportsPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"x": {"min_version": "1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "tools.json: x: min_version") {
		t.Fatalf("err = %v", err)
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{
		{"1.10.0", "1.10.0", true},
		{"1.10.1", "1.10.0", true},
		{"1.9.9", "1.10.0", false},
		{"2.0.0", "1.10.0", true},
		{"1.10.0", "1.9.0", true},
		{"0.0.9", "0.1.0", false},
		{"garbage", "1.0.0", false},
		{"1.10.0-rc.1", "1.10.0", false},
		{"1.10.1-rc.1", "1.10.0", true},
	}
	for _, c := range cases {
		if got := atLeast(c.have, c.min); got != c.want {
			t.Errorf("atLeast(%s, %s) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestFindVersion(t *testing.T) {
	cases := map[string]string{
		"openspec 1.10.0\n":                "1.10.0",
		"v22.22.0":                         "22.22.0",
		"go version go1.27.0 linux/amd64":  "",
		"mytool dev (built with go1.22.3)": "",
		"tool v1.2.3-rc.1 linux":           "1.2.3-rc.1",
		"tool-1.2.3":                       "1.2.3",
		"tool 2.3.4 (built with 1.2.3)":    "2.3.4",
		"no version here":                  "",
	}
	for out, want := range cases {
		got, _ := findVersion(out)
		if got != want {
			t.Errorf("findVersion(%q) = %q, want %q", out, got, want)
		}
	}
}

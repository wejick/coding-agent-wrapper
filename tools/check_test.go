package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTool writes an executable shell script named name into dir.
func fakeTool(t *testing.T, dir, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func pathEnv(dirs ...string) []string {
	return []string{"PATH=" + strings.Join(dirs, string(filepath.ListSeparator))}
}

func req(name, min string) Requirement {
	return Requirement{Name: name, Command: name, MinVersion: min}
}

func TestCheckStates(t *testing.T) {
	bin := t.TempDir()
	okPath := fakeTool(t, bin, "fresh", `echo "fresh 1.10.0"`)
	oldPath := fakeTool(t, bin, "stale", `echo "stale v1.8.0"`)
	silentPath := fakeTool(t, bin, "silent", `echo "usage: silent"; exit 2`)
	stderrPath := fakeTool(t, bin, "loud", `echo "loud 3.0.0" >&2`)

	got := Check(context.Background(), []Requirement{
		req("fresh", "1.10.0"),
		req("stale", "1.10.0"),
		req("silent", "1.0.0"),
		req("loud", "2.0.0"),
		req("absent", "1.0.0"),
	}, CheckOptions{Env: pathEnv(bin)})

	want := []struct {
		state   State
		path    string
		version string
	}{
		{OK, okPath, "1.10.0"},
		{TooOld, oldPath, "1.8.0"},
		{TooOld, silentPath, ""},
		{OK, stderrPath, "3.0.0"},
		{Missing, "", ""},
	}
	for i, w := range want {
		s := got[i]
		if s.State != w.state || s.Path != w.path || s.Version != w.version {
			t.Errorf("%s: got %s %q %q, want %s %q %q", s.Name, s.State, s.Path, s.Version, w.state, w.path, w.version)
		}
	}
	if got[2].Detail != "--version printed no version" {
		t.Errorf("silent detail = %q", got[2].Detail)
	}
}

func TestCheckTimesOut(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "hang", `exec /bin/sleep 30 || exec /usr/bin/sleep 30`)
	start := time.Now()
	got := Check(context.Background(), []Requirement{req("hang", "1.0.0")},
		CheckOptions{Env: pathEnv(bin), Timeout: 200 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Check took %s; a hung --version must not stall it", elapsed)
	}
	if got[0].State != TooOld || !strings.Contains(got[0].Detail, "did not finish within 200ms") {
		t.Fatalf("got %+v", got[0])
	}
}

func TestCheckTimesOutWhenAChildHoldsTheOutput(t *testing.T) {
	bin := t.TempDir()
	// The shell is killed at the timeout, but the sleep it started keeps
	// the output pipe open.
	fakeTool(t, bin, "hold", `/bin/sleep 30; echo 1.0.0`)
	start := time.Now()
	got := Check(context.Background(), []Requirement{req("hold", "1.0.0")},
		CheckOptions{Env: pathEnv(bin), Timeout: 200 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Check took %s; a child holding the pipe must not stall it", elapsed)
	}
	if got[0].State != TooOld {
		t.Fatalf("got %+v", got[0])
	}
}

func TestDescribeAndProblem(t *testing.T) {
	r := req("openspec", "1.10.0")
	cases := []struct {
		s        Status
		describe string
		problem  string
	}{
		{
			Status{Requirement: r, State: Missing},
			"openspec: missing (minimum 1.10.0)",
			"openspec is not installed.",
		},
		{
			Status{Requirement: r, State: TooOld, Path: "/bin/openspec", Version: "1.8.0"},
			"openspec: too old, 1.8.0 at /bin/openspec (minimum 1.10.0)",
			"openspec 1.8.0 is older than the required 1.10.0.",
		},
		{
			Status{Requirement: r, State: TooOld, Path: "/bin/openspec", Detail: "--version printed no version"},
			"openspec: too old, no version reported by /bin/openspec (--version printed no version) (minimum 1.10.0)",
			"openspec at /bin/openspec did not report a version (--version printed no version).",
		},
		{
			Status{Requirement: r, State: OK, Path: "/bin/openspec", Version: "1.10.0"},
			"openspec: ok, 1.10.0 at /bin/openspec (minimum 1.10.0)",
			"",
		},
	}
	for _, c := range cases {
		if got := c.s.Describe(); got != c.describe {
			t.Errorf("Describe = %q, want %q", got, c.describe)
		}
		if got := c.s.Problem(); got != c.problem {
			t.Errorf("Problem = %q, want %q", got, c.problem)
		}
	}
}

func TestNewerFindsShadowedCopy(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	fakeTool(t, first, "openspec", `echo 1.8.0`)
	newPath := fakeTool(t, second, "openspec", `echo 1.10.0`)
	o := CheckOptions{Env: pathEnv(first, second)}

	s := Check(context.Background(), []Requirement{req("openspec", "1.10.0")}, o)[0]
	if s.State != TooOld {
		t.Fatalf("first copy should win: %+v", s)
	}
	newer, ok := Newer(context.Background(), s, o)
	if !ok || newer.Path != newPath || newer.Version != "1.10.0" {
		t.Fatalf("Newer = %+v, %v", newer, ok)
	}

	if _, ok := Newer(context.Background(), s, CheckOptions{Env: pathEnv(first)}); ok {
		t.Fatal("no second copy, nothing is shadowed")
	}
}

func TestLookPathAllSkipsDuplicates(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "x", `echo 1.0.0`)
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(bin, "x"), filepath.Join(link, "x")); err != nil {
		t.Fatal(err)
	}
	if got := lookPathAll("x", pathEnv(bin, link, bin)); len(got) != 1 {
		t.Fatalf("lookPathAll = %v, want one entry", got)
	}
}

func TestCheckSkipsRelativePathEntries(t *testing.T) {
	cwd := t.TempDir()
	fakeTool(t, cwd, "proj", `echo 9.9.9`)
	t.Chdir(cwd)
	got := Check(context.Background(), []Requirement{req("proj", "1.0.0")},
		CheckOptions{Env: []string{"PATH=" + string(filepath.ListSeparator) + "." + string(filepath.ListSeparator) + "bin"}})
	if got[0].State != Missing {
		t.Fatalf("a tool in the current directory must not run: %+v", got[0])
	}
}

func TestCheckSkipsTheRunningExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper installed under a tool's name, as with an agent shim.
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, "selftool")); err != nil {
		t.Fatal(err)
	}
	got := Check(context.Background(), []Requirement{req("selftool", "1.0.0")}, CheckOptions{Env: pathEnv(dir)})
	if got[0].State != Missing {
		t.Fatalf("the running executable must never be checked: %+v", got[0])
	}
}

func TestCheckReadsGoModuleVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// A tagged module: go build stamps the tag as the main module version,
	// as go install pkg@v0.20.0 does. The tool has no --version flag.
	src := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/stringer\n\ngo 1.21\n")
	write("main.go", "package main\n\nimport \"os\"\n\nfunc main() { os.Exit(2) }\n")
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GOFLAGS=-buildvcs=true", "GOTOOLCHAIN=local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	run("git", "init", "--quiet")
	run("git", "add", "-A")
	run("git", "commit", "--quiet", "-m", "v0.20.0")
	run("git", "tag", "v0.20.0")
	bin := t.TempDir()
	run(goBin, "build", "-o", filepath.Join(bin, "stringer"), ".")

	r := req("stringer", "0.20.0")
	if got := Check(context.Background(), []Requirement{r}, CheckOptions{Env: pathEnv(bin)})[0]; got.State != TooOld {
		t.Fatalf("without a go install method the version comes from --version only: %+v", got)
	}
	r.Install = &Install{Manager: "go", Package: "example.com/stringer"}
	got := Check(context.Background(), []Requirement{r}, CheckOptions{Env: pathEnv(bin)})[0]
	if got.State != OK || got.Version != "0.20.0" {
		t.Fatalf("a go-installed tool should report its module version: %+v", got)
	}
}

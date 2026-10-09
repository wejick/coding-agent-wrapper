package tools

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installReq(name, manager, pkg string) Requirement {
	r := req(name, "1.10.0")
	r.Install = &Install{Manager: manager, Package: pkg}
	return r
}

func missing(r Requirement) Status { return Status{Requirement: r, State: Missing} }

// fakeNPM writes an npm that reports prefix as its global prefix and, on
// install, writes a tool that prints the installed version.
func fakeNPM(t *testing.T, dir, prefix string) {
	t.Helper()
	fakeTool(t, dir, "npm", `case "$1" in
prefix) echo "`+prefix+`" ;;
install)
  spec="$3"; ver="${spec##*@}"; name="${spec%@*}"; name="${name##*/}"
  mkdir -p "`+prefix+`/bin"
  printf '#!/bin/sh\necho %s\n' "$ver" > "`+prefix+`/bin/$name"
  chmod +x "`+prefix+`/bin/$name" ;;
*) exit 1 ;;
esac`)
}

func TestPlanNPM(t *testing.T) {
	bin, prefix := t.TempDir(), t.TempDir()
	fakeNPM(t, bin, prefix)
	// The fake npm needs mkdir and chmod.
	env := pathEnv(filepath.Join(prefix, "bin"), bin, "/bin", "/usr/bin")

	r := installReq("openspec", "npm", "@fission-ai/openspec")
	steps := Plan(context.Background(), []Status{
		{Requirement: req("fine", "1.0.0"), State: OK},
		missing(r),
	}, PlanOptions{Env: env})
	if len(steps) != 1 {
		t.Fatalf("OK tools are not planned, got %+v", steps)
	}
	st := steps[0]
	if st.Err != nil {
		t.Fatal(st.Err)
	}
	if st.String() != "npm install -g @fission-ai/openspec@1.10.0" || st.Binary != filepath.Join(bin, "npm") {
		t.Fatalf("step = %q via %s", st.String(), st.Binary)
	}

	if err := Run(context.Background(), st, env, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	after := Check(context.Background(), []Requirement{r}, CheckOptions{Env: env})[0]
	if after.State != OK || after.Path != filepath.Join(prefix, "bin", "openspec") {
		t.Fatalf("after install: %+v", after)
	}
}

func TestPlanArgsPerManager(t *testing.T) {
	cases := map[string]string{
		"npm":  "npm install -g pkg@1.10.0",
		"pnpm": "pnpm add -g pkg@1.10.0",
		"bun":  "bun add -g pkg@1.10.0",
		"go":   "go install pkg@v1.10.0",
	}
	for name, want := range cases {
		got := strings.Join(append([]string{name}, managers[name].args("pkg", "1.10.0")...), " ")
		if got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestPlanPNPM(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "pnpm", `exit 0`)
	r := installReq("x", "pnpm", "x")

	steps := Plan(context.Background(), []Status{missing(r)}, PlanOptions{Env: pathEnv(bin)})
	if steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), "PNPM_HOME is not set; run `pnpm setup`") {
		t.Fatalf("without PNPM_HOME: %v", steps[0].Err)
	}

	env := append(pathEnv(bin), "PNPM_HOME="+home)
	steps = Plan(context.Background(), []Status{missing(r)}, PlanOptions{Env: env})
	if steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), "not on PATH") {
		t.Fatalf("PNPM_HOME off PATH: %v", steps[0].Err)
	}

	env = append(pathEnv(bin, home), "PNPM_HOME="+home)
	steps = Plan(context.Background(), []Status{missing(r)}, PlanOptions{Env: env})
	if steps[0].Err != nil || steps[0].String() != "pnpm add -g x@1.10.0" {
		t.Fatalf("usable pnpm: %q %v", steps[0].String(), steps[0].Err)
	}
}

func TestPlanBunUsesHome(t *testing.T) {
	bin, home := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "bun", `exit 0`)
	env := append(pathEnv(bin, filepath.Join(home, ".bun", "bin")), "HOME="+home)
	// ~/.bun does not exist yet: bun creates it, so HOME must be writable.
	steps := Plan(context.Background(), []Status{missing(installReq("x", "bun", "x"))}, PlanOptions{Env: env})
	if steps[0].Err != nil {
		t.Fatal(steps[0].Err)
	}
}

func TestPlanGoUsesGOPATH(t *testing.T) {
	bin, gopath := t.TempDir(), t.TempDir()
	fakeTool(t, bin, "go", `[ "$1 $2 $3" = "env GOBIN GOPATH" ] || exit 1
echo
echo "`+gopath+`"`)
	env := pathEnv(bin, filepath.Join(gopath, "bin"))
	steps := Plan(context.Background(), []Status{missing(installReq("lint", "go", "example.com/lint"))}, PlanOptions{Env: env})
	if steps[0].Err != nil || steps[0].String() != "go install example.com/lint@v1.10.0" {
		t.Fatalf("go step: %q %v", steps[0].String(), steps[0].Err)
	}
}

func TestPlanRefusesUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	bin, prefix := t.TempDir(), t.TempDir()
	fakeNPM(t, bin, prefix)
	if err := os.Mkdir(filepath.Join(prefix, "bin"), 0o555); err != nil {
		t.Fatal(err)
	}
	steps := Plan(context.Background(), []Status{missing(installReq("x", "npm", "x"))},
		PlanOptions{Env: pathEnv(filepath.Join(prefix, "bin"), bin)})
	if steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), "never install with sudo") {
		t.Fatalf("unwritable prefix: %v", steps[0].Err)
	}
}

func TestPlanCannotInstall(t *testing.T) {
	cases := map[string]struct {
		r    Requirement
		want string
	}{
		"check only":      {req("node", "20.0.0"), "the pack has no install method for node; install node 20.0.0 or newer yourself"},
		"unknown manager": {installReq("x", "uv", "x"), `package manager "uv" is not supported by this wrapper`},
		"manager missing": {installReq("x", "npm", "x"), "npm is not installed"},
	}
	for name, c := range cases {
		steps := Plan(context.Background(), []Status{missing(c.r)}, PlanOptions{Env: pathEnv(t.TempDir())})
		if len(steps) != 1 || steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), c.want) {
			t.Errorf("%s: %+v", name, steps)
		}
		if err := Run(context.Background(), steps[0], nil, nil, io.Discard, io.Discard); err == nil {
			t.Errorf("%s: a step with Err must not run", name)
		}
	}
}

func TestPlanRefusesInstallBehindOldCopy(t *testing.T) {
	bin, prefix, old := t.TempDir(), t.TempDir(), t.TempDir()
	fakeNPM(t, bin, prefix)
	oldPath := fakeTool(t, old, "x", `echo 1.8.0`)
	r := installReq("x", "npm", "x")
	stale := Status{Requirement: r, State: TooOld, Path: oldPath, Version: "1.8.0"}

	steps := Plan(context.Background(), []Status{stale}, PlanOptions{Env: pathEnv(old, filepath.Join(prefix, "bin"), bin)})
	if steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), oldPath+" comes before "+filepath.Join(prefix, "bin")+" on PATH") {
		t.Fatalf("an install behind the old copy cannot work: %v", steps[0].Err)
	}

	steps = Plan(context.Background(), []Status{stale}, PlanOptions{Env: pathEnv(filepath.Join(prefix, "bin"), old, bin)})
	if steps[0].Err != nil {
		t.Fatalf("an install ahead of the old copy should be planned: %v", steps[0].Err)
	}
}

func TestPlanRefusesOverUnreadableVersion(t *testing.T) {
	bin, prefix := t.TempDir(), t.TempDir()
	fakeNPM(t, bin, prefix)
	r := installReq("x", "npm", "x")
	unread := Status{Requirement: r, State: TooOld, Path: "/opt/x", Detail: "--version did not finish within 2s"}
	steps := Plan(context.Background(), []Status{unread}, PlanOptions{Env: pathEnv(filepath.Join(prefix, "bin"), bin)})
	if steps[0].Err == nil || !strings.Contains(steps[0].Err.Error(), "could replace a newer copy") {
		t.Fatalf("a copy with an unknown version must not be replaced: %v", steps[0].Err)
	}
}

func TestPlanProbesEachManagerOnce(t *testing.T) {
	bin, prefix := t.TempDir(), t.TempDir()
	log := filepath.Join(t.TempDir(), "probes")
	fakeTool(t, bin, "npm", `echo "$*" >> "`+log+`"; echo "`+prefix+`"`)
	statuses := []Status{
		missing(installReq("a", "npm", "a")),
		missing(installReq("b", "npm", "b")),
		missing(installReq("c", "npm", "c")),
	}
	steps := Plan(context.Background(), statuses, PlanOptions{Env: pathEnv(filepath.Join(prefix, "bin"), bin)})
	for _, st := range steps {
		if st.Err != nil {
			t.Fatal(st.Err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "prefix -g"); got != 1 {
		t.Fatalf("npm prefix -g ran %d times, want once per Plan", got)
	}
}

package wrapper_test

// End-to-end tests: they build the reference CLI binary and run it as a
// black box against a fake agent that records what it received. These tests
// pin the two properties nothing else can: the exec-in-place model (the
// agent is the process users signal) and the guarantee that the wrapper
// never launches itself.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var wrBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wr-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: temp dir:", err)
		os.Exit(1)
	}
	wrBinary = filepath.Join(dir, "wr")
	build := exec.Command("go", "build", "-o", wrBinary, "./cmd/wr")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: building wr: %v: %s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeAgent writes an executable shell script named name that records its
// argv and environment into $RECORD, then exits with $FAKE_EXIT.
func fakeAgent(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := `#!/bin/sh
: > "$RECORD"
printf 'ARGV\n' >> "$RECORD"
for a in "$@"; do printf '%s\n' "$a" >> "$RECORD"; done
printf 'ENV\n' >> "$RECORD"
env >> "$RECORD"
if [ -n "$READY" ]; then printf 'READY\n' > "$READY"; fi
case "$*" in
*"--hang"*)
  trap 'exit 42' TERM
  while :; do sleep 0.2; done
  ;;
esac
exit "${FAKE_EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// upsertEnv returns base with every KEY=VALUE in kvs applied, replacing
// existing entries so the child cannot read stale values.
func upsertEnv(base []string, kvs ...string) []string {
	drop := map[string]bool{}
	for _, kv := range kvs {
		drop[strings.SplitN(kv, "=", 2)[0]] = true
	}
	out := make([]string, 0, len(base)+len(kvs))
	for _, kv := range base {
		if !drop[strings.SplitN(kv, "=", 2)[0]] {
			out = append(out, kv)
		}
	}
	return append(out, kvs...)
}

func controlledHome(t *testing.T, userSettings string) string {
	t.Helper()
	home := t.TempDir()
	if userSettings != "" {
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(userSettings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func packAbs(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("examples/pack")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

type agentRecord struct {
	Args []string
	Env  map[string]string
}

func readRecord(t *testing.T, path string) agentRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("agent record: %v", err)
	}
	rec := agentRecord{Env: map[string]string{}}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		switch line {
		case "ARGV", "ENV":
			section = line
			continue
		case "":
			continue
		}
		if section == "ARGV" {
			rec.Args = append(rec.Args, line)
		} else if section == "ENV" {
			if k, v, ok := strings.Cut(line, "="); ok {
				rec.Env[k] = v
			}
		}
	}
	return rec
}

// runWr runs the built CLI and returns its stdout and exit code.
func runWr(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(wrBinary, args...)
	cmd.Env = env
	stdout := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stdout
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running wr %v: %v\n%s", args, err, stdout.String())
	}
	return stdout.String(), code
}

func TestE2ELaunchAppliesDefaultsAndPassesThrough(t *testing.T) {
	home := controlledHome(t, `{"model":"user-model"}`)
	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"RECORD="+record,
		"FAKE_EXIT=7",
	)
	stdout, code := runWr(t, env, "--pack", packAbs(t), "claude", "--model", "sonnet")

	if code != 7 {
		t.Fatalf("agent exit code should pass through, got %d\n%s", code, stdout)
	}
	rec := readRecord(t, record)

	// Org flags first, user args last.
	if len(rec.Args) < 2 || rec.Args[0] != "--settings" {
		t.Fatalf("first agent arg = %v, want --settings first", rec.Args)
	}
	if rec.Args[len(rec.Args)-2] != "--model" || rec.Args[len(rec.Args)-1] != "sonnet" {
		t.Fatalf("user args must be last, got %v", rec.Args)
	}
	// The merged settings file exists and layers correctly.
	merged, err := os.ReadFile(rec.Args[1])
	if err != nil {
		t.Fatalf("merged settings: %v", err)
	}
	for _, want := range []string{`"model": "user-model"`, `"includeCoAuthoredBy": false`} {
		if !strings.Contains(string(merged), want) {
			t.Fatalf("merged settings missing %s:\n%s", want, merged)
		}
	}
	// Org env defaults are injected.
	if rec.Env["DISABLE_TELEMETRY"] != "1" {
		t.Fatalf("DISABLE_TELEMETRY = %q, want 1", rec.Env["DISABLE_TELEMETRY"])
	}
	if rec.Env["HOME"] != home {
		t.Fatalf("HOME = %q, want the controlled %q", rec.Env["HOME"], home)
	}
}

func TestE2ELaunchOpenCodeInjectsEnvironment(t *testing.T) {
	home := controlledHome(t, "")
	binDir := t.TempDir()
	fakeAgent(t, binDir, "opencode")
	record := filepath.Join(t.TempDir(), "record")

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"RECORD="+record,
	)
	stdout, code := runWr(t, env, "--pack", packAbs(t), "opencode", "run", "hello")
	if code != 0 {
		t.Fatalf("agent exit code should pass through, got %d\n%s", code, stdout)
	}
	rec := readRecord(t, record)

	// Nothing is injected into argv; user args pass through untouched.
	if strings.Join(rec.Args, " ") != "run hello" {
		t.Fatalf("agent args = %v, want the user's args only", rec.Args)
	}
	// The generated config file exists and layers org defaults.
	configPath := rec.Env["OPENCODE_CONFIG"]
	if configPath == "" {
		t.Fatalf("OPENCODE_CONFIG must point at the generated file, env: %v", rec.Env)
	}
	merged, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("generated config: %v", err)
	}
	for _, want := range []string{`"share": "disabled"`, `"org-handbook"`} {
		if !strings.Contains(string(merged), want) {
			t.Fatalf("generated config missing %s:\n%s", want, merged)
		}
	}
	// The policy travels through OPENCODE_CONFIG_CONTENT above project
	// config, and org env defaults are injected.
	if !strings.Contains(rec.Env["OPENCODE_CONFIG_CONTENT"], "deny") {
		t.Fatalf("OPENCODE_CONFIG_CONTENT should carry the policy, got %q", rec.Env["OPENCODE_CONFIG_CONTENT"])
	}
	if rec.Env["DISABLE_TELEMETRY"] != "1" {
		t.Fatalf("DISABLE_TELEMETRY = %q, want 1", rec.Env["DISABLE_TELEMETRY"])
	}
	if rec.Env["HOME"] != home {
		t.Fatalf("HOME = %q, want the controlled %q", rec.Env["HOME"], home)
	}
}

func TestE2ESignalReachesAgentDirectly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UNIX signal semantics")
	}
	home := controlledHome(t, "")
	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")
	ready := filepath.Join(t.TempDir(), "ready")

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"RECORD="+record,
		"READY="+ready,
	)
	cmd := exec.Command(wrBinary, "--pack", packAbs(t), "claude", "--hang")
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never signalled readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()

	// The trap in the fake agent fired: with exec-in-place, wr's PID is the
	// agent's PID, so the signal hit the trap directly.
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if exit != 42 {
		t.Fatalf("agent trap exit = %d, want 42 (signal must reach the agent, not a supervisor)", exit)
	}
	rec := readRecord(t, record)
	found := false
	for _, a := range rec.Args {
		if a == "--hang" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent args missing --hang: %v", rec.Args)
	}
}

func TestE2EWrapperOnPathAsAgentDoesNotRecurse(t *testing.T) {
	home := controlledHome(t, "")
	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")

	// The wrapper installed on PATH under the agent's name, as a symlink to
	// this exact wr binary: resolving "claude" must skip it.
	shadow := filepath.Join(t.TempDir(), "shadow")
	if err := os.MkdirAll(shadow, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(wrBinary, filepath.Join(shadow, "claude")); err != nil {
		t.Fatal(err)
	}

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+shadow+string(filepath.ListSeparator)+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"RECORD="+record,
	)
	stdout, code := runWr(t, env, "--pack", packAbs(t), "claude")

	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	rec := readRecord(t, record)
	if len(rec.Args) == 0 {
		t.Fatal("the fake agent should have run; the wrapper launched itself or nothing")
	}
	for _, a := range rec.Args {
		if a == "--settings" {
			return // the real adapter ran: the fake agent was launched
		}
	}
	t.Fatalf("expected the fake agent to receive org flags, got %v", rec.Args)
}

func TestE2EDoctorJSON(t *testing.T) {
	home := controlledHome(t, "")
	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")
	fakeAgent(t, binDir, "opencode")

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
	)
	stdout, code := runWr(t, env, "--pack", packAbs(t), "doctor", "--json")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	var report struct {
		Agents []struct {
			Name   string `json:"name"`
			Binary string `json:"binary"`
		} `json:"agents"`
		Launch map[string]struct {
			Args []string `json:"args"`
			Env  []string `json:"env_injected"`
		} `json:"launch"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, stdout)
	}
	names := map[string]bool{}
	for _, a := range report.Agents {
		names[a.Name] = true
	}
	if !names["claude"] || !names["opencode"] || len(report.Agents) != 2 {
		t.Fatalf("agents = %+v", report.Agents)
	}
	launch, ok := report.Launch["claude"]
	if !ok || len(launch.Args) == 0 || launch.Args[0] != "--settings" {
		t.Fatalf("launch.claude = %+v", launch)
	}
	oconfig, ok := report.Launch["opencode"]
	if !ok {
		t.Fatalf("launch.opencode missing")
	}
	hasConfig := false
	for _, kv := range oconfig.Env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			hasConfig = true
		}
	}
	if !hasConfig {
		t.Fatalf("launch.opencode should inject OPENCODE_CONFIG, got %+v", oconfig)
	}
}

func TestE2EDoctorWithoutPackIsASetupCheck(t *testing.T) {
	home := controlledHome(t, "")
	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")

	env := upsertEnv(os.Environ(),
		"HOME="+home,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
	)
	stdout, code := runWr(t, env, "doctor")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "agents:") || !strings.Contains(stdout, "claude") {
		t.Fatalf("doctor should verify binaries without a pack:\n%s", stdout)
	}
}

func TestE2ESyncFromLocalGitOrigin(t *testing.T) {
	home := controlledHome(t, "")
	env := upsertEnv(os.Environ(), "HOME="+home)

	origin := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "--quiet")
	if err := filepath.WalkDir("examples/pack", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel("examples/pack", path)
		if err != nil {
			return err
		}
		dst := filepath.Join(origin, "pack", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "pack v1")

	stdout, code := runWr(t, env, "--pack", "file://"+origin, "sync")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "from:   git") || !strings.Contains(stdout, "head:") {
		t.Fatalf("sync should report source and head:\n%s", stdout)
	}
}

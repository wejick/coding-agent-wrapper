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

// packAbs returns a copy of the example pack without its tools.json, so
// launches do not run the tools installed on the test machine. Tests of
// required tools write their own tools.json.
func packAbs(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyPack(t, "examples/pack", dst)
	if err := os.Remove(filepath.Join(dst, "tools.json")); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyPack(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
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
			Args   []string `json:"args"`
			Env    []string `json:"env_injected"`
			Skills []struct {
				Name string `json:"name"`
			} `json:"skills"`
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
	if len(launch.Skills) != 1 || launch.Skills[0].Name != "commit-style" {
		t.Fatalf("launch.claude should list the org skills, got %+v", launch.Skills)
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
	copyPack(t, "examples/pack", filepath.Join(origin, "pack"))
	if err := os.Remove(filepath.Join(origin, "pack", "tools.json")); err != nil {
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

	// With the remote gone, a manual refresh fails with the reason instead
	// of quietly running on the cached pack.
	if err := os.Rename(origin, origin+"-gone"); err != nil {
		t.Fatal(err)
	}
	stdout, code = runWr(t, env, "--pack", "file://"+origin, "sync")
	if code == 0 || !strings.Contains(stdout, "could not refresh the defaults pack") {
		t.Fatalf("sync against a missing remote should fail clearly, exit = %d\n%s", code, stdout)
	}

	binDir := t.TempDir()
	fakeAgent(t, binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")
	runEnv := upsertEnv(env,
		"PATH="+binDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		"RECORD="+record,
	)
	stdout, code = runWr(t, runEnv, "--pack", "file://"+origin, "--refresh", "claude")
	if code == 0 || !strings.Contains(stdout, "could not refresh the defaults pack") {
		t.Fatalf("--refresh against a missing remote should fail clearly, exit = %d\n%s", code, stdout)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("the agent must not start when the requested refresh failed")
	}

	// Without --refresh, the launch runs on the cached pack.
	if _, code = runWr(t, runEnv, "--pack", "file://"+origin, "claude"); code != 0 {
		t.Fatalf("a launch without --refresh should use the cached pack, exit = %d", code)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatal("the agent should have started from the cached pack")
	}
}

// toolsSetup is a machine with an old faketool on PATH, a fake npm whose
// global prefix is prefix, and a pack that requires faketool 1.10.0.
type toolsSetup struct {
	pack, prefix, oldDir, binDir, npmLog string
	home                                 string
}

func newToolsSetup(t *testing.T) toolsSetup {
	t.Helper()
	s := toolsSetup{
		pack:   t.TempDir(),
		prefix: t.TempDir(),
		oldDir: t.TempDir(),
		binDir: t.TempDir(),
		home:   controlledHome(t, ""),
	}
	s.npmLog = filepath.Join(t.TempDir(), "npm.log")
	tools := `{"faketool": {"min_version": "1.10.0", "install": {"manager": "npm", "package": "@acme/faketool"}}}`
	if err := os.WriteFile(filepath.Join(s.pack, "tools.json"), []byte(tools), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.oldDir, "faketool"), []byte("#!/bin/sh\necho faketool 1.8.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	npm := `#!/bin/sh
case "$1" in
prefix) echo "` + s.prefix + `" ;;
install)
  echo "$*" >> "$NPM_LOG"
  spec="$3"; ver="${spec##*@}"
  mkdir -p "` + s.prefix + `/bin"
  printf '#!/bin/sh\necho faketool %s\n' "$ver" > "` + s.prefix + `/bin/faketool"
  chmod +x "` + s.prefix + `/bin/faketool" ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(s.binDir, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// env returns the environment with dirs first on PATH, in order.
func (s toolsSetup) env(dirs ...string) []string {
	sep := string(filepath.ListSeparator)
	return upsertEnv(os.Environ(),
		"HOME="+s.home,
		"PATH="+strings.Join(dirs, sep)+sep+s.binDir+sep+os.Getenv("PATH"),
		"NPM_LOG="+s.npmLog,
	)
}

func (s toolsSetup) installs(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(s.npmLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestE2ELaunchWarnsAboutRequiredTools(t *testing.T) {
	s := newToolsSetup(t)
	fakeAgent(t, s.binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")
	env := upsertEnv(s.env(), "RECORD="+record)

	stdout, code := runWr(t, env, "--pack", s.pack, "claude")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "note: faketool is not installed. Run `wr init`.\n") {
		t.Fatalf("launch should warn about the missing tool:\n%s", stdout)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatal("a missing tool must not block the launch")
	}
}

func TestE2EInitDryRun(t *testing.T) {
	s := newToolsSetup(t)
	stdout, code := runWr(t, s.env(filepath.Join(s.prefix, "bin"), s.oldDir), "--pack", s.pack, "init", "--dry-run")
	if code == 0 {
		t.Fatalf("a dry run with a tool to install should exit non-zero\n%s", stdout)
	}
	for _, want := range []string{
		"faketool: too old, 1.8.0 at " + filepath.Join(s.oldDir, "faketool") + " (minimum 1.10.0)\n",
		"will run:\n  npm install -g @acme/faketool@1.10.0\n",
		"dry run, nothing installed",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	if got := s.installs(t); got != "" {
		t.Fatalf("a dry run must not install, npm ran: %s", got)
	}
}

func TestE2EInitWithoutTerminalInstallsNothing(t *testing.T) {
	s := newToolsSetup(t)
	// runWr leaves stdin at /dev/null: not a terminal.
	stdout, code := runWr(t, s.env(filepath.Join(s.prefix, "bin"), s.oldDir), "--pack", s.pack, "init")
	if code == 0 || !strings.Contains(stdout, "rerun with --yes") {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if got := s.installs(t); got != "" {
		t.Fatalf("init must not install without a terminal or --yes, npm ran: %s", got)
	}
}

func TestE2EInitYesInstalls(t *testing.T) {
	s := newToolsSetup(t)
	env := s.env(filepath.Join(s.prefix, "bin"), s.oldDir)
	stdout, code := runWr(t, env, "--pack", s.pack, "init", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if got := s.installs(t); got != "install -g @acme/faketool@1.10.0\n" {
		t.Fatalf("npm calls = %q", got)
	}
	want := "faketool: ok, 1.10.0 at " + filepath.Join(s.prefix, "bin", "faketool") + " (minimum 1.10.0)"
	if !strings.Contains(stdout, "$ npm install -g @acme/faketool@1.10.0\n") || !strings.Contains(stdout, want) {
		t.Fatalf("output:\n%s", stdout)
	}

	// Everything is ok now: init reports it and exits 0 without installing.
	stdout, code = runWr(t, env, "--pack", s.pack, "init")
	if code != 0 || strings.Contains(stdout, "will run") {
		t.Fatalf("second init: exit = %d\n%s", code, stdout)
	}
	if got := s.installs(t); strings.Count(got, "install") != 1 {
		t.Fatalf("second init must not install again, npm calls = %q", got)
	}
}

func TestE2EInitReportsShadowedTool(t *testing.T) {
	s := newToolsSetup(t)
	// The old copy comes before the npm prefix on PATH, where a newer
	// copy is already installed.
	prefixBin := filepath.Join(s.prefix, "bin")
	if err := os.MkdirAll(prefixBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefixBin, "faketool"), []byte("#!/bin/sh\necho faketool 1.10.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, code := runWr(t, s.env(s.oldDir, prefixBin), "--pack", s.pack, "init", "--yes")
	if code == 0 {
		t.Fatalf("a shadowed tool is not ok\n%s", stdout)
	}
	oldPath := filepath.Join(s.oldDir, "faketool")
	for _, want := range []string{
		"1.8.0 at " + oldPath + " comes first on PATH, before 1.10.0 at " + filepath.Join(prefixBin, "faketool"),
		"faketool: cannot install: faketool at " + oldPath + " comes before " + prefixBin + " on PATH",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	if got := s.installs(t); got != "" {
		t.Fatalf("an install that cannot take effect must not run, npm ran: %s", got)
	}
}

func TestE2EDoctorListsRequiredTools(t *testing.T) {
	s := newToolsSetup(t)
	fakeAgent(t, s.binDir, "claude")
	stdout, code := runWr(t, s.env(s.oldDir), "--pack", s.pack, "doctor")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	want := "tools:\n  faketool: too old, 1.8.0 at " + filepath.Join(s.oldDir, "faketool") + " (minimum 1.10.0), run wr init\n"
	if !strings.Contains(stdout, want) {
		t.Fatalf("doctor should list the tool:\n%s", stdout)
	}
	if strings.Count(stdout, "faketool:") != 1 {
		t.Fatalf("the tool should be listed once, not once per agent launch:\n%s", stdout)
	}

	stdout, code = runWr(t, s.env(s.oldDir), "--pack", s.pack, "doctor", "--json")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	var report struct {
		Tools struct {
			Required []struct {
				Name    string `json:"name"`
				State   string `json:"state"`
				Version string `json:"version"`
			} `json:"required"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if r := report.Tools.Required; len(r) != 1 || r[0].Name != "faketool" || r[0].State != "too_old" || r[0].Version != "1.8.0" {
		t.Fatalf("doctor --json tools = %+v", report.Tools)
	}
}

func TestE2EInitFlagsOnlyApplyToInit(t *testing.T) {
	s := newToolsSetup(t)
	fakeAgent(t, s.binDir, "claude")
	record := filepath.Join(t.TempDir(), "record")
	stdout, code := runWr(t, upsertEnv(s.env(), "RECORD="+record), "--pack", s.pack, "--dry-run", "claude")
	if code == 0 || !strings.Contains(stdout, "--dry-run and --yes only apply to init") {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("a launch with --dry-run must not start the agent")
	}
}

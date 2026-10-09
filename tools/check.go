package tools

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// State is the outcome of checking one tool.
type State string

const (
	// OK means the tool is on PATH at min_version or newer.
	OK State = "ok"
	// Missing means no executable with the tool's command is on PATH.
	Missing State = "missing"
	// TooOld means the tool is older than min_version or did not report a
	// version.
	TooOld State = "too_old"
)

// DefaultTimeout bounds each `--version` call made by Check.
const DefaultTimeout = 2 * time.Second

// Status is the checked state of one required tool.
type Status struct {
	Requirement
	State State `json:"state"`
	// Path is the executable found first on PATH; empty when missing.
	Path string `json:"path,omitempty"`
	// Version is the version the tool reported; empty when it reported
	// none.
	Version string `json:"version,omitempty"`
	// Detail explains a version that could not be read, such as a
	// timeout.
	Detail string `json:"detail,omitempty"`
}

// CheckOptions configures Check.
type CheckOptions struct {
	// Env is the environment whose PATH is searched and that the tools
	// run with; nil means os.Environ.
	Env []string
	// Timeout bounds each `--version` call; zero means DefaultTimeout.
	Timeout time.Duration
}

// Check looks up each required tool on PATH and runs it with --version.
// The calls run in parallel and each is bounded by the timeout, so a tool
// that hangs cannot stall the caller. Statuses are returned in the order
// of reqs.
func Check(ctx context.Context, reqs []Requirement, o CheckOptions) []Status {
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	out := make([]Status, len(reqs))
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = checkOne(ctx, r, env, o.Timeout)
		}()
	}
	wg.Wait()
	return out
}

func checkOne(ctx context.Context, r Requirement, env []string, timeout time.Duration) Status {
	s := Status{Requirement: r, State: Missing}
	paths := lookPathAll(r.Command, env)
	if len(paths) == 0 {
		return s
	}
	s.Path = paths[0]
	s.Version, s.Detail = inspect(ctx, r, s.Path, env, timeout)
	if s.Version != "" && atLeast(s.Version, r.MinVersion) {
		s.State = OK
	} else {
		s.State = TooOld
	}
	return s
}

// inspect returns the version of the copy of r at path, or a detail
// explaining why there is none.
func inspect(ctx context.Context, r Requirement, path string, env []string, timeout time.Duration) (string, string) {
	v, detail := versionOf(ctx, path, env, timeout)
	if v == "" && r.Install != nil && r.Install.Manager == "go" {
		// Many Go tools have no --version flag; `go install` records
		// the module version in the binary.
		if mv, ok := goModuleVersion(path); ok {
			return mv, ""
		}
	}
	return v, detail
}

// versionOf runs path --version and returns the first MAJOR.MINOR.PATCH in
// its combined output, or a detail explaining why there is none.
func versionOf(ctx context.Context, path string, env []string, timeout time.Duration) (string, string) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = env
	// A tool that leaves a child holding the output pipe must not keep
	// the wait going past the timeout.
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if v, ok := findVersion(string(out)); ok {
		return v, ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Sprintf("--version did not finish within %s", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", fmt.Sprintf("--version could not run: %v", err)
		}
	}
	return "", "--version printed no version"
}

// goModuleVersion reads the main module version that `go install
// pkg@vX.Y.Z` embeds in a Go binary.
func goModuleVersion(path string) (string, bool) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", false
	}
	return findVersion(info.Main.Version)
}

// Describe renders the status for listings, for example
// "openspec: too old, 1.8.0 at /usr/local/bin/openspec (minimum 1.10.0)".
func (s Status) Describe() string {
	min := "(minimum " + s.MinVersion + ")"
	switch {
	case s.State == Missing:
		return fmt.Sprintf("%s: missing %s", s.Name, min)
	case s.Version == "":
		return fmt.Sprintf("%s: too old, no version reported by %s (%s) %s", s.Name, s.Path, s.Detail, min)
	case s.State == TooOld:
		return fmt.Sprintf("%s: too old, %s at %s %s", s.Name, s.Version, s.Path, min)
	default:
		return fmt.Sprintf("%s: ok, %s at %s %s", s.Name, s.Version, s.Path, min)
	}
}

// Problem renders a status that is not OK as a sentence for launch notes,
// for example "openspec 1.8.0 is older than the required 1.10.0."
// It returns "" for an OK status.
func (s Status) Problem() string {
	switch {
	case s.State == OK:
		return ""
	case s.State == Missing:
		return fmt.Sprintf("%s is not installed.", s.Name)
	case s.Version == "":
		return fmt.Sprintf("%s at %s did not report a version (%s).", s.Name, s.Path, s.Detail)
	default:
		return fmt.Sprintf("%s %s is older than the required %s.", s.Name, s.Version, s.MinVersion)
	}
}

// Newer looks for a copy of the tool later on PATH that meets the
// requirement while the first copy does not. It returns that copy's status
// and true when one exists: the tool is installed but shadowed.
func Newer(ctx context.Context, s Status, o CheckOptions) (Status, bool) {
	if s.State != TooOld {
		return Status{}, false
	}
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	paths := lookPathAll(s.Command, env)
	if len(paths) < 2 {
		return Status{}, false
	}
	for _, p := range paths[1:] {
		v, detail := inspect(ctx, s.Requirement, p, env, o.Timeout)
		if v != "" && atLeast(v, s.MinVersion) {
			return Status{Requirement: s.Requirement, State: OK, Path: p, Version: v, Detail: detail}, true
		}
	}
	return Status{}, false
}

// lookPathAll returns every executable named name on the PATH in env, in
// PATH order. It skips relative PATH entries (an empty entry means the
// current directory), so checking a tool never runs a file from the
// project the user is in, and it skips the running executable, so a
// wrapper installed under a tool's name is never asked for its version.
// Entries that resolve to a file already listed are skipped too.
func lookPathAll(name string, env []string) []string {
	self, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	var out []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(getenv(env, "PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, candidate := range candidates(filepath.Join(dir, name)) {
			if !executable(candidate) {
				continue
			}
			key := candidate
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
				key = resolved
			}
			if key == self {
				break
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, candidate)
			}
			break
		}
	}
	return out
}

func candidates(path string) []string {
	if runtime.GOOS != "windows" {
		return []string{path}
	}
	return []string{path + ".exe", path + ".cmd", path + ".bat", path}
}

func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode()&0o111 != 0
}

// getenv returns the last value of key in a KEY=VALUE environment.
func getenv(env []string, key string) string {
	value := ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && (k == key || runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			value = v
		}
	}
	return value
}

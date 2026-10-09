package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Step is one planned install.
type Step struct {
	// Tool is the requirement the step installs.
	Tool string `json:"tool"`
	// Argv is the command line, with the package manager's name first.
	Argv []string `json:"argv,omitempty"`
	// Binary is the package manager executable that runs Argv.
	Binary string `json:"binary,omitempty"`
	// Err is why the tool cannot be installed on this machine; the step
	// does not run when it is set.
	Err error `json:"-"`
}

// String renders the command line for display.
func (s Step) String() string {
	return strings.Join(s.Argv, " ")
}

// PlanOptions configures Plan.
type PlanOptions struct {
	// Env is the environment the package managers are probed and run
	// with; nil means os.Environ.
	Env []string
}

// Managers lists the package managers a pack can name in install.manager.
func Managers() []string {
	names := make([]string, 0, len(managers))
	for name := range managers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Plan returns one step for each status that is not OK, in order. A step
// carries Err instead of a command line when the tool has no install
// method, names an unknown package manager, or its package manager is not
// usable on this machine. It also carries Err when the copy already on
// PATH did not report a version (the install could downgrade it) or comes
// before the directory the install would write to (the install could not
// take effect).
func Plan(ctx context.Context, statuses []Status, o PlanOptions) []Step {
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	p := planner{env: env, probed: map[string]probed{}}
	var steps []Step
	for _, s := range statuses {
		if s.State == OK {
			continue
		}
		steps = append(steps, p.step(ctx, s))
	}
	return steps
}

// probed is where one package manager installs, asked once per Plan.
type probed struct {
	bin, binDir string
	err         error
}

type planner struct {
	env    []string
	probed map[string]probed
}

func (p planner) step(ctx context.Context, s Status) Step {
	r := s.Requirement
	step := Step{Tool: r.Name}
	if r.Install == nil {
		step.Err = fmt.Errorf("the pack has no install method for %s; install %s %s or newer yourself", r.Name, r.Name, r.MinVersion)
		return step
	}
	m, ok := managers[r.Install.Manager]
	if !ok {
		step.Err = fmt.Errorf("package manager %q is not supported by this wrapper (supported: %s); update the wrapper", r.Install.Manager, strings.Join(Managers(), ", "))
		return step
	}
	if s.State == TooOld && s.Version == "" {
		step.Err = fmt.Errorf("%s at %s did not report a version (%s), so installing %s could replace a newer copy; check it yourself, or remove it and install again",
			r.Name, s.Path, s.Detail, r.MinVersion)
		return step
	}
	pm, ok := p.probed[r.Install.Manager]
	if !ok {
		pm = p.probe(ctx, r.Install.Manager, m)
		p.probed[r.Install.Manager] = pm
	}
	if pm.err != nil {
		step.Err = pm.err
		if pm.bin == "" {
			step.Err = fmt.Errorf("%w; install %s, or install %s %s yourself", pm.err, r.Install.Manager, r.Name, r.MinVersion)
		}
		return step
	}
	if s.State == TooOld {
		oldDir := filepath.Dir(s.Path)
		if before(oldDir, pm.binDir, p.env) {
			step.Err = fmt.Errorf("%s at %s comes before %s on PATH, so a %s install there would not be used; remove %s or move %s earlier on PATH",
				r.Name, s.Path, pm.binDir, r.Install.Manager, s.Path, pm.binDir)
			return step
		}
	}
	step.Binary = pm.bin
	step.Argv = append([]string{r.Install.Manager}, m.args(r.Install.Package, r.MinVersion)...)
	return step
}

// Run executes a planned step with the given environment (nil means
// os.Environ) and streams. It never elevates privileges.
func Run(ctx context.Context, s Step, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if s.Err != nil {
		return s.Err
	}
	if s.Binary == "" || len(s.Argv) == 0 {
		return errors.New("tools: empty step")
	}
	cmd := exec.CommandContext(ctx, s.Binary, s.Argv[1:]...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", s.String(), err)
	}
	return nil
}

// probe finds the manager's executable and the directory it installs
// executables into, and checks that this directory is usable.
func (p planner) probe(ctx context.Context, name string, m manager) probed {
	bin := firstOnPath(name, p.env)
	if bin == "" {
		return probed{err: fmt.Errorf("%s is not installed", name)}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	binDir, err := m.binDir(ctx, bin, p.env)
	if err != nil {
		return probed{bin: bin, err: fmt.Errorf("%s: %w", name, err)}
	}
	if err := usableBinDir(name, binDir, p.env); err != nil {
		return probed{bin: bin, err: err}
	}
	for _, dir := range m.writeDirs(binDir) {
		if err := writableOrCreatable(dir); err != nil {
			return probed{bin: bin, err: fmt.Errorf("%s cannot write to %s (%v); fix the %s install directory permissions, never install with sudo", name, dir, err, name)}
		}
	}
	return probed{bin: bin, binDir: binDir}
}

// before reports whether dir a comes strictly before dir b on PATH.
func before(a, b string, env []string) bool {
	if samePath(a, b) {
		return false
	}
	for _, p := range filepath.SplitList(getenv(env, "PATH")) {
		switch {
		case samePath(p, a):
			return true
		case samePath(p, b):
			return false
		}
	}
	return false
}

// probeTimeout bounds the calls that ask a package manager where it
// installs (npm prefix -g, go env).
const probeTimeout = 10 * time.Second

type manager struct {
	// args returns the install arguments after the manager's name.
	args func(pkg, version string) []string
	// binDir returns the directory the manager links global executables
	// into.
	binDir func(ctx context.Context, bin string, env []string) (string, error)
	// writeDirs returns the directories an install writes to.
	writeDirs func(binDir string) []string
}

var managers = map[string]manager{
	"npm": {
		args: func(pkg, v string) []string { return []string{"install", "-g", pkg + "@" + v} },
		binDir: func(ctx context.Context, bin string, env []string) (string, error) {
			prefix, err := probe(ctx, bin, env, "prefix", "-g")
			if err != nil {
				return "", err
			}
			if runtime.GOOS == "windows" {
				return strings.TrimSpace(prefix), nil
			}
			return filepath.Join(strings.TrimSpace(prefix), "bin"), nil
		},
		writeDirs: func(binDir string) []string {
			if runtime.GOOS == "windows" {
				return []string{binDir}
			}
			return []string{binDir, filepath.Join(filepath.Dir(binDir), "lib", "node_modules")}
		},
	},
	"pnpm": {
		args: func(pkg, v string) []string { return []string{"add", "-g", pkg + "@" + v} },
		binDir: func(_ context.Context, _ string, env []string) (string, error) {
			home := getenv(env, "PNPM_HOME")
			if home == "" {
				return "", errors.New("PNPM_HOME is not set; run `pnpm setup` and open a new shell")
			}
			return home, nil
		},
		writeDirs: func(binDir string) []string { return []string{binDir} },
	},
	"bun": {
		args: func(pkg, v string) []string { return []string{"add", "-g", pkg + "@" + v} },
		binDir: func(_ context.Context, _ string, env []string) (string, error) {
			root := getenv(env, "BUN_INSTALL")
			if root == "" {
				home := getenv(env, "HOME")
				if home == "" {
					return "", errors.New("neither BUN_INSTALL nor HOME is set")
				}
				root = filepath.Join(home, ".bun")
			}
			return filepath.Join(root, "bin"), nil
		},
		writeDirs: func(binDir string) []string { return []string{filepath.Dir(binDir)} },
	},
	"go": {
		args: func(pkg, v string) []string { return []string{"install", pkg + "@v" + v} },
		binDir: func(ctx context.Context, bin string, env []string) (string, error) {
			out, err := probe(ctx, bin, env, "env", "GOBIN", "GOPATH")
			if err != nil {
				return "", err
			}
			// GOBIN is often unset, which prints an empty first line.
			gobin, gopath, _ := strings.Cut(out, "\n")
			if gobin = strings.TrimSpace(gobin); gobin != "" {
				return gobin, nil
			}
			first := filepath.SplitList(strings.TrimSpace(gopath))
			if len(first) == 0 || first[0] == "" {
				return "", errors.New("`go env` reported neither GOBIN nor GOPATH")
			}
			return filepath.Join(first[0], "bin"), nil
		},
		writeDirs: func(binDir string) []string { return []string{binDir} },
	},
}

// probe runs a package manager query and returns its stdout without the
// trailing newline. Leading blank lines are kept: they are empty values.
func probe(ctx context.Context, bin string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("`%s %s` failed: %v", filepath.Base(bin), strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// usableBinDir checks that executables the manager installs will be found
// on PATH.
func usableBinDir(name, dir string, env []string) error {
	for _, p := range filepath.SplitList(getenv(env, "PATH")) {
		if samePath(p, dir) {
			return nil
		}
	}
	return fmt.Errorf("%s installs executables into %s, which is not on PATH; add it to PATH and open a new shell", name, dir)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// writableOrCreatable checks that dir, or its nearest existing ancestor
// when dir does not exist yet, is writable by the current user.
func writableOrCreatable(dir string) error {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			return writable(dir)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
}

func firstOnPath(name string, env []string) string {
	if paths := lookPathAll(name, env); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

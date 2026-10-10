// Package wrapper turns coding agents (Claude Code, OpenCode, pi, ...) into
// org-managed launchables.
//
// The core idea: organizations maintain a "defaults pack" (a versioned
// directory, typically a git repo) holding per-agent defaults: settings,
// MCP servers, plugins, skills, env vars, policy. A wrapper binary embeds
// this library, registers adapters for the agents it wants to launch, and
// maps `mywrapper claude` to "run the real claude with the pack layered on
// top, at launch time, without touching user config".
//
// Minimal embedding:
//
//	package main
//
//	import (
//		"context"
//		"os"
//
//		wrapper "github.com/you/coding-agent-wrapper"
//		"github.com/you/coding-agent-wrapper/adapters/claude"
//		"github.com/you/coding-agent-wrapper/pack"
//	)
//
//	func main() {
//		wrapper.Register(claude.New())
//		err := wrapper.Run(context.Background(), wrapper.Options{
//			Agent: "claude",
//			Args:  os.Args[1:],
//			Pack:  pack.Git("github.com/acme/agent-defaults", "main"),
//		})
//		if err != nil {
//			os.Exit(1)
//		}
//	}
//
// On UNIX, Run execs the agent in place: on success it never returns, and
// signals, exit codes and terminal state behave exactly like running the
// agent directly.
package wrapper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wejick/coding-agent-wrapper/pack"
	"github.com/wejick/coding-agent-wrapper/tools"
)

// Options configures a launch.
type Options struct {
	// Agent is a registered adapter name, e.g. "claude".
	Agent string
	// Args are the user's arguments, passed through to the agent after any
	// adapter-injected flags.
	Args []string
	// Pack is the defaults pack source. Required.
	Pack pack.Source
	// Refresh forces a pack round trip before launching instead of using
	// the cached copy.
	Refresh bool
	// Fetched, when set, is used instead of fetching Pack; Refresh is ignored.
	Fetched *pack.FetchResult
	// SkipPolicy skips the pack's policy layer and stops forcing pack env
	// defaults, so the launch runs with soft defaults only. The zero
	// value enforces org policy.
	SkipPolicy bool
	// StrictMCP replaces the user's MCP configuration instead of extending
	// it (where the agent supports it).
	StrictMCP bool
	// Env is the base environment for the child; nil means os.Environ.
	Env []string
	// Dir is the agent's working directory and project root: adapters
	// read project config from it and the agent starts in it. Empty
	// means the current working directory.
	Dir string
	// Headless runs the agent non-interactively on one task. Nil means
	// an interactive launch. Agents without a headless mode fail with
	// ErrHeadlessUnsupported.
	Headless *Headless
	// SetupCommand is the command users run to install the pack's
	// required tools, such as "wr init". When set, warnings about a tool
	// the pack can install end with "Run `<SetupCommand>`.", or with why
	// the setup command would refuse to install it; otherwise they name
	// the version to install.
	SetupCommand string
	// SkipTools skips checking the tools the pack requires (tools.json).
	// Callers that check them separately, such as a doctor command, set
	// it to avoid running every tool once per launch they prepare.
	SkipTools bool
	// Stdin, Stdout and Stderr are used on platforms without in-place exec;
	// on UNIX the child inherits the process's streams. Run also writes
	// the launch warnings to Stderr (os.Stderr when nil).
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Launch is a computed launch: everything needed to start the agent, plus a
// decision trail explaining what the wrapper layered on.
type Launch struct {
	Agent  string   `json:"agent"`
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env,omitempty"`
	// Dir is the absolute directory the agent starts in; empty means the
	// caller's working directory.
	Dir   string   `json:"dir,omitempty"`
	Notes []string `json:"notes,omitempty"`
	// Warnings are problems the user can act on, such as a required tool
	// that is missing or too old. They are also listed in Notes. A
	// warning never blocks the launch; wrappers print them before
	// executing the agent.
	Warnings    []string       `json:"warnings,omitempty"`
	Files       []string       `json:"files,omitempty"`
	Source      string         `json:"pack_source,omitempty"`
	PackVersion map[string]any `json:"pack_version,omitempty"`
}

// Prepare resolves the agent, fetches the defaults pack and computes the
// launch without starting the agent. The only commands it runs are
// `<tool> --version` for the tools the pack requires (none with
// SkipTools). Use it for diagnostics, tests and tooling that needs to
// know exactly what would execute; Run is Prepare followed by
// Launch.Exec.
func Prepare(ctx context.Context, opts Options) (*Launch, error) {
	if opts.Agent == "" {
		return nil, errors.New("wrapper: no agent specified")
	}
	a, err := Lookup(opts.Agent)
	if err != nil {
		return nil, err
	}
	if opts.Pack == nil {
		return nil, errors.New("wrapper: no defaults pack configured")
	}
	if opts.Headless != nil && opts.Headless.Prompt == "" {
		return nil, errors.New("wrapper: headless launch needs a prompt")
	}
	dir := opts.Dir
	if dir != "" {
		if dir, err = filepath.Abs(dir); err != nil {
			return nil, err
		}
		if info, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("wrapper: launch directory: %w", err)
		} else if !info.IsDir() {
			return nil, fmt.Errorf("wrapper: launch directory %s is not a directory", dir)
		}
	}
	res := opts.Fetched
	if res == nil {
		if res, err = opts.Pack.Fetch(ctx, pack.FetchOptions{Refresh: opts.Refresh}); err != nil {
			return nil, err
		}
	}
	p, err := pack.Load(res.Dir, opts.Pack.Describe())
	if err != nil {
		return nil, err
	}
	p.Notes = append(p.Notes, res.Notes...)
	if opts.Fetched != nil && opts.Refresh {
		p.Notes = append(p.Notes, "refresh not attempted: the caller passed an already-fetched pack")
	}
	launch, err := a.Build(ctx, p, BuildOptions{
		Args:       opts.Args,
		Env:        opts.Env,
		SkipPolicy: opts.SkipPolicy,
		StrictMCP:  opts.StrictMCP,
		Dir:        dir,
		Headless:   opts.Headless,
	})
	if err != nil {
		return nil, err
	}
	launch.Agent = a.Name()
	launch.Dir = dir
	launch.Source = opts.Pack.Describe() + " (" + res.From + ")"
	launch.PackVersion = p.Version
	launch.Notes = append(launch.Notes, p.Notes...)
	if !opts.SkipTools {
		launch.Warnings = append(launch.Warnings, toolWarnings(ctx, p.Dir, opts)...)
		launch.Notes = append(launch.Notes, launch.Warnings...)
	}
	return launch, nil
}

// toolWarnings checks the tools the pack requires and returns one warning
// per problem. A tools.json that cannot be read becomes a warning too, so
// it never blocks a launch.
func toolWarnings(ctx context.Context, dir string, opts Options) []string {
	reqs, err := tools.Load(dir)
	if err != nil {
		return []string{fmt.Sprintf("required tools were not checked: %v", err)}
	}
	statuses := tools.Check(ctx, reqs, tools.CheckOptions{Env: opts.Env})
	var refused map[string]error
	if opts.SetupCommand != "" {
		refused = refusedInstalls(ctx, statuses, opts.Env)
	}
	var warnings []string
	for _, s := range statuses {
		problem := s.Problem()
		if problem == "" {
			continue
		}
		switch {
		case s.Install == nil || opts.SetupCommand == "":
			problem += fmt.Sprintf(" Install %s %s or newer.", s.Name, s.MinVersion)
		case refused[s.Name] != nil:
			problem += fmt.Sprintf(" `%s` cannot install it: %v.", opts.SetupCommand, refused[s.Name])
		default:
			problem += " Run `" + opts.SetupCommand + "`."
		}
		warnings = append(warnings, problem)
	}
	return warnings
}

// planTimeout bounds the install planning a launch does for broken tools.
const planTimeout = 3 * time.Second

// refusedInstalls plans the installs for the tools that are not ok and
// returns, per tool, why the setup command would refuse to install it, so
// a warning never sends the user to a command that cannot help. It runs
// nothing when every tool is ok. When planning does not finish in time it
// returns nil and the warnings point to the setup command as usual.
func refusedInstalls(ctx context.Context, statuses []tools.Status, env []string) map[string]error {
	var broken []tools.Status
	for _, s := range statuses {
		if s.State != tools.OK && s.Install != nil {
			broken = append(broken, s)
		}
	}
	if len(broken) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()
	steps := tools.Plan(ctx, broken, tools.PlanOptions{Env: env})
	if ctx.Err() != nil {
		return nil
	}
	refused := map[string]error{}
	for _, st := range steps {
		if st.Err != nil {
			refused[st.Tool] = st.Err
		}
	}
	return refused
}

// Run prepares the launch, prints its warnings as "note: ..." lines and
// starts the agent. On UNIX it replaces the current process (it never
// returns on success); elsewhere it runs the agent as a child and waits,
// returning its error.
func Run(ctx context.Context, opts Options) error {
	launch, err := Prepare(ctx, opts)
	if err != nil {
		return err
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	for _, w := range launch.Warnings {
		fmt.Fprintln(stderr, "note: "+w)
	}
	return launch.Exec(ExecOptions{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
}

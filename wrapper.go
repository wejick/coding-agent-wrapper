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
	"io"

	"github.com/wejick/coding-agent-wrapper/pack"
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
	// SkipPolicy skips the pack's policy layer and stops forcing pack env
	// defaults, so the launch runs with soft defaults only. The zero
	// value enforces org policy.
	SkipPolicy bool
	// StrictMCP replaces the user's MCP configuration instead of extending
	// it (where the agent supports it).
	StrictMCP bool
	// Env is the base environment for the child; nil means os.Environ.
	Env []string
	// Stdin, Stdout and Stderr are used on platforms without in-place exec;
	// on UNIX the child inherits the process's streams.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Launch is a computed launch: everything needed to start the agent, plus a
// decision trail explaining what the wrapper layered on.
type Launch struct {
	Agent       string         `json:"agent"`
	Binary      string         `json:"binary"`
	Args        []string       `json:"args"`
	Env         []string       `json:"env,omitempty"`
	Notes       []string       `json:"notes,omitempty"`
	Files       []string       `json:"files,omitempty"`
	Source      string         `json:"pack_source,omitempty"`
	PackVersion map[string]any `json:"pack_version,omitempty"`
}

// Prepare resolves the agent, fetches the defaults pack and computes the
// launch without running anything. Use it for diagnostics, tests and
// tooling that needs to know exactly what would execute; Run is Prepare
// followed by Launch.Exec.
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
	res, err := opts.Pack.Fetch(ctx, pack.FetchOptions{Refresh: opts.Refresh})
	if err != nil {
		return nil, err
	}
	p, err := pack.Load(res.Dir, opts.Pack.Describe())
	if err != nil {
		return nil, err
	}
	p.Notes = append(p.Notes, res.Notes...)
	launch, err := a.Build(ctx, p, BuildOptions{
		Args:       opts.Args,
		Env:        opts.Env,
		SkipPolicy: opts.SkipPolicy,
		StrictMCP:  opts.StrictMCP,
	})
	if err != nil {
		return nil, err
	}
	launch.Agent = a.Name()
	launch.Source = opts.Pack.Describe() + " (" + res.From + ")"
	launch.PackVersion = p.Version
	launch.Notes = append(launch.Notes, p.Notes...)
	return launch, nil
}

// Run prepares the launch and starts the agent. On UNIX it replaces the
// current process (it never returns on success); elsewhere it runs the agent
// as a child and waits, returning its error.
func Run(ctx context.Context, opts Options) error {
	launch, err := Prepare(ctx, opts)
	if err != nil {
		return err
	}
	return launch.Exec(ExecOptions{
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
}

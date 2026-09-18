// Command wr is the reference CLI for the coding-agent-wrapper library. It
// demonstrates the smallest complete wrapper an organization can ship: a
// named binary that maps `wr <agent>` to "run the real agent with the org's
// defaults pack layered on top".
//
// The name is a placeholder. Organizations embed the library in their own
// binary (see the README); this command exists to exercise the library end
// to end.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/claude"
	"github.com/wejick/coding-agent-wrapper/pack"
)

const usageText = `wr runs coding agents with organization defaults.

Usage:
  wr [flags] <agent> [agent args...]   run an agent with the org defaults pack
  wr [flags] doctor [--json]           verify setup and show computed launches
  wr [flags] sync                      refresh the defaults pack
  wr agents                            list registered agents

Flags:
  --pack URL|PATH    defaults pack: a git URL or a local directory
                     (env WRAPPER_PACK_URL)
  --ref REF          git branch, tag or commit to pin (env WRAPPER_PACK_REF)
  --no-policy        skip the pack's policy layer and env forcing for
                     this launch (env WRAPPER_NO_POLICY=1)
  --strict-mcp       replace the user's MCP servers instead of extending them
  --refresh          force a pack refresh before acting
  --json             with doctor: print the report as JSON

Examples:
  WRAPPER_PACK_URL=github.com/acme/agent-defaults wr claude
  wr --pack github.com/acme/agent-defaults --ref v1.2.0 claude --model opus
  wr --pack ./examples/pack doctor
`

func main() {
	wrapper.Register(claude.New())
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wr:", err)
		os.Exit(1)
	}
}

type globals struct {
	packRef   string
	rev       string
	noPolicy  bool
	strictMCP bool
	refresh   bool
	jsonOut   bool
}

func run(ctx context.Context, args []string) error {
	fs := newFlagSet()
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return errors.New("missing command")
	}
	cmd, cmdArgs := rest[0], rest[1:]

	// Diagnostics commands accept flags after the command too (`wr sync
	// --refresh`); agent commands must not touch their arguments.
	switch cmd {
	case "help", "sync", "doctor":
		if err := fs.Parse(cmdArgs); err != nil {
			return err
		}
		cmdArgs = fs.Args()
	}

	switch cmd {
	case "help":
		fmt.Print(usageText)
		return nil
	case "agents":
		fmt.Println(strings.Join(wrapper.Agents(), "\n"))
		return nil
	case "sync":
		return cmdSync(ctx)
	case "doctor":
		return cmdDoctor(ctx)
	default:
		if _, err := wrapper.Lookup(cmd); err != nil {
			return err
		}
		return cmdRun(ctx, cmd, cmdArgs)
	}
}

func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("wr", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	fs.StringVar(&globalsRef().packRef, "pack", os.Getenv("WRAPPER_PACK_URL"), "")
	fs.StringVar(&globalsRef().rev, "ref", os.Getenv("WRAPPER_PACK_REF"), "")
	fs.BoolVar(&globalsRef().noPolicy, "no-policy", envBool("WRAPPER_NO_POLICY"), "")
	fs.BoolVar(&globalsRef().strictMCP, "strict-mcp", false, "")
	fs.BoolVar(&globalsRef().refresh, "refresh", false, "")
	fs.BoolVar(&globalsRef().jsonOut, "json", false, "")
	return fs
}

var g globals

func globalsRef() *globals { return &g }

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "1" || strings.EqualFold(v, "true")
}

func source() (pack.Source, error) {
	if g.packRef == "" {
		return nil, errors.New("no defaults pack configured: pass --pack or set WRAPPER_PACK_URL")
	}
	if info, err := os.Stat(g.packRef); err == nil && info.IsDir() {
		return pack.Local(g.packRef), nil
	}
	return pack.Git(g.packRef, g.rev), nil
}

func options(agent string, args []string, src pack.Source) wrapper.Options {
	return wrapper.Options{
		Agent:      agent,
		Args:       args,
		Pack:       src,
		Refresh:    g.refresh,
		SkipPolicy: g.noPolicy,
		StrictMCP:  g.strictMCP,
	}
}

func cmdRun(ctx context.Context, agent string, args []string) error {
	src, err := source()
	if err != nil {
		return err
	}
	return wrapper.Run(ctx, options(agent, args, src))
}

type doctorReport struct {
	Wrapper string                   `json:"wrapper"`
	Runtime string                   `json:"runtime"`
	Agents  []agentStatus            `json:"agents"`
	Pack    *packStatus              `json:"pack,omitempty"`
	Launch  map[string]*launchStatus `json:"launch,omitempty"`
}

type agentStatus struct {
	Name   string `json:"name"`
	Binary string `json:"binary,omitempty"`
	Error  string `json:"error,omitempty"`
}

type packStatus struct {
	Source  string   `json:"source"`
	From    string   `json:"from,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Head    string   `json:"head,omitempty"`
	Version string   `json:"version,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type launchStatus struct {
	Binary string   `json:"binary,omitempty"`
	Args   []string `json:"args,omitempty"`
	Env    []string `json:"env_injected,omitempty"`
	Files  []string `json:"files,omitempty"`
	Notes  []string `json:"notes,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// diffEnv returns entries of env that are absent from base or hold a
// different value: the injected and overridden variables.
func diffEnv(base, env []string) []string {
	known := make(map[string]string, len(base))
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			known[k] = v
		}
	}
	var out []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if known[k] != v {
			out = append(out, kv)
		}
	}
	return out
}

// cmdDoctor verifies the setup without running anything: agent binaries,
// pack state, and the computed launch for every registered agent. It works
// before any pack is configured, which makes it the setup check.
func cmdDoctor(ctx context.Context) error {
	report := doctorReport{
		Wrapper: "wr (coding-agent-wrapper reference CLI)",
		Runtime: fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
		Agents:  []agentStatus{},
	}
	for _, name := range wrapper.Agents() {
		st := agentStatus{Name: name}
		a, err := wrapper.Lookup(name)
		if err == nil {
			st.Binary, err = a.Locate()
		}
		if err != nil {
			st.Error = err.Error()
		}
		report.Agents = append(report.Agents, st)
	}

	if g.packRef != "" {
		report.Pack = checkPack(ctx, &report)
	}

	if g.jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	printDoctor(report)
	return nil
}

func checkPack(ctx context.Context, report *doctorReport) *packStatus {
	src, err := source()
	if err != nil {
		return &packStatus{Source: g.packRef, Error: err.Error()}
	}
	ps := &packStatus{Source: src.Describe()}
	res, err := src.Fetch(ctx, pack.FetchOptions{Refresh: g.refresh})
	if err != nil {
		ps.Error = err.Error()
		return ps
	}
	p, err := pack.Load(res.Dir, src.Describe())
	if err != nil {
		ps.From, ps.Dir = res.From, res.Dir
		ps.Error = err.Error()
		return ps
	}
	ps.From, ps.Dir = res.From, res.Dir
	ps.Head = pack.HeadInfo(ctx, res.Dir)
	ps.Version = p.ShortVersion()
	ps.Notes = append(res.Notes, p.Notes...)

	report.Launch = make(map[string]*launchStatus, len(report.Agents))
	for _, name := range wrapper.Agents() {
		ls := &launchStatus{}
		launch, err := wrapper.Prepare(ctx, options(name, nil, src))
		if err != nil {
			ls.Error = err.Error()
		} else {
			ls.Binary = launch.Binary
			ls.Args = launch.Args
			ls.Env = diffEnv(os.Environ(), launch.Env)
			ls.Files = launch.Files
			ls.Notes = launch.Notes
		}
		report.Launch[name] = ls
	}
	return ps
}

func printDoctor(r doctorReport) {
	fmt.Printf("wrapper: %s\n", r.Wrapper)
	fmt.Printf("runtime: %s\n", r.Runtime)

	fmt.Println("\nagents:")
	for _, a := range r.Agents {
		if a.Error != "" {
			fmt.Printf("  %-10s NOT FOUND (%s)\n", a.Name, a.Error)
			continue
		}
		fmt.Printf("  %-10s %s\n", a.Name, a.Binary)
	}

	if r.Pack != nil {
		fmt.Println("\npack:")
		fmt.Printf("  source:  %s\n", r.Pack.Source)
		if r.Pack.Error != "" {
			fmt.Printf("  error:   %s\n", r.Pack.Error)
		} else {
			fmt.Printf("  from:    %s\n", r.Pack.From)
			fmt.Printf("  dir:     %s\n", r.Pack.Dir)
			fmt.Printf("  head:    %s\n", r.Pack.Head)
			fmt.Printf("  version: %s\n", r.Pack.Version)
			for _, note := range r.Pack.Notes {
				fmt.Println("  note:    " + note)
			}
		}
	}

	if len(r.Launch) > 0 {
		fmt.Println("\nlaunch:")
		for _, a := range r.Agents {
			ls, ok := r.Launch[a.Name]
			if !ok {
				continue
			}
			if ls.Error != "" {
				fmt.Printf("  %s: error: %s\n", a.Name, ls.Error)
				continue
			}
			fmt.Printf("  %s: %s\n", a.Name, ls.Binary)
			if len(ls.Args) > 0 {
				fmt.Printf("    args:  %s\n", strings.Join(ls.Args, " "))
			}
			if len(ls.Env) > 0 {
				fmt.Printf("    env:   %s\n", strings.Join(ls.Env, " "))
			}
			for _, f := range ls.Files {
				fmt.Printf("    file:  %s\n", f)
			}
			for _, note := range ls.Notes {
				fmt.Printf("    - %s\n", note)
			}
		}
	}
}

func cmdSync(ctx context.Context) error {
	src, err := source()
	if err != nil {
		return err
	}
	res, err := src.Fetch(ctx, pack.FetchOptions{Refresh: true})
	if err != nil {
		return err
	}
	p, err := pack.Load(res.Dir, src.Describe())
	if err != nil {
		return err
	}
	fmt.Printf("pack:   %s\nfrom:   %s\ndir:    %s\nhead:   %s\nversion: %s\n",
		src.Describe(), res.From, res.Dir, pack.HeadInfo(ctx, res.Dir), p.ShortVersion())
	for _, note := range append(res.Notes, p.Notes...) {
		fmt.Println("note:   " + note)
	}
	// Launches tolerate a stale cached pack; an explicit sync must not.
	if len(res.Notes) > 0 {
		return errors.New("sync incomplete; see notes above")
	}
	return nil
}

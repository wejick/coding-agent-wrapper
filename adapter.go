package wrapper

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wejick/coding-agent-wrapper/pack"
)

// BuildOptions carries everything an adapter needs to turn a pack into a
// launch.
type BuildOptions struct {
	// Args are the user's arguments; adapters must append them after any
	// injected flags so explicit user flags still apply.
	Args []string
	// Env is the base environment for the child process; nil means
	// os.Environ.
	Env []string
	// SkipPolicy skips the pack's policy layer (locked settings) and
	// stops forcing pack env defaults. The zero value enforces org
	// policy.
	SkipPolicy bool
	// StrictMCP asks the agent to replace the user's MCP configuration
	// instead of extending it, where the agent supports it.
	StrictMCP bool
	// Dir is the absolute working directory and project root of the
	// launch; adapters read project config from it. Empty means the
	// current working directory.
	Dir string
	// Headless asks for a non-interactive run. Adapters turn it into
	// agent flags placed after the pack-injected flags and before Args,
	// or return ErrHeadlessUnsupported. Nil means interactive.
	Headless *Headless
}

// Headless describes a non-interactive run: the agent gets one task, runs
// it without a user at the terminal and exits.
type Headless struct {
	// Prompt is the task to run. Required.
	Prompt string
	// Model is an optional model alias or name.
	Model string
	// Effort is an optional effort level.
	Effort string
	// Unattended turns off permission prompts.
	Unattended bool
}

// ErrHeadlessUnsupported is returned by adapters whose agent has no
// headless mode.
var ErrHeadlessUnsupported = errors.New("wrapper: agent has no headless mode")

// Adapter integrates one coding agent with the wrapper. Implement this to
// support a new agent and register it from your binary with Register.
type Adapter interface {
	// Name is the subcommand users type, e.g. "claude".
	Name() string
	// Locate returns the path of the real agent binary, skipping the
	// wrapper itself on PATH.
	Locate() (string, error)
	// Build computes the launch for the given pack.
	Build(ctx context.Context, p *pack.Pack, o BuildOptions) (*Launch, error)
	// Skills returns the pack's skills for this agent, or nil if unsupported.
	Skills(p *pack.Pack) Skills
}

// Skills is an agent's view of the skills in one pack.
type Skills interface {
	// List returns the skills sorted by Name, then by Path.
	List() ([]pack.Skill, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Adapter{}
)

// Register makes an adapter available to Prepare and Run. It panics on
// duplicate names: adapters are static configuration.
func Register(a Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[a.Name()]; dup {
		panic(fmt.Sprintf("wrapper: adapter %q registered twice", a.Name()))
	}
	registry[a.Name()] = a
}

// Lookup returns the registered adapter with the given name.
func Lookup(name string) (Adapter, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("wrapper: no adapter registered for %q (registered: %s)", name, strings.Join(Agents(), ", "))
	}
	return a, nil
}

// Agents lists registered adapter names, sorted.
func Agents() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

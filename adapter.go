package wrapper

import (
	"context"
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
}

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

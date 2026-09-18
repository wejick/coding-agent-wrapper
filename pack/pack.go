// Package pack loads and distributes "defaults packs": versioned directories
// of per-agent defaults (settings, MCP servers, plugins, env vars, ...) that
// an organization distributes to its coding agents.
//
// A pack is any directory containing one subdirectory per agent:
//
//	version.json          optional: {"name": "...", "version": "..."}
//	claude/               defaults for the Claude Code adapter
//	  settings.json         soft defaults (user settings win per key)
//	  policy.json           locked layer (applied on top when enforcing)
//	  mcp.json              {"mcpServers": {...}} added to the user's
//	  env.json              {"KEY": "VALUE"} injected environment
//	  system-prompt.md      appended to the agent's system prompt
//	  plugin/               org plugin dir (skills, agents, commands, hooks)
//	opencode/             (future adapters follow the same pattern)
package pack

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FetchOptions controls how a Source materializes the pack.
type FetchOptions struct {
	// Refresh forces a round trip to the source. Without it, sources that
	// cache (like Git) reuse the local copy so launches stay fast and work
	// offline.
	Refresh bool
}

// FetchResult describes where a pack came from.
type FetchResult struct {
	// Dir is the local directory holding the pack.
	Dir string
	// From is one of "local", "git" or "cache".
	From string
	// Notes carries warnings and decisions worth surfacing to the user.
	Notes []string
}

// Source materializes a defaults pack into a local directory.
type Source interface {
	Fetch(ctx context.Context, o FetchOptions) (*FetchResult, error)
	// Describe returns a short human-readable label, used in logs and
	// launches.
	Describe() string
}

// Local serves a pack from a fixed directory: development, monorepos,
// vendored installs.
func Local(dir string) Source {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return localSource(abs)
}

type localSource string

func (l localSource) Fetch(_ context.Context, _ FetchOptions) (*FetchResult, error) {
	return &FetchResult{Dir: string(l), From: "local"}, nil
}

func (l localSource) Describe() string { return "local:" + string(l) }

// Git serves a pack from a git URL, cached under the user's cache dir.
// It shells out to the git binary so existing SSH keys, credential helpers
// and proxy configuration apply unchanged. The ref may be a branch, tag or
// commit; empty means the remote's default branch. Pin a ref in production
// for reproducible launches.
//
// The URL may carry a "#subdir" suffix (e.g. "github.com/acme/defaults#pack")
// to root the pack at a subdirectory of the repo, keeping room for a README
// and CI config at the top level.
func Git(url, ref string) Source {
	sub := ""
	if i := strings.Index(url, "#"); i >= 0 {
		url, sub = url[:i], url[i+1:]
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return gitSource{url: url, ref: ref, dir: filepath.Join(base, "coding-agent-wrapper", "git", cacheKey(url)), sub: sub}
}

// GitWithCache is Git with an explicit cache directory and no subdirectory.
func GitWithCache(url, ref, cacheDir string) Source {
	return gitSource{url: url, ref: ref, dir: cacheDir}
}

type gitSource struct {
	url string
	ref string
	dir string
	sub string
}

func (g gitSource) Describe() string {
	s := "git:" + g.url
	if g.ref != "" {
		s += "@" + g.ref
	}
	if g.sub != "" {
		s += "#" + g.sub
	}
	return s
}

func cacheKey(url string) string {
	sum := sha1.Sum([]byte(url))
	return hex.EncodeToString(sum[:8])
}

// Pack is a loaded defaults pack.
type Pack struct {
	// Dir is the local pack directory.
	Dir string
	// Source labels where the pack came from.
	Source string
	// Notes carries warnings from loading and fetching.
	Notes []string
	// Version is the parsed version.json content, nil when absent.
	Version map[string]any
}

// Load validates that dir exists and reads optional metadata. A pack may be
// empty; adapters decide what a missing agent subdirectory means.
func Load(dir, source string) (*Pack, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("pack: %s is not a directory", dir)
	}
	p := &Pack{Dir: dir, Source: source}
	var version map[string]any
	ok, err := readJSONFile(filepath.Join(dir, "version.json"), &version)
	if err != nil {
		return nil, fmt.Errorf("pack: version.json: %w", err)
	}
	if ok {
		p.Version = version
	}
	return p, nil
}

// AgentDir returns the pack subdirectory for an agent (present or not).
func (p *Pack) AgentDir(agent string) string {
	return filepath.Join(p.Dir, agent)
}

// File returns the path to agent/name if it exists as a regular file.
func (p *Pack) File(agent, name string) (string, bool) {
	path := filepath.Join(p.AgentDir(agent), name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// Subdir returns the path to agent/name if it exists as a directory.
func (p *Pack) Subdir(agent, name string) (string, bool) {
	path := filepath.Join(p.AgentDir(agent), name)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return path, true
}

// JSONFile unmarshals agent/name into v. It returns false when the file
// does not exist, and an error when it exists but is not valid JSON.
func (p *Pack) JSONFile(agent, name string, v any) (bool, error) {
	path, ok := p.File(agent, name)
	if !ok {
		return false, nil
	}
	exists, err := readJSONFile(path, v)
	if err != nil {
		return true, fmt.Errorf("pack: %s/%s: %w", agent, name, err)
	}
	return exists, nil
}

// ShortVersion renders the pack version for display.
func (p *Pack) ShortVersion() string {
	if p.Version == nil {
		return "(unversioned)"
	}
	name, _ := p.Version["name"].(string)
	version, _ := p.Version["version"].(string)
	switch {
	case name != "" && version != "":
		return name + " " + version
	case version != "":
		return version
	case name != "":
		return name
	default:
		return "(unversioned)"
	}
}

func readJSONFile(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

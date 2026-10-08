// Package pack loads and distributes "defaults packs": versioned directories
// of per-agent defaults (settings, MCP servers, plugins, env vars, ...) that
// an organization distributes to its coding agents.
//
// A pack is any directory containing one subdirectory per agent:
//
//	version.json          optional: {"name": "...", "version": "..."}
//	claude/               defaults for the Claude Code adapter
//	  settings.json         soft defaults (user settings win per key)
//	  policy.json           locked layer (applied on top by default)
//	  mcp.json              {"mcpServers": {...}} added to the user's
//	  env.json              {"KEY": "VALUE"} injected environment
//	  system-prompt.md      appended to the agent's system prompt
//	  plugin/               org plugin dir (skills, agents, commands, hooks)
//	opencode/             defaults for the OpenCode adapter
//	  settings.json         soft defaults in OpenCode config shape (the
//	                        user's global config wins per key)
//	  policy.json           locked layer (applied above project config)
//	  env.json              {"KEY": "VALUE"} injected environment
//	  config/               org agents, commands, plugins and skills
//
// Later adapters follow the same pattern: one subdirectory per agent,
// named after the agent, with per-agent file names inside.
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
	// offline. A refresh that fails falls back to the cached copy and sets
	// FetchResult.RefreshErr.
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
	// RefreshErr is why a requested refresh failed; Dir then holds the
	// cached copy. Callers that must not run on a stale pack (a sync
	// command) return it as their error.
	RefreshErr error
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
// for reproducible launches. Each URL and ref pair gets its own cache
// directory, so several refs of one repo can be used side by side.
//
// The URL may carry a "#subdir" suffix (e.g. "github.com/acme/defaults#pack")
// to root the pack at a subdirectory of the repo, keeping room for a README
// and CI config at the top level.
func Git(url, ref string) Source {
	return GitWith(url, ref, GitOptions{})
}

// GitOptions configures a git-backed pack source. The zero value behaves
// like Git.
type GitOptions struct {
	// CacheDir is the directory that holds pack checkouts, one
	// subdirectory per URL and ref. Empty means
	// <user cache dir>/coding-agent-wrapper/git.
	CacheDir string
}

// GitWith is Git with options. It accepts the same URL forms as Git,
// including the "#subdir" suffix.
func GitWith(url, ref string, o GitOptions) Source {
	sub := ""
	if i := strings.Index(url, "#"); i >= 0 {
		url, sub = url[:i], url[i+1:]
	}
	root := o.CacheDir
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		root = filepath.Join(base, "coding-agent-wrapper", "git")
	}
	dir := filepath.Join(root, cacheKey(url+"\x00"+ref))
	return gitSource{url: url, ref: ref, dir: dir, sub: sub}
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

func cacheKey(key string) string {
	sum := sha1.Sum([]byte(key))
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

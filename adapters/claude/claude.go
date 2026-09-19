// Package claude adapts Claude Code to the wrapper.
//
// It layers an organization's defaults on top of a user's setup at launch
// time, using Claude Code's own injection points, so nothing on disk is
// modified:
//
//	pack file          injection point
//	-----------------  ------------------------------------------------
//	settings.json      deep-merged as the LOWEST layer
//	                     org defaults < user < project < local project
//	                   and passed via --settings <merged file>; user and
//	                   project values win per key, permission lists union
//	policy.json        same merge but as the TOP layer, above user and
//	                   project settings; wins per key (on by default,
//	                   --no-policy skips it)
//	mcp.json           --mcp-config <file>, added to the user's servers
//	                   (or replacing them with --strict-mcp-config)
//	env.json           environment variables; existing environment wins
//	                   unless policy mode is on (the default)
//	plugin/            --plugin-dir <dir>: org skills, agents, commands
//	                   and hooks as a Claude Code plugin
//	system-prompt.md   --append-system-prompt-file <file>
//
// See docs/config-layers.md for the layering model in depth.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wrapper "github.com/wejick/coding-agent-wrapper"
	"github.com/wejick/coding-agent-wrapper/adapters/internal/mergedfile"
	"github.com/wejick/coding-agent-wrapper/pack"
)

// Pack file and directory names, inside the pack's claude/ subdirectory.
const (
	DirName          = "claude"
	FileSettings     = "settings.json"
	FilePolicy       = "policy.json"
	FileMCP          = "mcp.json"
	FileEnv          = "env.json"
	FileSystemPrompt = "system-prompt.md"
	DirPlugin        = "plugin"
)

// unionArrayPaths are merged by union instead of override: a user's
// allowlist should keep the org's defaults and vice versa.
var unionArrayPaths = []string{
	"permissions.allow",
	"permissions.deny",
	"permissions.ask",
	"permissions.additionalDirectories",
}

// Adapter launches Claude Code.
type Adapter struct {
	// Binary is the agent command resolved on PATH; default "claude".
	Binary string
	// UserSettingsPath overrides ~/.claude/settings.json (for tests).
	UserSettingsPath string
	// ProjectDir overrides the project root whose .claude/ directory holds
	// project settings; default is the current working directory.
	ProjectDir string
	// CacheDir overrides where merged settings are written (for tests).
	CacheDir string
}

// New returns a Claude Code adapter with defaults.
func New() *Adapter { return &Adapter{} }

// Name implements Adapter.
func (a *Adapter) Name() string { return DirName }

// Locate implements Adapter.
func (a *Adapter) Locate() (string, error) {
	return wrapper.ResolveBinary(a.binaryName(), nil)
}

func (a *Adapter) binaryName() string {
	if a.Binary != "" {
		return a.Binary
	}
	return DirName
}

// Build implements Adapter. It computes the full launch for Claude
// Code from the pack, layering org defaults under user settings (and policy
// above them unless o.SkipPolicy).
func (a *Adapter) Build(ctx context.Context, p *pack.Pack, o wrapper.BuildOptions) (*wrapper.Launch, error) {
	launch := &wrapper.Launch{Notes: []string{}, Files: []string{}}
	binary, err := a.Locate()
	if err != nil {
		return nil, err
	}
	launch.Binary = binary

	base := o.Env
	if base == nil {
		base = os.Environ()
	}
	args := []string{}
	org := false

	// 1. Settings. The wrapper rebuilds the layering explicitly and passes
	// the result as one --settings file, inserting the org's defaults at
	// the very bottom and, when enforcing, the org's policy at the top:
	//
	//	org defaults < user < project < local project < org policy
	//
	// Higher layers win per key; permission lists union. See
	// docs/config-layers.md for the full model.
	var defaults, policy map[string]any
	hasDefaults, err := p.JSONFile(DirName, FileSettings, &defaults)
	if err != nil {
		return nil, err
	}
	hasPolicy := false
	policyPresent := false
	if _, ok := p.File(DirName, FilePolicy); ok {
		policyPresent = true
		if !o.SkipPolicy {
			if hasPolicy, err = p.JSONFile(DirName, FilePolicy, &policy); err != nil {
				return nil, err
			}
		}
	}

	rules := pack.MergeRules{UnionArrays: unionArrayPaths}
	var merged any = map[string]any{}
	labels := []string{}
	if hasDefaults {
		merged = pack.MergeJSON(merged, defaults, rules)
		labels = append(labels, "org defaults")
	}
	if user := a.userSettings(); user.corrupt != "" {
		launch.Notes = append(launch.Notes, fmt.Sprintf("user settings %s could not be parsed (%s); org defaults may override it", user.path, user.corrupt))
	} else if user.ok {
		merged = pack.MergeJSON(merged, user.value, rules)
		labels = append(labels, "user "+user.path)
	}
	for _, sf := range a.projectSettings() {
		if sf.corrupt != "" {
			launch.Notes = append(launch.Notes, fmt.Sprintf("%s settings %s could not be parsed (%s); org defaults may override it", sf.label, sf.path, sf.corrupt))
			continue
		}
		if !sf.ok {
			continue
		}
		merged = pack.MergeJSON(merged, sf.value, rules)
		labels = append(labels, sf.label+" "+sf.path)
	}
	if policyPresent && o.SkipPolicy {
		launch.Notes = append(launch.Notes, "policy.json present but skipped (--no-policy); soft defaults only")
	}
	if hasPolicy {
		merged = pack.MergeJSON(merged, policy, rules)
		labels = append(labels, "org policy (locked)")
	}

	if hasDefaults || hasPolicy {
		org = true
		path, err := a.writeMergedSettings(merged)
		if err != nil {
			return nil, err
		}
		launch.Files = append(launch.Files, path)
		args = append(args, "--settings", path)
		if len(labels) > 1 {
			launch.Notes = append(launch.Notes, "settings layers (low → high): "+strings.Join(labels, " < "))
		}
		if hasPolicy {
			launch.Notes = append(launch.Notes, fmt.Sprintf("policy %s applied over every layer (user and project settings cannot override it)", FilePolicy))
		}
	}

	// 2. MCP servers.
	if mcpPath, ok := p.File(DirName, FileMCP); ok {
		org = true
		args = append(args, "--mcp-config", mcpPath)
		if o.StrictMCP {
			args = append(args, "--strict-mcp-config")
			launch.Notes = append(launch.Notes, "org MCP config REPLACES user MCP servers (--strict-mcp-config)")
		} else {
			launch.Notes = append(launch.Notes, "org MCP config added on top of user MCP servers")
		}
	}

	// 3. Org plugin: skills, agents, commands, hooks.
	if pluginDir, ok := p.Subdir(DirName, DirPlugin); ok {
		org = true
		args = append(args, "--plugin-dir", pluginDir)
		launch.Notes = append(launch.Notes, fmt.Sprintf("org plugin loaded from %s", pluginDir))
	}

	// 4. System prompt append.
	if promptPath, ok := p.File(DirName, FileSystemPrompt); ok {
		org = true
		args = append(args, "--append-system-prompt-file", promptPath)
		launch.Notes = append(launch.Notes, fmt.Sprintf("org system prompt appended from %s", promptPath))
	}

	// 5. Environment.
	var envDefaults map[string]string
	hasEnv, err := p.JSONFile(DirName, FileEnv, &envDefaults)
	if err != nil {
		return nil, err
	}
	if hasEnv {
		org = true
		merged, notes := pack.MergeEnv(base, envDefaults, !o.SkipPolicy)
		launch.Notes = append(launch.Notes, notes...)
		launch.Env = merged
	} else {
		launch.Env = base
	}

	// 6. User args last so they still apply.
	launch.Args = append(args, o.Args...)

	if !org {
		launch.Notes = append(launch.Notes, fmt.Sprintf("pack contains no %s defaults; launching vanilla %s", DirName, a.binaryName()))
	}
	return launch, nil
}

// settingsFile is one settings layer on disk.
type settingsFile struct {
	label   string
	path    string
	value   map[string]any
	ok      bool
	corrupt string
}

func (a *Adapter) readSettings(label, path string) settingsFile {
	var v map[string]any
	state, err := mergedfile.ReadJSON(path, &v)
	if state == mergedfile.Corrupt {
		return settingsFile{label: label, path: path, corrupt: err.Error()}
	}
	if state != mergedfile.Loaded {
		return settingsFile{label: label, path: path}
	}
	return settingsFile{label: label, path: path, value: v, ok: true}
}

// userSettings loads ~/.claude/settings.json (or the override path).
func (a *Adapter) userSettings() settingsFile {
	path := a.UserSettingsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return settingsFile{}
		}
		path = filepath.Join(home, ".claude", FileSettings)
	}
	return a.readSettings("user", path)
}

// projectSettings loads the native Claude Code project layers from the
// project's .claude/ directory: the shared settings.json and the personal,
// usually gitignored settings.local.json.
func (a *Adapter) projectSettings() []settingsFile {
	dir := a.ProjectDir
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil
		}
	}
	return []settingsFile{
		a.readSettings("project", filepath.Join(dir, ".claude", FileSettings)),
		a.readSettings("local project", filepath.Join(dir, ".claude", "settings.local.json")),
	}
}

func (a *Adapter) writeMergedSettings(merged any) (string, error) {
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	// The file name comes from its content, so concurrent launches with
	// identical settings share one path and different settings never
	// overwrite each other between write and exec.
	return mergedfile.Write(a.cacheDir(), "settings", data)
}

func (a *Adapter) cacheDir() string {
	if a.CacheDir != "" {
		return a.CacheDir
	}
	return mergedfile.CacheDir(DirName)
}

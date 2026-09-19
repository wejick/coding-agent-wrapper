// Package opencode adapts OpenCode to the wrapper.
//
// It layers an organization's defaults on top of a user's setup at launch
// time through OpenCode's own config sources, so nothing on disk is
// modified:
//
//	pack file          injection point
//	-----------------  ------------------------------------------------
//	settings.json      deep-merged as the LOWEST layer
//	                     org defaults < user global config <
//	                     the user's own OPENCODE_CONFIG
//	                   and passed via OPENCODE_CONFIG <generated file>;
//	                   OpenCode merges project config above it
//	policy.json        passed via OPENCODE_CONFIG_CONTENT, which
//	                   OpenCode merges above project config; wins per
//	                   key (on by default, --no-policy skips it)
//	env.json           environment variables; existing environment wins
//	                   unless policy mode is on (the default)
//	config/            OPENCODE_CONFIG_DIR: org agents, commands,
//	                   plugins, skills, tools and themes, loaded like
//	                   a project .opencode directory
//
// Effective config order for a wrapped launch:
//
//	org defaults < user global < user's own OPENCODE_CONFIG
//	  < project opencode.json < org policy
//
// Nothing is injected into argv: every injection point is an environment
// variable, so the user's arguments pass through untouched. The user's
// global config is read (JSON and JSONC) and merged UNDER the org
// defaults because a bare OPENCODE_CONFIG file would sit above it and let
// org values shadow user choices. The user's own OPENCODE_CONFIG, when
// set, is merged in the same way and the variable is repointed at the
// generated file.
//
// See docs/config-layers.md for the layering model in depth.
package opencode

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

// Pack file and directory names, inside the pack's opencode/ subdirectory,
// and the environment variables used to inject them.
const (
	DirName      = "opencode"
	FileSettings = "settings.json"
	FilePolicy   = "policy.json"
	FileEnv      = "env.json"
	DirConfig    = "config"

	EnvConfig        = "OPENCODE_CONFIG"
	EnvConfigDir     = "OPENCODE_CONFIG_DIR"
	EnvConfigContent = "OPENCODE_CONFIG_CONTENT"
)

// unionArrayPaths are merged by union instead of override: org
// instructions and npm plugins should add to the user's, not replace them.
var unionArrayPaths = []string{
	"instructions",
	"plugin",
}

// Adapter launches OpenCode.
type Adapter struct {
	// Binary is the agent command resolved on PATH; default "opencode".
	// Point it at a differently named build (for example "opencode2") if
	// that is how the agent is installed.
	Binary string
	// UserConfigPath overrides the user's global config file (for tests).
	// When empty, the adapter reads config.json, opencode.json and
	// opencode.jsonc from the OpenCode global config directory.
	UserConfigPath string
	// CacheDir overrides where generated config files are written (for
	// tests).
	CacheDir string
}

// New returns an OpenCode adapter with defaults.
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

// Build implements Adapter. It computes the full launch for OpenCode from
// the pack, layering org defaults under the user's config (and policy
// above it unless o.SkipPolicy).
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
	envs := base
	org := false

	// 1. Defaults. The wrapper rebuilds the layering explicitly and passes
	// the result as one OPENCODE_CONFIG file:
	//
	//	org defaults < user global config < the user's own OPENCODE_CONFIG
	//
	// OpenCode then merges project config above the file on its own.
	// Higher layers win per key; instructions and plugin lists union.
	var defaults map[string]any
	hasDefaults, err := p.JSONFile(DirName, FileSettings, &defaults)
	if err != nil {
		return nil, err
	}
	if hasDefaults {
		org = true
		rules := pack.MergeRules{UnionArrays: unionArrayPaths}
		merged := pack.MergeJSON(map[string]any{}, defaults, rules).(map[string]any)
		labels := []string{"org defaults"}
		for _, path := range a.globalConfigFiles() {
			var one map[string]any
			state, err := mergedfile.ReadJSON(path, &one)
			if state == mergedfile.Corrupt {
				launch.Notes = append(launch.Notes, fmt.Sprintf("global config %s could not be parsed (%s); org defaults may override it", path, err))
				continue
			}
			if state != mergedfile.Loaded {
				continue
			}
			merged = pack.MergeJSON(merged, one, rules).(map[string]any)
			labels = append(labels, "user global "+path)
		}
		if inherited, ok := envLookup(envs, EnvConfig); ok {
			var one map[string]any
			state, err := mergedfile.ReadJSON(inherited, &one)
			switch state {
			case mergedfile.Corrupt:
				launch.Notes = append(launch.Notes, fmt.Sprintf("%s %s could not be parsed (%s); org defaults may override it", EnvConfig, inherited, err))
			case mergedfile.Loaded:
				merged = pack.MergeJSON(merged, one, rules).(map[string]any)
				labels = append(labels, "user "+EnvConfig+" "+inherited)
			}
		}
		data, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return nil, err
		}
		data = append(data, '\n')
		path, err := mergedfile.Write(a.cacheDir(), "settings", data)
		if err != nil {
			return nil, err
		}
		launch.Files = append(launch.Files, path)
		var replaced string
		envs, replaced = setEnv(envs, EnvConfig, path)
		if replaced != "" {
			launch.Notes = append(launch.Notes, replaced)
		}
		if len(labels) > 1 {
			launch.Notes = append(launch.Notes, "settings layers (low → high): "+strings.Join(labels, " < "))
		}
	}

	// 2. Policy. OPENCODE_CONFIG_CONTENT is merged by OpenCode above the
	// project config, so policy beats everything the user can edit.
	var policy map[string]any
	if _, ok := p.File(DirName, FilePolicy); ok {
		if o.SkipPolicy {
			launch.Notes = append(launch.Notes, "policy.json present but skipped (--no-policy); soft defaults only")
		} else {
			hasPolicy, err := p.JSONFile(DirName, FilePolicy, &policy)
			if err != nil {
				return nil, err
			}
			if hasPolicy {
				org = true
				data, err := json.Marshal(policy)
				if err != nil {
					return nil, err
				}
				var replaced string
				envs, replaced = setEnv(envs, EnvConfigContent, string(data))
				if replaced != "" {
					launch.Notes = append(launch.Notes, replaced)
				}
				launch.Notes = append(launch.Notes, fmt.Sprintf("policy %s applied above project config (user and project settings cannot override it)", FilePolicy))
			}
		}
	}

	// 3. Org config dir: agents, commands, plugins, skills, tools, themes.
	if dir, ok := p.Subdir(DirName, DirConfig); ok {
		org = true
		var replaced string
		envs, replaced = setEnv(envs, EnvConfigDir, dir)
		if replaced != "" {
			launch.Notes = append(launch.Notes, replaced)
		}
		launch.Notes = append(launch.Notes, fmt.Sprintf("org config dir loaded from %s (agents, commands, plugins, skills)", dir))
	}

	// 4. StrictMCP is Claude-specific; OpenCode has no equivalent switch.
	if o.StrictMCP {
		launch.Notes = append(launch.Notes, "StrictMCP has no OpenCode equivalent; org config keys extend the user's MCP servers")
	}

	// 5. Environment.
	var envDefaults map[string]string
	hasEnv, err := p.JSONFile(DirName, FileEnv, &envDefaults)
	if err != nil {
		return nil, err
	}
	if hasEnv {
		org = true
		mergedEnv, notes := pack.MergeEnv(envs, envDefaults, !o.SkipPolicy)
		launch.Notes = append(launch.Notes, notes...)
		envs = mergedEnv
	}
	launch.Env = envs

	// 6. User args pass through untouched; every injection is an env var.
	launch.Args = append([]string{}, o.Args...)

	if !org {
		launch.Notes = append(launch.Notes, fmt.Sprintf("pack contains no %s defaults; launching vanilla %s", DirName, a.binaryName()))
	}
	return launch, nil
}

// globalConfigFiles lists the user's global config files in OpenCode's own
// load order. Tests override the whole list with UserConfigPath.
func (a *Adapter) globalConfigFiles() []string {
	if a.UserConfigPath != "" {
		return []string{a.UserConfigPath}
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, DirName)
	return []string{
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "opencode.json"),
		filepath.Join(dir, "opencode.jsonc"),
	}
}

func (a *Adapter) cacheDir() string {
	if a.CacheDir != "" {
		return a.CacheDir
	}
	return mergedfile.CacheDir(DirName)
}

// envLookup returns the value of key in env (a KEY=VALUE list).
func envLookup(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// setEnv returns env with key set to value, replacing an existing entry,
// plus a note when an inherited value was replaced.
func setEnv(env []string, key, value string) ([]string, string) {
	for i, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			out := append([]string{}, env...)
			out[i] = key + "=" + value
			return out, fmt.Sprintf("env %s replaced inherited %q", key, v)
		}
	}
	return append(env, key+"="+value), ""
}

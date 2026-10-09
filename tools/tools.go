// Package tools reads the tools a defaults pack requires, checks them
// against the user's machine, and plans and runs their installs.
//
// A pack declares its tools in tools.json at the pack root:
//
//	{
//	  "node":     { "min_version": "20.0.0" },
//	  "openspec": { "min_version": "1.10.0",
//	                "install": { "manager": "npm", "package": "@fission-ai/openspec" } }
//	}
//
// Each key names a tool. The tool is found on PATH by its command (the key
// unless "command" is set) and its version is the first MAJOR.MINOR.PATCH
// in the output of `<command> --version`. A tool without "install" is only
// checked. A tool with "install" is installed at exactly min_version with
// the named package manager; the package manager and its command line come
// from this package, so the pack chooses what to install but not which
// commands run.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FileName is the name of the requirements file at the pack root.
const FileName = "tools.json"

// Requirement is one tool the pack requires.
type Requirement struct {
	// Name is the tool's key in tools.json.
	Name string `json:"name"`
	// Command is the executable looked up on PATH.
	Command string `json:"command"`
	// MinVersion is the lowest accepted version, MAJOR.MINOR.PATCH.
	MinVersion string `json:"min_version"`
	// Install says how to install the tool; nil means check only.
	Install *Install `json:"install,omitempty"`
}

// Install names the package manager and package that provide a tool.
type Install struct {
	// Manager is one of the managers this package supports: "npm",
	// "pnpm", "bun" or "go".
	Manager string `json:"manager"`
	// Package is the package to install, without a version.
	Package string `json:"package"`
}

// Load reads FileName from the pack directory dir. A missing file means
// the pack requires no tools and returns nil without error. Requirements
// are returned sorted by name. Unknown fields are ignored so packs can use
// fields added by newer versions of this package.
func Load(dir string) ([]Requirement, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses the content of a tools.json file. See Load.
func Parse(data []byte) ([]Requirement, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	reqs := make([]Requirement, 0, len(raw))
	for name, msg := range raw {
		var entry struct {
			Command    string   `json:"command"`
			MinVersion string   `json:"min_version"`
			Install    *Install `json:"install"`
		}
		if err := json.Unmarshal(msg, &entry); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", FileName, name, err)
		}
		r := Requirement{Name: name, Command: entry.Command, MinVersion: entry.MinVersion, Install: entry.Install}
		if r.Command == "" {
			r.Command = name
		}
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", FileName, name, err)
		}
		reqs = append(reqs, r)
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Name < reqs[j].Name })
	return reqs, nil
}

func (r Requirement) validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("the tool name is empty")
	}
	if !bareWord(r.Command) || strings.ContainsAny(r.Command, `/\`) {
		return fmt.Errorf("command %q must be a plain executable name", r.Command)
	}
	if r.MinVersion == "" {
		return errors.New("min_version is required")
	}
	if _, ok := parseStrict(r.MinVersion); !ok {
		return fmt.Errorf("min_version %q must be MAJOR.MINOR.PATCH, such as 1.10.0", r.MinVersion)
	}
	if r.Install == nil {
		return nil
	}
	if r.Install.Manager == "" {
		return errors.New("install.manager is required")
	}
	if !bareWord(r.Install.Package) {
		return fmt.Errorf("install.package %q must be a package name", r.Install.Package)
	}
	if strings.Contains(r.Install.Package[1:], "@") {
		return fmt.Errorf("install.package %q must not carry a version; min_version sets it", r.Install.Package)
	}
	return nil
}

// bareWord reports whether s is non-empty, has no whitespace or control
// characters, and cannot be read as a command-line flag.
func bareWord(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, c := range s {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// version is a parsed MAJOR.MINOR.PATCH.
type version [3]int

var (
	strictVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	// looseVersion finds a version in tool output: MAJOR.MINOR.PATCH with
	// an optional v and prerelease suffix, not glued to a word, so
	// "go1.22.3" in a build banner is not read as the tool's version.
	looseVersion = regexp.MustCompile(`(?:^|[^0-9A-Za-z.])[vV]?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)`)
)

func parseStrict(s string) (version, bool) {
	m := strictVersion.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	return toVersion(m[1:])
}

// findVersion returns the first version in a tool's output, such as
// "1.10.0" or "1.10.0-rc.1".
func findVersion(out string) (string, bool) {
	m := looseVersion.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func toVersion(parts []string) (version, bool) {
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v[i] = n
	}
	return v, true
}

// atLeast reports whether have >= min. have is a version found by
// findVersion; a prerelease such as 1.10.0-rc.1 is lower than 1.10.0. An
// unparsable have is never enough.
func atLeast(have, min string) bool {
	core, pre, _ := strings.Cut(have, "-")
	h, ok := toVersion(strings.SplitN(core, ".", 3))
	if !ok {
		return false
	}
	m, ok := parseStrict(min)
	if !ok {
		return false
	}
	for i := range h {
		if h[i] != m[i] {
			return h[i] > m[i]
		}
	}
	return pre == ""
}

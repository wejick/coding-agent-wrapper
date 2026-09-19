// Package mergedfile holds launch-file plumbing shared by adapters:
// content-addressed generated config files under the user's cache dir, and
// tolerant reads of the user's existing config (JSON or JSONC).
//
// Generated files are named after their content, so two launches that
// would produce identical config share one path safely, and launches with
// different config can never overwrite each other's file between write and
// exec. Paths stay deterministic, so nothing needs cleaning up after an
// in-place exec.
package mergedfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Write stores data under dir as <prefix>-<sha256>.json and returns the
// final path. The write is atomic, and files older than 30 days are pruned
// afterwards. Best effort pruning never fails the launch.
func Write(dir, prefix string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := prefix + "-" + hex.EncodeToString(sum[:6]) + ".json"
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}
	prune(dir, prefix, name)
	return final, nil
}

// prune deletes generated files of the form <prefix>-*.json under dir that
// no launch has referenced for a month. Best effort: cleanup must never
// fail a launch.
func prune(dir, prefix, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for _, entry := range entries {
		name := entry.Name()
		hashed := strings.HasPrefix(name, prefix+"-") && strings.HasSuffix(name, ".json")
		if name == keep || !hashed {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}

// CacheDir returns the cache directory for adapter name's generated files:
// <user cache dir>/coding-agent-wrapper/merge/<name>, falling back to the
// system temp dir when the user cache dir is unavailable.
func CacheDir(name string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "coding-agent-wrapper", "merge", name)
}

// State is the outcome of reading a config file.
type State int

const (
	// NotFound means the file does not exist.
	NotFound State = iota
	// Loaded means the file was read and parsed into v.
	Loaded
	// Corrupt means the file exists but could not be read or parsed; the
	// returned error carries the reason.
	Corrupt
)

// ReadJSON reads path into v. JSONC input (// and /* */ comments and
// trailing commas) is normalized before parsing, so configs written for
// OpenCode load like any other JSON. On Corrupt, v may hold partially
// parsed data and must be discarded by the caller.
func ReadJSON(path string, v any) (State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NotFound, nil
	}
	if err != nil {
		return Corrupt, fmt.Errorf("%s: %w", path, err)
	}
	stripped := stripTrailingCommas(stripJSONComments(data))
	if err := json.Unmarshal(stripped, v); err != nil {
		return Corrupt, fmt.Errorf("%s: %w", path, err)
	}
	return Loaded, nil
}

// stripJSONComments removes // line comments and /* */ block comments from
// JSONC input, respecting string literals. Trailing input that does not
// parse afterwards surfaces as a Corrupt read.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escape := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++ // move onto the closing '/'
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripTrailingCommas drops commas that are directly followed by a closing
// brace or bracket, the way JSONC writers and OpenCode's own examples use
// them. String literals are left untouched.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escape := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == ',':
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

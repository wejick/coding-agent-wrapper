package wrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveBinary resolves name against the PATH entries in env (os.Environ
// when nil). Entries that resolve to the running executable itself are
// skipped, so a wrapper installed on PATH under an agent's name does not
// recurse into itself. A name containing a slash is treated as a path.
func ResolveBinary(name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		abs, err := filepath.Abs(name)
		if err != nil {
			abs = name
		}
		if fileExecutable(abs) {
			return abs, nil
		}
		return "", fmt.Errorf("wrapper: %s: not found or not executable", name)
	}
	if env == nil {
		env = os.Environ()
	}
	self, _ := os.Executable()
	if self != "" {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
	}
	var searched []string
	for _, dir := range SearchPath(env) {
		searched = append(searched, dir)
		candidate := filepath.Join(dir, name)
		if !fileExecutable(candidate) {
			continue
		}
		if self != "" {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err == nil && resolved == self {
				continue
			}
		}
		return candidate, nil
	}
	return "", fmt.Errorf("wrapper: %q not found on PATH (searched: %s)", name, strings.Join(searched, string(filepath.ListSeparator)))
}

// SearchPath extracts the PATH directories from a KEY=VALUE environment.
func SearchPath(env []string) []string {
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && key == "PATH" {
			return filepath.SplitList(value)
		}
	}
	return nil
}

func fileExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

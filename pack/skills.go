package pack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Skill is read from a SKILL.md frontmatter.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// SkillDirs implements wrapper.Skills over directories of SKILL.md files.
type SkillDirs []string

func (d SkillDirs) List() ([]Skill, error) {
	var skills []Skill
	for _, dir := range d {
		found, err := readSkills(dir)
		if err != nil {
			return nil, err
		}
		skills = append(skills, found...)
	}
	return skills, nil
}

// readSkills finds every SKILL.md under dir, following symlinks and
// skipping hidden entries.
func readSkills(dir string) ([]Skill, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var skills []Skill
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if err := walk(path); err != nil {
					return err
				}
				continue
			}
			if e.Name() != "SKILL.md" {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fm := frontmatter(string(data))
			skills = append(skills, Skill{Name: fm["name"], Description: fm["description"], Path: path})
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	return skills, nil
}

// frontmatter returns the top-level keys of a leading "---" YAML block.
func frontmatter(doc string) map[string]string {
	doc = strings.ReplaceAll(strings.TrimPrefix(doc, "\ufeff"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(doc, "---\n")
	if !ok {
		return nil
	}
	block, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return nil
	}
	return parseYAML(block)
}

// parseYAML reads "key: value" lines. Indented lines continue the previous
// value, joined with spaces, which also covers "|" and ">" blocks.
func parseYAML(block string) map[string]string {
	out := map[string]string{}
	key := ""
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || strings.HasPrefix(line, "#"):
		case line[0] == ' ' || line[0] == '\t':
			if key != "" {
				out[key] = strings.TrimSpace(out[key] + " " + trimmed)
			}
		default:
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				key = ""
				continue
			}
			key = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
				v = ""
			} else if i := strings.Index(v, " #"); i >= 0 && !strings.ContainsAny(v[:1], `"'`) {
				v = strings.TrimSpace(v[:i])
			}
			out[key] = unquote(v)
		}
	}
	return out
}

func unquote(s string) string {
	if len(s) < 2 || s[0] != s[len(s)-1] {
		return s
	}
	switch s[0] {
	case '"':
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	case '\'':
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

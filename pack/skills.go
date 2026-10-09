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

// frontmatter parses the top-level keys of a leading "---" YAML block.
func frontmatter(doc string) map[string]string {
	doc = strings.TrimPrefix(doc, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	out := map[string]string{}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return out
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		var more []string
		for i+1 < len(lines) && (strings.TrimSpace(lines[i+1]) == "" || lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
			i++
			more = append(more, strings.TrimSpace(lines[i]))
		}
		switch {
		case strings.HasPrefix(value, "|"):
			value = strings.TrimSpace(strings.Join(more, "\n"))
		case strings.HasPrefix(value, ">"):
			value = strings.Join(strings.Fields(strings.Join(more, " ")), " ")
		default:
			if !strings.HasPrefix(value, "\"") && !strings.HasPrefix(value, "'") {
				if i := strings.Index(value, " #"); i >= 0 {
					value = strings.TrimSpace(value[:i])
				}
			}
			if len(more) > 0 {
				value = strings.Join(strings.Fields(value+" "+strings.Join(more, " ")), " ")
			}
			value = unquote(value)
		}
		out[strings.TrimSpace(key)] = value
	}
	return nil
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

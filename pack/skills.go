package pack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Skill is one skill a pack provides, read from its SKILL.md.
type Skill struct {
	// Name and Description come from the SKILL.md frontmatter. Each is
	// empty when the frontmatter does not set it.
	Name        string `json:"name"`
	Description string `json:"description"`
	// Path is the skill's SKILL.md file.
	Path string `json:"path"`
}

// SkillDirs is the list of directories an agent loads SKILL.md skills
// from. It implements wrapper.Skills, so an adapter whose agent uses
// SKILL.md files returns one from Adapter.Skills.
type SkillDirs []string

// List returns the skills in every directory, in directory order. A
// missing directory has no skills.
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

// readSkills lists the skills under dir: every SKILL.md at any depth, the
// way OpenCode's "skills/**/SKILL.md" scan finds them, so skills can be
// grouped in subdirectories (skills/team/review/SKILL.md). Each SKILL.md
// opens with YAML frontmatter. Symlinked directories are followed, hidden
// entries are skipped, and a missing dir has no skills. Skills come back
// in path order.
func readSkills(dir string) ([]Skill, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var skills []Skill
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		// A symlink loop would otherwise recurse forever.
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
			info, err := os.Stat(path) // follows symlinks
			if err != nil {
				continue // dangling symlink
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

// frontmatter reads the top-level "key: value" pairs of a leading
// "---"-delimited YAML block. It covers what skill files use: plain and
// quoted scalars, values continued on indented lines, and "|" and ">"
// block scalars. It returns nil when there is no complete block.
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
		// Indented lines that follow belong to this value.
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
			// " #" starts a comment in a plain scalar.
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

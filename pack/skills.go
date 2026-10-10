package pack

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Skill is read from a SKILL.md frontmatter.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	// NameFromDir is true when the frontmatter has no name and Name was
	// taken from the skill's directory (DirNamedSkillDirs only), so a
	// pack's CI can still find skills without a frontmatter name.
	NameFromDir bool `json:"name_from_dir,omitempty"`
}

// SkillDirs implements wrapper.Skills over directories of SKILL.md files,
// for agents that need a frontmatter name. Name is empty when the
// frontmatter does not set one.
type SkillDirs []string

// List returns the skills in every directory, sorted by Name and then by
// Path. A skill directory reached twice, through a symlink or two
// overlapping directories, is listed once, under the path found first in
// directory order. With no skills it returns an
// empty, non-nil slice.
func (d SkillDirs) List() ([]Skill, error) {
	return listSkills(d, false)
}

// DirNamedSkillDirs is SkillDirs for agents that name a skill after its
// directory when the frontmatter has no name, as Claude Code does:
// skills/review/SKILL.md without a name key is listed as review, with
// NameFromDir set. A SKILL.md directly in one of the directories keeps an
// empty Name, since the agent does not load it as a named skill.
type DirNamedSkillDirs []string

// List behaves like SkillDirs.List, with the directory name fallback.
func (d DirNamedSkillDirs) List() ([]Skill, error) {
	return listSkills(d, true)
}

func listSkills(dirs []string, nameFromDir bool) ([]Skill, error) {
	skills := []Skill{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		found, err := readSkills(dir, seen, nameFromDir)
		if err != nil {
			return nil, err
		}
		skills = append(skills, found...)
	}
	slices.SortFunc(skills, func(a, b Skill) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Path, b.Path))
	})
	return skills, nil
}

// readSkills finds the skill directories under root, following symlinks
// and skipping hidden entries and directories already in seen. It does
// not look inside a skill directory for more skills. A whitespace-only
// name counts as missing; with nameFromDir, a missing name below root is
// replaced by the skill's directory name.
func readSkills(root string, seen map[string]bool, nameFromDir bool) ([]Skill, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var skills []Skill
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
		path := filepath.Join(dir, "SKILL.md")
		if data, err := os.ReadFile(path); err == nil {
			fm := frontmatter(string(data))
			skill := Skill{Name: fm["name"], Description: fm["description"], Path: path}
			if strings.TrimSpace(skill.Name) == "" {
				skill.Name = ""
				if nameFromDir && dir != root {
					skill.Name, skill.NameFromDir = filepath.Base(dir), true
				}
			}
			skills = append(skills, skill)
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			sub := filepath.Join(dir, e.Name())
			if info, err := os.Stat(sub); err == nil && info.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if err := walk(sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
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

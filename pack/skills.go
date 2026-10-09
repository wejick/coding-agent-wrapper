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

// ReadSkills lists the skills in dir: one subdirectory per skill, each
// holding a SKILL.md that opens with YAML frontmatter. Claude Code plugins
// and OpenCode config directories share this layout. Subdirectories
// without a SKILL.md are skipped, and a missing dir has no skills. Skills
// are sorted by directory name.
func ReadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fm := frontmatter(string(data))
		skills = append(skills, Skill{Name: fm["name"], Description: fm["description"], Path: path})
	}
	return skills, nil
}

// frontmatter reads the top-level "key: value" pairs of a leading
// "---"-delimited YAML block. It covers what skill files use: plain and
// quoted scalars, values continued on indented lines, and "|" and ">"
// block scalars. It returns nil when there is no complete block.
func frontmatter(doc string) map[string]string {
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

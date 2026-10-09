package pack

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadSkills(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plain", "---\nname: plain\ndescription: Plain description.\n---\n\nBody.\n")
	write("quoted", "---\nname: \"quoted\"\ndescription: 'It''s quoted: with a colon'\n---\n")
	write("folded", "---\nname: folded\ndescription: >\n  First line\n  second line.\nallowed-tools: Read\n---\n")
	write("continued", "---\r\nname: continued\r\ndescription: Starts here\r\n  and continues.\r\n---\r\n")
	write("nometa", "# No frontmatter\n")
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Skill{
		{Name: "continued", Description: "Starts here and continues.", Path: filepath.Join(dir, "continued", "SKILL.md")},
		{Name: "folded", Description: "First line second line.", Path: filepath.Join(dir, "folded", "SKILL.md")},
		{Path: filepath.Join(dir, "nometa", "SKILL.md")},
		{Name: "plain", Description: "Plain description.", Path: filepath.Join(dir, "plain", "SKILL.md")},
		{Name: "quoted", Description: "It's quoted: with a colon", Path: filepath.Join(dir, "quoted", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadSkillsMissingDir(t *testing.T) {
	got, err := ReadSkills(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want no skills and no error", got, err)
	}
}

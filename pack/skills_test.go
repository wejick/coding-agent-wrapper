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
	write("bom", "\ufeff---\nname: bom # saved by a Windows editor\ndescription: Has a BOM.\n---\n")
	write("team/nested", "---\nname: nested\ndescription: Grouped under team/.\n---\n")
	write("plain/templates", "---\nname: template\n---\n")
	write(".hidden", "---\nname: hidden\n---\n")
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("---\nname: linked\ndescription: Shared.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "team", "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SkillDirs{dir}.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Skill{
		{Name: "bom", Description: "Has a BOM.", Path: filepath.Join(dir, "bom", "SKILL.md")},
		{Name: "continued", Description: "Starts here and continues.", Path: filepath.Join(dir, "continued", "SKILL.md")},
		{Name: "folded", Description: "First line second line.", Path: filepath.Join(dir, "folded", "SKILL.md")},
		{Name: "linked", Description: "Shared.", Path: filepath.Join(dir, "linked", "SKILL.md")},
		{Name: "nested", Description: "Grouped under team/.", Path: filepath.Join(dir, "team", "nested", "SKILL.md")},
		{Name: "nometa", Path: filepath.Join(dir, "nometa", "SKILL.md")},
		{Name: "plain", Description: "Plain description.", Path: filepath.Join(dir, "plain", "SKILL.md")},
		{Name: "quoted", Description: "It's quoted: with a colon", Path: filepath.Join(dir, "quoted", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSkillDirsListSortsAcrossDirs(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		path = filepath.Join(root, path, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b/zeta", "---\nname: zeta\n---\n")
	write("b/alpha", "---\nname: shared\n---\n")
	write("a/review", "---\ndescription: No name, named after its directory.\n---\n")
	write("a/x", "---\nname: shared\n---\n")

	got, err := SkillDirs{filepath.Join(root, "b"), filepath.Join(root, "a")}.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Skill{
		{Name: "review", Description: "No name, named after its directory.", Path: filepath.Join(root, "a", "review", "SKILL.md")},
		{Name: "shared", Path: filepath.Join(root, "a", "x", "SKILL.md")},
		{Name: "shared", Path: filepath.Join(root, "b", "alpha", "SKILL.md")},
		{Name: "zeta", Path: filepath.Join(root, "b", "zeta", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadSkillsMissingDir(t *testing.T) {
	got, err := readSkills(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want no skills and no error", got, err)
	}
}

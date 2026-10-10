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
	write("nokey", "---\ndescription: No name key.\n---\n")
	write("blank", "---\nname: \" \"\ndescription: Blank name.\n---\n")
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
		{Description: "Blank name.", Path: filepath.Join(dir, "blank", "SKILL.md")},
		{Description: "No name key.", Path: filepath.Join(dir, "nokey", "SKILL.md")},
		{Path: filepath.Join(dir, "nometa", "SKILL.md")},
		{Name: "bom", Description: "Has a BOM.", Path: filepath.Join(dir, "bom", "SKILL.md")},
		{Name: "continued", Description: "Starts here and continues.", Path: filepath.Join(dir, "continued", "SKILL.md")},
		{Name: "folded", Description: "First line second line.", Path: filepath.Join(dir, "folded", "SKILL.md")},
		{Name: "linked", Description: "Shared.", Path: filepath.Join(dir, "linked", "SKILL.md")},
		{Name: "nested", Description: "Grouped under team/.", Path: filepath.Join(dir, "team", "nested", "SKILL.md")},
		{Name: "plain", Description: "Plain description.", Path: filepath.Join(dir, "plain", "SKILL.md")},
		{Name: "quoted", Description: "It's quoted: with a colon", Path: filepath.Join(dir, "quoted", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSkillDirsListSortsAcrossDirs(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write := func(root, name, skill string) {
		t.Helper()
		path := filepath.Join(root, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: "+skill+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "x", "review")
	write(a, "y", "commit-style")
	write(b, "z", "commit-style")
	write(b, "a", "lint")
	if err := os.Symlink(filepath.Join(a, "x"), filepath.Join(b, "review-link")); err != nil {
		t.Fatal(err)
	}

	got, err := SkillDirs{b, a}.List()
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(a, "y", "SKILL.md"), filepath.Join(b, "z", "SKILL.md")
	if second < first {
		first, second = second, first
	}
	want := []Skill{
		{Name: "commit-style", Path: first},
		{Name: "commit-style", Path: second},
		{Name: "lint", Path: filepath.Join(b, "a", "SKILL.md")},
		// a/x is also reached as b/review-link; b is listed first, so its
		// path is the one kept.
		{Name: "review", Path: filepath.Join(b, "review-link", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDirNamedSkillDirsNamesSkillsAfterDir(t *testing.T) {
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
	write("", "# At the root, not a named skill\n")
	root, err := DirNamedSkillDirs{dir}.List()
	if err != nil {
		t.Fatal(err)
	}
	if want := []Skill{{Path: filepath.Join(dir, "SKILL.md")}}; !reflect.DeepEqual(root, want) {
		t.Fatalf("root skills = %+v, want %+v", root, want)
	}

	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	write("nometa", "# No frontmatter\n")
	write("nokey", "---\ndescription: No name key.\n---\n")
	write("blank", "---\nname: \" \"\n---\n")
	write("named", "---\nname: other\n---\n")
	got, err := DirNamedSkillDirs{dir}.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Skill{
		{Name: "blank", Path: filepath.Join(dir, "blank", "SKILL.md"), NameFromDir: true},
		{Name: "nokey", Description: "No name key.", Path: filepath.Join(dir, "nokey", "SKILL.md"), NameFromDir: true},
		{Name: "nometa", Path: filepath.Join(dir, "nometa", "SKILL.md"), NameFromDir: true},
		{Name: "other", Path: filepath.Join(dir, "named", "SKILL.md")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skills =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSkillDirsListEmpty(t *testing.T) {
	got, err := SkillDirs{filepath.Join(t.TempDir(), "nope")}.List()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v; want an empty, non-nil list", got, err)
	}
}

func TestReadSkillsMissingDir(t *testing.T) {
	got, err := readSkills(filepath.Join(t.TempDir(), "nope"), map[string]bool{}, false)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want no skills and no error", got, err)
	}
}

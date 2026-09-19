package mergedfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteIsContentAddressedAndAtomic(t *testing.T) {
	dir := t.TempDir()

	first, err := Write(dir, "settings", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(dir, "settings", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identical content must share one path: %s vs %s", first, second)
	}

	third, err := Write(dir, "settings", []byte(`{"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("different content must not overwrite the same path")
	}
	for _, path := range []string{first, third} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if !strings.HasPrefix(filepath.Base(first), "settings-") || !strings.HasSuffix(first, ".json") {
		t.Fatalf("unexpected file name %s", first)
	}
	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPruneRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, "settings-deadbeefdead.json")
	if err := os.WriteFile(stale, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "settings-cafecafecafe.json")
	if err := os.WriteFile(fresh, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-abcdefabcdef.json")
	if err := os.WriteFile(other, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	keep, err := Write(dir, "settings", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale settings file should be pruned, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("recent settings file must survive: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("files with another prefix must survive: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("the file just written must survive: %v", err)
	}
}

func TestReadJSONStates(t *testing.T) {
	dir := t.TempDir()

	if state, err := ReadJSON(filepath.Join(dir, "missing.json"), &map[string]any{}); state != NotFound || err != nil {
		t.Fatalf("missing file: state=%v err=%v", state, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"a":`), 0o644); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	state, err := ReadJSON(bad, &v)
	if state != Corrupt || err == nil {
		t.Fatalf("corrupt file: state=%v err=%v", state, err)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("corrupt error should name the path, got %v", err)
	}

	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v = nil
	if state, err := ReadJSON(good, &v); state != Loaded || err != nil || v["a"] != float64(1) {
		t.Fatalf("good file: state=%v err=%v v=%v", state, err, v)
	}
}

func TestReadJSONCAcceptsComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	content := `{
  // user preference
  "model": "anthropic/claude-opus-4-5", /* trailing block comment */
  "share": "disabled" // keeps its value
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	state, err := ReadJSON(path, &v)
	if state != Loaded || err != nil {
		t.Fatalf("jsonc: state=%v err=%v", state, err)
	}
	if v["model"] != "anthropic/claude-opus-4-5" || v["share"] != "disabled" {
		t.Fatalf("jsonc values lost: %v", v)
	}
}

func TestStripJSONCommentsKeepsStringsIntact(t *testing.T) {
	in := []byte(`{"url":"https://x.test/a//b","code":"/* not a comment */"} // real comment`)
	out := stripJSONComments(in)
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped output should parse: %v\n%s", err, out)
	}
	if v["url"] != "https://x.test/a//b" || v["code"] != "/* not a comment */" {
		t.Fatalf("string contents changed: %v", v)
	}
}

func TestCacheDirIsStable(t *testing.T) {
	a, b := CacheDir("opencode"), CacheDir("opencode")
	if a != b || a == "" {
		t.Fatalf("CacheDir should be stable and non-empty: %q vs %q", a, b)
	}
	if !strings.HasSuffix(a, filepath.Join("coding-agent-wrapper", "merge", "opencode")) {
		t.Fatalf("unexpected cache dir %q", a)
	}
}

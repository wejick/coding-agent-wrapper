package pack

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func commitFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "--quiet", "-m", "add "+name)
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitCmd(t, repo, "init", "--quiet")
	return repo
}

func TestGitCloneRefreshAndCache(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")

	cache := filepath.Join(t.TempDir(), "cache")
	src := GitWithCache(origin, "", cache)
	ctx := context.Background()

	res, err := src.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "git" {
		t.Fatalf("first fetch: from=%s want git", res.From)
	}
	data, err := os.ReadFile(filepath.Join(cache, "v1.txt"))
	if err != nil || string(data) != "one" {
		t.Fatalf("cloned content: %q err=%v", data, err)
	}

	res, err = src.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "cache" {
		t.Fatalf("second fetch without Refresh: from=%s want cache", res.From)
	}

	commitFile(t, origin, "v2.txt", "two")
	res, err = src.Fetch(ctx, FetchOptions{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "git" {
		t.Fatalf("refresh: from=%s want git", res.From)
	}
	if _, err := os.Stat(filepath.Join(cache, "v2.txt")); err != nil {
		t.Fatalf("refresh did not pick up new commit: %v", err)
	}
}

func TestGitRefPin(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")
	gitCmd(t, origin, "branch", "--quiet", "stable")
	commitFile(t, origin, "v2.txt", "two")

	cache := filepath.Join(t.TempDir(), "cache")
	src := GitWithCache(origin, "stable", cache)
	if _, err := src.Fetch(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "v1.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "v2.txt")); !os.IsNotExist(err) {
		t.Fatal("pinned ref should not include commits after the branch point")
	}

	// Main moved on; refreshing the pinned branch must stay on stable.
	commitFile(t, origin, "v3.txt", "three")
	if _, err := src.Fetch(context.Background(), FetchOptions{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "v3.txt")); !os.IsNotExist(err) {
		t.Fatal("refresh followed main instead of pinned branch")
	}
}

func TestGitOfflineFallsBackToCache(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")

	cache := filepath.Join(t.TempDir(), "cache")
	if _, err := GitWithCache(origin, "", cache).Fetch(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	// Same cache dir, but an unresolvable ref: refresh fails and the fetch
	// must fall back to the cached copy with a note.
	broken := GitWithCache(origin, "no-such-ref", cache)
	res, err := broken.Fetch(context.Background(), FetchOptions{Refresh: true})
	if err != nil {
		t.Fatalf("offline fetch should fall back to cache: %v", err)
	}
	if res.From != "cache" || len(res.Notes) == 0 {
		t.Fatalf("expected cache fallback with notes, got %+v", res)
	}
}

func TestGitSubdirPack(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "defaults"), 0o755); err != nil {
		t.Fatal(err)
	}
	version := `{"name": "sub", "version": "1.0"}`
	if err := os.WriteFile(filepath.Join(repo, "defaults", "version.json"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, repo, "add", "-A")
	gitCmd(t, repo, "commit", "--quiet", "-m", "subdir")

	src := Git(repo+"#defaults", "")
	res, err := src.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Dir, "defaults") {
		t.Fatalf("pack dir = %s, want the #defaults subdirectory", res.Dir)
	}
	p, err := Load(res.Dir, src.Describe())
	if err != nil {
		t.Fatal(err)
	}
	if p.ShortVersion() != "sub 1.0" {
		t.Fatalf("version = %s", p.ShortVersion())
	}

	// A missing subdirectory is a clean error.
	broken := Git(repo+"#nope", "")
	if _, err := broken.Fetch(context.Background(), FetchOptions{Refresh: true}); err == nil || !strings.Contains(err.Error(), "not found in") {
		t.Fatalf("expected subdirectory error, got %v", err)
	}
}

func TestGitMissingRepoErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-repo")
	_, err := GitWithCache(missing, "", filepath.Join(t.TempDir(), "cache")).Fetch(context.Background(), FetchOptions{})
	if err == nil {
		t.Fatal("expected error cloning missing repo")
	}
}

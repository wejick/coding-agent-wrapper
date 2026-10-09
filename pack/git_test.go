package pack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	src := gitAt(origin, "", t.TempDir())
	ctx := context.Background()

	res, err := src.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "git" {
		t.Fatalf("first fetch: from=%s want git", res.From)
	}
	if got := readPackFile(t, res.Dir, "v1.txt"); got != "one" {
		t.Fatalf("cloned content: %q", got)
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
	if readPackFile(t, res.Dir, "v2.txt") != "two" {
		t.Fatal("refresh did not pick up new commit")
	}
}

func TestGitRefPin(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")
	gitCmd(t, origin, "branch", "--quiet", "stable")
	commitFile(t, origin, "v2.txt", "two")

	src := gitAt(origin, "stable", t.TempDir())
	res, err := src.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if readPackFile(t, res.Dir, "v1.txt") != "one" {
		t.Fatal("pinned ref missing its own commit")
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "v2.txt")); !os.IsNotExist(err) {
		t.Fatal("pinned ref should not include commits after the branch point")
	}

	// Main moved on; refreshing the pinned branch must stay on stable.
	commitFile(t, origin, "v3.txt", "three")
	if _, err := src.Fetch(context.Background(), FetchOptions{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "v3.txt")); !os.IsNotExist(err) {
		t.Fatal("refresh followed main instead of pinned branch")
	}
}

func TestGitOfflineFallsBackToCache(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")

	src := gitAt(origin, "", t.TempDir())
	if _, err := src.Fetch(context.Background(), FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	// The remote disappears: the refresh fails and the fetch falls back to
	// the cached copy with a note.
	if err := os.Rename(origin, origin+"-gone"); err != nil {
		t.Fatal(err)
	}
	res, err := src.Fetch(context.Background(), FetchOptions{Refresh: true})
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
	commitFile(t, repo, "defaults/version.json", `{"name": "sub", "version": "1.0"}`)
	isolateCache(t)

	src := Git(repo+"#defaults", "")
	res, err := src.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Dir) != "defaults" {
		t.Fatalf("pack dir = %s, want the #defaults subdirectory", res.Dir)
	}
	if !strings.HasSuffix(src.Describe(), "#defaults") {
		t.Fatalf("Describe() = %s, want the #defaults suffix", src.Describe())
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
	_, err := gitAt(missing, "", t.TempDir()).Fetch(context.Background(), FetchOptions{})
	if err == nil {
		t.Fatal("expected error cloning missing repo")
	}
}

// gitAt is a git source cached under root.
func gitAt(url, ref, root string) Source {
	return GitWith(url, ref, GitOptions{CacheDir: root})
}

// isolateCache points the user cache dir at a temp directory so Git()
// never touches the real cache.
func isolateCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func readPackFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// assertEmpty fails when dir holds anything, such as a failed clone's
// leftovers.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s holds %d entries, first %s", dir, len(entries), entries[0].Name())
	}
}

func TestGitSwitchingRefsServesRequestedRef(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "which.txt", "stable")
	gitCmd(t, origin, "branch", "--quiet", "stable")
	gitCmd(t, origin, "checkout", "--quiet", "-b", "feature")
	commitFile(t, origin, "which.txt", "feature")
	root := t.TempDir()
	ctx := context.Background()

	dirs := map[string]string{}
	for _, ref := range []string{"stable", "feature", "stable"} {
		res, err := gitAt(origin, ref, root).Fetch(ctx, FetchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := readPackFile(t, res.Dir, "which.txt"); got != ref {
			t.Fatalf("ref %s: which.txt = %q", ref, got)
		}
		dirs[ref] = res.Dir
	}
	if dirs["stable"] == dirs["feature"] {
		t.Fatal("two refs of one repo must be cached side by side")
	}
}

func TestGitFailedCloneLeavesNoCache(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")
	root := t.TempDir()
	src := gitAt(origin, "no-such-ref", root)

	for i := 0; i < 2; i++ {
		_, err := src.Fetch(context.Background(), FetchOptions{})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("fetch %d: expected ref not found, got %v", i+1, err)
		}
	}
	assertEmpty(t, root)
}

func TestGitConcurrentFirstFetches(t *testing.T) {
	origin := initRepo(t)
	for i := 0; i < 20; i++ {
		commitFile(t, origin, fmt.Sprintf("f%02d.txt", i), "x")
	}
	root := t.TempDir()

	const n = 8
	type result struct {
		dir string
		err error
	}
	results := make(chan result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			res, err := gitAt(origin, "", root).Fetch(context.Background(), FetchOptions{})
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{dir: res.Dir}
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent first fetch failed: %v", r.err)
		}
		// Every caller sees a complete checkout.
		if readPackFile(t, r.dir, "f19.txt") != "x" {
			t.Fatalf("incomplete checkout at %s", r.dir)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want only the checkout, got %d entries", len(entries))
	}
}

func TestStaleAfterCloneAndRefresh(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, repo, "pack/version.json", `{"version":"1"}`)
	src := gitAt(repo+"#pack", "", t.TempDir())

	res, err := src.Fetch(context.Background(), FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Dir(res.Dir)
	for _, dir := range []string{checkout, res.Dir} {
		if Stale(dir, time.Hour) {
			t.Fatalf("freshly cloned pack reported stale (dir %s)", dir)
		}
	}
	if !Stale(res.Dir, 0) {
		t.Fatal("maxAge 0 must always be stale")
	}

	// Backdate the sync, then refresh: no longer stale.
	backdate(t, checkout)
	if !Stale(res.Dir, time.Hour) {
		t.Fatal("pack synced two hours ago should be stale for maxAge 1h")
	}
	if _, err := src.Fetch(context.Background(), FetchOptions{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	if Stale(res.Dir, time.Hour) {
		t.Fatal("refreshed pack reported stale")
	}
	if !Stale(t.TempDir(), time.Hour) {
		t.Fatal("a directory that is not a checkout is always stale")
	}
}

func TestLastSyncedIgnoresFailedRefresh(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")
	src := gitAt(origin, "", t.TempDir())
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	res, err := src.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	synced, ok := LastSynced(res.Dir)
	if !ok || synced.Before(before) {
		t.Fatalf("clone must record a sync time, got %v ok=%v", synced, ok)
	}

	backdate(t, res.Dir)
	old, _ := LastSynced(res.Dir)
	if time.Since(old) < time.Hour {
		t.Fatalf("backdate did not take effect: %v", old)
	}

	// An unreachable remote fails the refresh and keeps the old time.
	if err := os.RemoveAll(origin); err != nil {
		t.Fatal(err)
	}
	res, err = src.Fetch(ctx, FetchOptions{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RefreshErr == nil {
		t.Fatal("refresh against a missing remote should fail")
	}
	got, ok := LastSynced(res.Dir)
	if !ok || !got.Equal(old) {
		t.Fatalf("failed refresh changed the sync time: %v -> %v", old, got)
	}

	if _, ok := LastSynced(t.TempDir()); ok {
		t.Fatal("a directory outside a checkout has no sync time")
	}
}

// backdate moves the checkout's last sync two hours into the past.
func backdate(t *testing.T, checkout string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(checkout, ".git", syncMarker), old, old); err != nil {
		t.Fatal(err)
	}
}

func TestGitRefreshOfDeletedBranchKeepsCacheWithNote(t *testing.T) {
	origin := initRepo(t)
	commitFile(t, origin, "v1.txt", "one")
	gitCmd(t, origin, "branch", "--quiet", "feature")
	src := gitAt(origin, "feature", t.TempDir())
	ctx := context.Background()
	res, err := src.Fetch(ctx, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, res.Dir)

	gitCmd(t, origin, "branch", "--quiet", "-D", "feature")
	res, err = src.Fetch(ctx, FetchOptions{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != "cache" || len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "not found") {
		t.Fatalf("a deleted branch must not refresh onto the stale local branch, got %+v", res)
	}
	if !Stale(res.Dir, time.Hour) {
		t.Fatal("a failed refresh must not count as a sync")
	}
}

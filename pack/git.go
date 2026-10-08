package pack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// syncMarker is written inside the checkout's .git directory after every
// successful clone or refresh; its mtime is the last time the checkout
// matched the remote.
const syncMarker = "coding-agent-wrapper-synced"

func (g gitSource) Fetch(ctx context.Context, o FetchOptions) (*FetchResult, error) {
	from := "cache"
	var notes []string
	var refreshErr error
	if !isCheckout(g.dir) {
		if err := g.clone(ctx); err != nil {
			return nil, err
		}
		from = "git"
	} else if o.Refresh {
		if refreshErr = g.refresh(ctx); refreshErr != nil {
			notes = append(notes, fmt.Sprintf("refresh failed (%v); using cached pack", refreshErr))
		} else {
			from = "git"
		}
	}
	dir := g.dir
	if g.sub != "" {
		dir = filepath.Join(dir, g.sub)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("pack: subdirectory %q not found in %s", g.sub, g.url)
		}
	}
	return &FetchResult{Dir: dir, From: from, Notes: notes, RefreshErr: refreshErr}, nil
}

// clone checks the ref out in a temporary sibling directory and renames it
// into place, so the cache directory either does not exist or holds a
// complete checkout. A failed clone leaves nothing behind. When concurrent
// launches clone the same pack, the first rename wins and the others use
// its checkout.
func (g gitSource) clone(ctx context.Context) error {
	parent := filepath.Dir(g.dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(g.dir)+"-clone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := git(ctx, "", "clone", "--quiet", g.url, tmp); err != nil {
		return fmt.Errorf("pack: cloning %s: %w", g.url, err)
	}
	if err := g.checkout(ctx, tmp); err != nil {
		return fmt.Errorf("pack: %w", err)
	}
	if err := markSynced(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, g.dir); err != nil {
		if isCheckout(g.dir) {
			return nil
		}
		return fmt.Errorf("pack: moving clone into %s: %w", g.dir, err)
	}
	return nil
}

// refresh fetches from the remote and moves the checkout to the latest
// commit of the requested ref.
func (g gitSource) refresh(ctx context.Context) error {
	if _, err := git(ctx, g.dir, "fetch", "--quiet", "--all", "--prune", "--tags"); err != nil {
		return err
	}
	if err := g.checkout(ctx, g.dir); err != nil {
		return err
	}
	return markSynced(g.dir)
}

// checkout moves the checkout in dir to the requested ref using only refs
// already present locally. Branches are matched first, then tags, then raw
// commits; git's own error for a missing ref masquerades as a pathspec
// complaint.
func (g gitSource) checkout(ctx context.Context, dir string) error {
	ref := g.ref
	if ref == "" {
		// The remote's default branch, as recorded by clone.
		out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return errors.New("no ref pinned and the remote's default branch is unknown; pin a ref for deterministic refresh")
		}
		ref = strings.TrimPrefix(out, "origin/")
	}
	verify := func(name string) bool {
		_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", name)
		return err == nil
	}
	var args []string
	switch {
	case verify("refs/remotes/origin/" + ref):
		args = []string{"checkout", "--quiet", "--force", "-B", ref, "refs/remotes/origin/" + ref}
	case verify("refs/tags/" + ref + "^{commit}"):
		args = []string{"checkout", "--quiet", "--force", "--detach", "refs/tags/" + ref}
	case !verify("refs/heads/"+ref) && verify(ref+"^{commit}"):
		// A raw commit. A local branch of the same name is left over from
		// an earlier checkout of a branch the remote has since deleted;
		// matching it would serve that stale commit as if it were fresh.
		args = []string{"checkout", "--quiet", "--force", "--detach", ref}
	default:
		return fmt.Errorf("ref %q not found in %s", g.ref, g.url)
	}
	if _, err := git(ctx, dir, args...); err != nil {
		return fmt.Errorf("checking out %s in %s: %w", ref, g.url, err)
	}
	return nil
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func markSynced(dir string) error {
	return os.WriteFile(filepath.Join(dir, ".git", syncMarker), nil, 0o644)
}

// HeadInfo returns a short "abc1234 2026-09-13 msg" line for the pack HEAD,
// or "" when dir is not a git checkout.
func HeadInfo(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "log", "-1", "--format=%h %cs %s")
	if err != nil {
		return ""
	}
	return out
}

// git runs git in dir and returns its trimmed combined output. Errors carry
// the command and its output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return trimmed, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}

// Stale reports whether a git-backed pack last synced with its remote
// (cloned or refreshed) longer ago than maxAge. dir may be the checkout or
// a pack subdirectory inside it. Sources can use it to decide when Refresh
// is worth a round trip (e.g. refresh on launch at most hourly).
func Stale(dir string, maxAge time.Duration) bool {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if isCheckout(d) {
			info, err := os.Stat(filepath.Join(d, ".git", syncMarker))
			return err != nil || time.Since(info.ModTime()) > maxAge
		}
		if filepath.Dir(d) == d {
			return true
		}
	}
}

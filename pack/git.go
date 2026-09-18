package pack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (g gitSource) Fetch(ctx context.Context, o FetchOptions) (*FetchResult, error) {
	from := "cache"
	var notes []string
	if _, err := os.Stat(filepath.Join(g.dir, ".git")); os.IsNotExist(err) {
		if err := g.clone(ctx); err != nil {
			return nil, err
		}
		from = "git"
	} else if o.Refresh {
		if err := g.refresh(ctx); err != nil {
			notes = append(notes, fmt.Sprintf("refresh failed (%v); using cached pack", err))
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
	return &FetchResult{Dir: dir, From: from, Notes: notes}, nil
}

func (g gitSource) clone(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(g.dir), 0o755); err != nil {
		return err
	}
	if err := runGit(ctx, "", "clone", "--quiet", g.url, g.dir); err != nil {
		return fmt.Errorf("pack: cloning %s: %w", g.url, err)
	}
	if g.ref == "" {
		return nil
	}
	// Resolve the ref before touching the working tree; check branch,
	// then tag, then raw commit. git's own error for a missing ref
	// masquerades as a pathspec complaint.
	switch {
	case runGit(ctx, g.dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+g.ref) == nil:
		// Remote branch: a plain checkout DWIMs it into a tracking branch.
		if err := runGit(ctx, g.dir, "checkout", "--quiet", g.ref); err != nil {
			return fmt.Errorf("pack: checking out %s in %s: %w", g.ref, g.url, err)
		}
	case runGit(ctx, g.dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+g.ref) == nil,
		runGit(ctx, g.dir, "rev-parse", "--verify", "--quiet", g.ref+"^{commit}") == nil:
		if err := runGit(ctx, g.dir, "checkout", "--quiet", "--detach", g.ref); err != nil {
			return fmt.Errorf("pack: checking out %s in %s: %w", g.ref, g.url, err)
		}
	default:
		return fmt.Errorf("pack: ref %q not found in %s", g.ref, g.url)
	}
	return nil
}

func (g gitSource) refresh(ctx context.Context) error {
	if err := runGit(ctx, g.dir, "fetch", "--quiet", "--all", "--prune", "--tags"); err != nil {
		return err
	}
	if g.ref == "" {
		// Follow the upstream of whatever branch is checked out; for a plain
		// clone that is the remote's default branch at clone time.
		if err := runGit(ctx, g.dir, "reset", "--quiet", "--hard", "@{u}"); err != nil {
			return fmt.Errorf("no ref pinned and upstream unknown; pin --ref for deterministic refresh")
		}
		return nil
	}
	if err := runGit(ctx, g.dir, "reset", "--quiet", "--hard", "refs/remotes/origin/"+g.ref); err == nil {
		return nil
	}
	if err := runGit(ctx, g.dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+g.ref); err == nil {
		return runGit(ctx, g.dir, "checkout", "--quiet", "--detach", g.ref)
	}
	if err := runGit(ctx, g.dir, "rev-parse", "--verify", "--quiet", g.ref+"^{commit}"); err == nil {
		return runGit(ctx, g.dir, "checkout", "--quiet", "--detach", g.ref)
	}
	return fmt.Errorf("ref %q not found on %s", g.ref, g.url)
}

// HeadInfo returns a short "abc1234 2026-09-13 msg" line for the pack HEAD,
// or "" when dir is not a git checkout.
func HeadInfo(ctx context.Context, dir string) string {
	out, err := outputGit(ctx, dir, "log", "-1", "--format=%h %cs %s")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func outputGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Stale reports whether a git-backed pack was last refreshed longer ago
// than maxAge. Sources can use it to decide when Refresh is worth a round
// trip (e.g. refresh on launch at most hourly).
func Stale(dir string, maxAge time.Duration) bool {
	info, err := os.Stat(filepath.Join(dir, ".git", "FETCH_HEAD"))
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > maxAge
}

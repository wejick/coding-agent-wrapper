//go:build unix

package wrapper_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	wrapper "github.com/wejick/coding-agent-wrapper"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// onReady is a Stdout writer that calls fn on the child's first write;
// the test scripts print "ready" once their traps are set.
type onReady struct {
	once *sync.Once
	fn   func()
}

func (w onReady) Write(b []byte) (int, error) {
	w.once.Do(w.fn)
	return len(b), nil
}

func TestRunChildDirEnvStdinAndExitCode(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch := &wrapper.Launch{
		Binary: script(t, `pwd -P; echo "FOO=$FOO args=$*"; echo "stdin=[$(cat)]"; echo oops >&2; exit 3`),
		Args:   []string{"-p", "task"},
		Env:    []string{"FOO=launch", "PATH=" + os.Getenv("PATH")},
		Dir:    dir,
	}
	var out, errOut bytes.Buffer
	code, err := launch.RunChild(context.Background(), wrapper.RunOptions{Stdout: &out, Stderr: &errOut})
	if err != nil || code != 3 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	want := dir + "\nFOO=launch args=-p task\nstdin=[]\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.String() != "oops\n" {
		t.Fatalf("stderr = %q", errOut.String())
	}

	out.Reset()
	code, err = launch.RunChild(context.Background(), wrapper.RunOptions{
		Stdin:  strings.NewReader("piped"),
		Stdout: &out,
		Env:    []string{"FOO=override", "PATH=" + os.Getenv("PATH")},
	})
	if err != nil || code != 3 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if !strings.Contains(out.String(), "FOO=override") || !strings.Contains(out.String(), "stdin=[piped]") {
		t.Fatalf("RunOptions.Env and Stdin should apply, stdout = %q", out.String())
	}
}

func TestRunChildStartFailure(t *testing.T) {
	launch := &wrapper.Launch{Binary: filepath.Join(t.TempDir(), "missing")}
	if code, err := launch.RunChild(context.Background(), wrapper.RunOptions{}); code != -1 || err == nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}

func TestRunChildForwardsSignals(t *testing.T) {
	launch := &wrapper.Launch{Binary: script(t, `trap 'exit 7' INT; echo ready; while :; do sleep 0.05; done`)}
	interrupt := func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) }
	code, err := launch.RunChild(context.Background(), wrapper.RunOptions{Stdout: onReady{new(sync.Once), interrupt}})
	if err != nil || code != 7 {
		t.Fatalf("the child should get the caller's SIGINT: code = %d, err = %v", code, err)
	}
}

func TestRunChildCancelTerminates(t *testing.T) {
	launch := &wrapper.Launch{Binary: script(t, `trap 'exit 5' TERM; echo ready; while :; do sleep 0.05; done`)}
	ctx, cancel := context.WithCancel(context.Background())
	code, err := launch.RunChild(ctx, wrapper.RunOptions{Stdout: onReady{new(sync.Once), cancel}})
	if code != 5 || !errors.Is(err, context.Canceled) {
		t.Fatalf("code = %d, err = %v, want 5 and context.Canceled", code, err)
	}
}

func TestRunChildCancelKillsAfterGrace(t *testing.T) {
	old := *wrapper.KillGrace
	*wrapper.KillGrace = 200 * time.Millisecond
	t.Cleanup(func() { *wrapper.KillGrace = old })
	launch := &wrapper.Launch{Binary: script(t, `trap '' TERM; echo ready; while :; do sleep 0.05; done`)}
	ctx, cancel := context.WithCancel(context.Background())
	code, err := launch.RunChild(ctx, wrapper.RunOptions{Stdout: onReady{new(sync.Once), cancel}})
	if code != -1 || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("code = %d, err = %v, want -1 and a killed error wrapping context.Canceled", code, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunChildReportsOutputErrors(t *testing.T) {
	launch := &wrapper.Launch{Binary: script(t, "echo report\n")}
	code, err := launch.RunChild(context.Background(), wrapper.RunOptions{Stdout: failingWriter{}})
	if code != 0 || err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("code = %d, err = %v, want 0 and the writer's error", code, err)
	}
}

func TestRunChildIgnoresOutputHeldByLeftoverProcess(t *testing.T) {
	old := *wrapper.KillGrace
	*wrapper.KillGrace = 200 * time.Millisecond
	t.Cleanup(func() { *wrapper.KillGrace = old })
	// The background sleep inherits stdout and keeps the pipe open after
	// the agent exits 0.
	launch := &wrapper.Launch{Binary: script(t, "echo verdict\nsleep 3 &\nexit 0\n")}
	var out bytes.Buffer
	start := time.Now()
	code, err := launch.RunChild(context.Background(), wrapper.RunOptions{Stdout: &out})
	if code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v, want 0 and no error", code, err)
	}
	if out.String() != "verdict\n" {
		t.Fatalf("stdout = %q, want the agent's own output", out.String())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("RunChild waited %v for the leftover process", elapsed)
	}
}

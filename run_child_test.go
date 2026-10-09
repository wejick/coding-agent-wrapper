//go:build unix

package wrapper_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// whenReady runs fn once the child signals it is ready by creating path.
// The test waits for the polling goroutine before it completes.
func whenReady(t *testing.T, path string, fn func()) {
	stop, finished := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		<-finished
	})
	go func() {
		defer close(finished)
		deadline := time.After(10 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil {
				fn()
				return
			}
			select {
			case <-stop:
				return
			case <-deadline:
				t.Errorf("child never created %s", path)
				fn() // unblock RunChild so the test can finish
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
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
	ready := filepath.Join(t.TempDir(), "ready")
	launch := &wrapper.Launch{Binary: script(t, `trap 'exit 7' INT; touch "$1"; while :; do sleep 0.05; done`), Args: []string{ready}}
	whenReady(t, ready, func() { _ = syscall.Kill(os.Getpid(), syscall.SIGINT) })
	code, err := launch.RunChild(context.Background(), wrapper.RunOptions{})
	if err != nil || code != 7 {
		t.Fatalf("the child should get the caller's SIGINT: code = %d, err = %v", code, err)
	}
}

func TestRunChildCancelTerminates(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	launch := &wrapper.Launch{Binary: script(t, `trap 'exit 5' TERM; touch "$1"; while :; do sleep 0.05; done`), Args: []string{ready}}
	ctx, cancel := context.WithCancel(context.Background())
	whenReady(t, ready, cancel)
	code, err := launch.RunChild(ctx, wrapper.RunOptions{})
	if code != 5 || !errors.Is(err, context.Canceled) {
		t.Fatalf("code = %d, err = %v, want 5 and context.Canceled", code, err)
	}
}

func TestRunChildCancelKillsAfterGrace(t *testing.T) {
	old := *wrapper.KillGrace
	*wrapper.KillGrace = 200 * time.Millisecond
	t.Cleanup(func() { *wrapper.KillGrace = old })
	ready := filepath.Join(t.TempDir(), "ready")
	launch := &wrapper.Launch{Binary: script(t, `trap '' TERM; touch "$1"; while :; do sleep 0.05; done`), Args: []string{ready}}
	ctx, cancel := context.WithCancel(context.Background())
	whenReady(t, ready, cancel)
	code, err := launch.RunChild(ctx, wrapper.RunOptions{})
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

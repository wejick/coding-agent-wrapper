package wrapper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"time"
)

// RunOptions configures RunChild.
type RunOptions struct {
	// Stdin is the child's input; nil means empty input, so an agent in
	// print mode does not read a pipe meant for the caller.
	Stdin io.Reader
	// Stdout and Stderr receive the child's output; nil discards it.
	Stdout, Stderr io.Writer
	// Env overrides the launch's environment when non-nil.
	Env []string
}

// killGrace is how long RunChild waits after asking a cancelled child to
// stop before killing it.
var killGrace = 10 * time.Second

// RunChild runs the launch as a child process in l.Dir and waits for it.
// The environment is o.Env, else l.Env, else os.Environ.
//
// While the child runs, SIGINT and SIGTERM sent to the caller are
// forwarded to it (only interrupt off UNIX) and do not stop the caller.
// The child shares the caller's process group, so a Ctrl-C typed in a
// terminal reaches it both from the terminal and forwarded. A caller that
// should stop on these signals too passes a ctx from signal.NotifyContext.
// When ctx is done, the child gets SIGTERM and is killed if it is still
// running 10 seconds later (off UNIX it is killed at once).
//
// RunChild returns the child's exit code. It returns -1 and an error when
// the child could not start or was killed by a signal; that error wraps
// ctx.Err() when ctx was done. When the child exits on its own after ctx
// is done, it returns the exit code and ctx.Err(). An error from the
// Stdout or Stderr writer is returned with the exit code.
//
// A process the child leaves behind (a background shell task, an MCP
// server) can keep the child's stdout or stderr open after the child
// exits. RunChild then stops reading that output 10 seconds after the
// child exits and returns the exit code without an error; the child's
// own output is complete, and what the leftover process writes later is
// dropped.
func (l *Launch) RunChild(ctx context.Context, o RunOptions) (int, error) {
	cmd := exec.CommandContext(ctx, l.Binary, l.Args...)
	cmd.Dir = l.Dir
	cmd.Env = o.Env
	if cmd.Env == nil {
		cmd.Env = l.Env
	}
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = killGrace

	// Register before Start so a signal that arrives while the child
	// starts is forwarded instead of killing the caller.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, forwardSignals...)
	if err := cmd.Start(); err != nil {
		signal.Stop(sigs)
		return -1, err
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	signal.Stop(sigs) // no signal is delivered after Stop returns
	close(sigs)

	if cmd.ProcessState == nil {
		return -1, err
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		err = fmt.Errorf("wrapper: %s: %s", l.Binary, cmd.ProcessState)
		if ctx.Err() != nil {
			err = fmt.Errorf("%w: %w", err, ctx.Err())
		}
		return -1, err
	}
	if ctx.Err() != nil {
		return code, ctx.Err()
	}
	// exec reports ErrWaitDelay only after the child exited 0 while a
	// leftover process held its output open; the exit code is the result.
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrWaitDelay) {
		return code, err
	}
	return code, nil
}

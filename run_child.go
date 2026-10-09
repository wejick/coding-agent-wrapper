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
// is done, it returns the exit code and ctx.Err(). An error copying the
// child's output, or output still held open by a process the child left
// behind 10 seconds after it exited, is returned with the exit code.
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
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)

	if cmd.ProcessState == nil {
		return -1, err
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		if ctx.Err() != nil {
			return -1, fmt.Errorf("wrapper: %s: %s: %w", l.Binary, cmd.ProcessState, ctx.Err())
		}
		return -1, fmt.Errorf("wrapper: %s: %s", l.Binary, cmd.ProcessState)
	}
	if ctx.Err() != nil {
		return code, ctx.Err()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return code, err
	}
	return code, nil
}

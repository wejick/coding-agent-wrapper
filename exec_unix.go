//go:build unix

package wrapper

import (
	"io"
	"os"
	"syscall"
)

// ExecOptions optionally redirects the child's streams on platforms that
// cannot exec in place; on UNIX they are unused because the execed process
// inherits the caller's stdin/stdout/stderr.
type ExecOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Env overrides the launch's environment when non-nil.
	Env []string
}

// forwardSignals are the signals RunChild passes on to the child.
var forwardSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// terminate asks a child to stop when RunChild's context is done.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// Exec starts the launch. On UNIX it replaces the current process, after
// changing to p.Dir when it is set; on success it never returns.
func (p *Launch) Exec(o ExecOptions) error {
	env := o.Env
	if env == nil {
		env = p.Env
	}
	if env == nil {
		env = os.Environ()
	}
	if p.Dir != "" {
		old, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(p.Dir); err != nil {
			return err
		}
		defer os.Chdir(old) // only reached when the exec failed
	}
	return syscall.Exec(p.Binary, append([]string{p.Binary}, p.Args...), env)
}

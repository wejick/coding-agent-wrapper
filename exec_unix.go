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

// Exec starts the launch. On UNIX it replaces the current process;
// on success it never returns.
func (p *Launch) Exec(o ExecOptions) error {
	env := o.Env
	if env == nil {
		env = p.Env
	}
	if env == nil {
		env = os.Environ()
	}
	return syscall.Exec(p.Binary, append([]string{p.Binary}, p.Args...), env)
}

//go:build !unix

package wrapper

import (
	"io"
	"os"
	"os/exec"
)

// ExecOptions redirects the child's streams on platforms without in-place
// exec.
type ExecOptions struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Env overrides the launch's environment when non-nil.
	Env []string
}

// Exec runs the launch as a child process and waits for it.
func (p *Launch) Exec(o ExecOptions) error {
	env := o.Env
	if env == nil {
		env = p.Env
	}
	if env == nil {
		env = os.Environ()
	}
	cmd := exec.Command(p.Binary, p.Args...)
	cmd.Env = env
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	return cmd.Run()
}

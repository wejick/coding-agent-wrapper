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

// forwardSignals are the signals RunChild passes on to the child.
var forwardSignals = []os.Signal{os.Interrupt}

// terminate stops a child when RunChild's context is done. Without
// SIGTERM there is no polite request to send, so it kills the child.
func terminate(p *os.Process) error { return p.Kill() }

// Exec runs the launch as a child process in p.Dir and waits for it.
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
	cmd.Dir = p.Dir
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	return cmd.Run()
}

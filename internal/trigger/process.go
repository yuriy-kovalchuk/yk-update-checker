package trigger

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
)

// Process starts a scan as a child process: this binary with the given arguments.
type Process struct {
	base    context.Context // the dashboard's lifetime: stopping it stops the scan
	args    []string
	running atomic.Bool
}

// NewProcess runs `<this binary> args...` per scan.
func NewProcess(base context.Context, args ...string) *Process {
	return &Process{base: base, args: args}
}

// Available is always true: the binary is right here.
func (p *Process) Available() bool { return true }

// Running reports whether the child is still running.
func (p *Process) Running(context.Context) bool { return p.running.Load() }

// Trigger starts the child and returns; ErrAlreadyRunning while one runs.
func (p *Process) Trigger(context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	exe, err := os.Executable()
	if err != nil {
		p.running.Store(false)
		return err
	}
	cmd := exec.CommandContext(p.base, exe, p.args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		p.running.Store(false)
		return err
	}
	go func() {
		defer p.running.Store(false)
		if err := cmd.Wait(); err != nil {
			slog.Warn("scan finished with an error", "error", err)
		}
	}()
	return nil
}

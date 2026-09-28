package platform

import (
	"fmt"
	"os/exec"
)

// ProcessGroup starts child processes (QEMU, qemu-img) that are tied to this
// process's lifetime: if droidpector exits or crashes, the OS terminates
// them (Windows Job Object with KILL_ON_JOB_CLOSE, Linux PDEATHSIG). No
// orphaned virtual machines can survive the application.
type ProcessGroup struct {
	impl processGroupImpl
}

// NewProcessGroup creates the group.
func NewProcessGroup() (*ProcessGroup, error) {
	impl, err := newProcessGroupImpl()
	if err != nil {
		return nil, fmt.Errorf("creating process group: %w", err)
	}
	return &ProcessGroup{impl: impl}, nil
}

// Start configures and starts cmd inside the group. Console windows are
// suppressed on Windows.
func (g *ProcessGroup) Start(cmd *exec.Cmd) error {
	g.impl.prepare(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := g.impl.adopt(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("binding child process to the application lifetime: %w", err)
	}
	return nil
}

// Close terminates every process still in the group.
func (g *ProcessGroup) Close() error { return g.impl.close() }

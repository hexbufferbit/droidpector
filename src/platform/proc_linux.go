//go:build linux

package platform

import (
	"os/exec"
	"syscall"
)

type processGroupImpl struct{}

func newProcessGroupImpl() (processGroupImpl, error) { return processGroupImpl{}, nil }

func (processGroupImpl) prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func (processGroupImpl) adopt(*exec.Cmd) error { return nil }
func (processGroupImpl) close() error          { return nil }

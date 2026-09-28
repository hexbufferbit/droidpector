//go:build !windows && !linux

package platform

import "os/exec"

// Development platforms (macOS): children are stopped explicitly by the VM
// service; there is no kernel-enforced lifetime binding.
type processGroupImpl struct{}

func newProcessGroupImpl() (processGroupImpl, error) { return processGroupImpl{}, nil }
func (processGroupImpl) prepare(*exec.Cmd)           {}
func (processGroupImpl) adopt(*exec.Cmd) error       { return nil }
func (processGroupImpl) close() error                { return nil }

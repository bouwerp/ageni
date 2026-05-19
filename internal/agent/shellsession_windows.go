//go:build windows

package agent

import (
	"os"
	"os/exec"
)

// shellSetPgid is a no-op on Windows (no process group concept equivalent).
func shellSetPgid(cmd *exec.Cmd) {}

// shellKillGroup kills the process directly on Windows.
func shellKillGroup(proc *os.Process) {
	proc.Kill()
}

func shellInterruptGroup(proc *os.Process) error {
	if err := proc.Signal(os.Interrupt); err == nil {
		return nil
	}
	return proc.Kill()
}

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

//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// shellSetPgid puts the shell process in its own process group so that
// killing the group also kills all child processes it spawns.
func shellSetPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// shellKillGroup kills the entire process group of proc, ensuring child
// processes (servers, build tools, etc.) are also terminated.
func shellKillGroup(proc *os.Process) {
	pgid, err := syscall.Getpgid(proc.Pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
		proc.Kill()
	}
}

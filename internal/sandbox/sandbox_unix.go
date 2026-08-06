//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// configureProcAttr 使子进程位于独立进程组，便于整树终止。
func configureProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 终止子进程及其进程组（超时兜底，避免孤儿进程）。
func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

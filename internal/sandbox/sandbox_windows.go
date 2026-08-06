//go:build windows

package sandbox

import (
	"os/exec"
	"strconv"
)

// configureProcAttr Windows 无独立进程组概念（任务通过 taskkill /T 整树终止）。
func configureProcAttr(cmd *exec.Cmd) {}

// killTree 通过 taskkill /T /F 终止子进程及其进程树（超时兜底，避免孤儿进程）。
func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
}

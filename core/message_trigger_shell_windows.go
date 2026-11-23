package core

import (
	"os/exec"
	"syscall"
)

func isolateTriggerShellConsole(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000 // Win32 CREATE_NO_WINDOW.
	// Changing PowerShell's output encoding must not change the daemon's console.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

//go:build !windows

package core

import "os/exec"

func isolateTriggerShellConsole(*exec.Cmd) {}

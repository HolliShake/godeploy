//go:build !windows

package function

import "os/exec"

func hideConsoleWindow(*exec.Cmd) {}

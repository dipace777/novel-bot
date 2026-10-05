//go:build !unix

package browser

import "os/exec"

func configureProcess(cmd *exec.Cmd)  {}
func killProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }

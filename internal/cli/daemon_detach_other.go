//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package cli

import "os/exec"

func configureDetachedProcess(cmd *exec.Cmd) {}

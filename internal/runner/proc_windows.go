//go:build windows

package runner

import "os/exec"

func isolate(_ *exec.Cmd) {}

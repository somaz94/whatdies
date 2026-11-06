//go:build unix

package terraform

import (
	"os"
	"syscall"
)

const openFlags = os.O_RDONLY | syscall.O_NONBLOCK

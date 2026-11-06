//go:build !unix

package terraform

import "os"

const openFlags = os.O_RDONLY

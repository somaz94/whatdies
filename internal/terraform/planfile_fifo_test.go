//go:build !windows

package terraform

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// An agent can create a FIFO, and opening one blocks until a writer appears.
func TestPlanFileIsNotOpenedWhenItIsAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, problem := readPlanFile(path, nil); !strings.Contains(problem, "not a regular file") {
		t.Fatalf("problem %q", problem)
	}
}

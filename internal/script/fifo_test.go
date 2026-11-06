//go:build !windows

package script

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// An agent can create a FIFO, and opening one blocks until a writer appears.
func TestFileDoesNotOpenAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ff")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan string, 1)
	go func() { done <- File(path) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked on the FIFO")
	}
}

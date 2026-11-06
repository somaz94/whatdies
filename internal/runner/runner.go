// Package runner starts the read-only probes whatdies needs.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a program and returns its stdout.
type Runner interface {
	Run(ctx context.Context, opts Options, name string, args ...string) ([]byte, error)
}

// Options adjusts one run.
type Options struct {
	Dir string
}

// maxOutput bounds a probe's stdout; beyond it the verdict falls back to ask
// rather than exhausting memory on every hook call.
const maxOutput = 256 << 20

// Exec is the real Runner.
type Exec struct{}

// Run starts the program in its own process group so a credential plugin that
// inherited the pipe cannot outlive the deadline and hold the hook open.
func (Exec) Run(ctx context.Context, opts Options, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Dir
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutput, 1<<16
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	isolate(cmd)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if stdout.overflow {
		return nil, fmt.Errorf("%s output exceeded %d bytes", name, maxOutput)
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, firstLine(msg))
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// firstLine keeps probe errors to one line, since an agent reads the reason inline.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(stripANSI(line)); line != "" {
			return line
		}
	}
	return ""
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

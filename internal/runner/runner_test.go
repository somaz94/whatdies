package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunReturnsStdout(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), Options{}, "sh", "-c", "printf hello")
	if err != nil || string(out) != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunUsesDirAndEnvRules(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHATDIES_TEST_DROP", "x")
	t.Setenv("WHATDIES_PREFIX_A", "x")
	opts := Options{Dir: dir, DropEnv: []string{"WHATDIES_TEST_DROP", "WHATDIES_PREFIX_*"}, SetEnv: []string{"WHATDIES_SET=1"}}
	out, err := Exec{}.Run(context.Background(), opts, "sh", "-c", `pwd; printf "%s,%s,%s" "$WHATDIES_TEST_DROP" "$WHATDIES_PREFIX_A" "$WHATDIES_SET"`)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	lines := strings.Split(string(out), "\n")
	if lines[0] != real && lines[0] != dir {
		t.Errorf("pwd = %q, want %q", lines[0], dir)
	}
	if lines[1] != ",,1" {
		t.Errorf("env = %q", lines[1])
	}
}

func TestRunReportsTheFirstStderrLine(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), Options{}, "sh", "-c", `printf '\033[31m│\033[0m\n│ Error: boom\nmore\n' >&2; exit 3`)
	if err == nil || !strings.Contains(err.Error(), "Error: boom") || strings.Contains(err.Error(), "more") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWithoutStderrStillFails(t *testing.T) {
	if _, err := (Exec{}).Run(context.Background(), Options{}, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("want error")
	}
}

func TestRunStopsAtTheDeadlineEvenIfAChildHoldsThePipe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Exec{}.Run(ctx, Options{}, "sh", "-c", "sleep 30 & wait")
	if err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestLimitedBufferReportsOverflow(t *testing.T) {
	b := limitedBuffer{limit: 3}
	if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !b.overflow || b.String() != "abc" {
		t.Fatalf("overflow=%v buf=%q", b.overflow, b.String())
	}
	if _, err := b.Write([]byte("x")); err != nil || b.String() != "abc" {
		t.Fatalf("buf=%q err=%v", b.String(), err)
	}
}

func TestFirstLineSkipsDecoration(t *testing.T) {
	if got := firstLine("╷\n│ \n│ Error: x\n"); got != "Error: x" {
		t.Fatalf("got %q", got)
	}
	if got := firstLine(""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestMissingProgram(t *testing.T) {
	missing := filepath.Join(os.TempDir(), "whatdies-no-such-binary")
	if _, err := (Exec{}).Run(context.Background(), Options{}, missing); err == nil {
		t.Fatal("want error")
	}
}

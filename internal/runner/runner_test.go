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

func TestRunUsesDir(t *testing.T) {
	dir := t.TempDir()
	out, err := Exec{}.Run(context.Background(), Options{Dir: dir}, "sh", "-c", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if got := strings.TrimSpace(string(out)); got != real && got != dir {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}

func TestRunReportsTheFirstStderrLine(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), Options{}, "sh", "-c", `printf '\033[31m\033[0m\n  error: boom\nmore\n' >&2; exit 3`)
	if err == nil || !strings.Contains(err.Error(), "error: boom") || strings.Contains(err.Error(), "more") {
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

func TestFirstLineSkipsBlankAndColourOnlyLines(t *testing.T) {
	if got := firstLine("\n\x1b[0m \n  error: x\n"); got != "error: x" {
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

//go:build !windows

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// noisyHome is a home whose shell startup files all print, which is common and
// would break a decision Codex reads from stdout.
func noisyHome(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	files := map[string]string{
		".zshenv": "echo zshenv; echo zshenv >&2", ".zprofile": "echo zprofile", ".zshrc": "echo zshrc",
		".bash_profile": "echo bash_profile", ".bashrc": "echo bashrc", ".profile": "echo profile",
		"bash_env": "echo bash_env", ".config/fish/config.fish": "echo fish",
	}
	for name, body := range files {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"HOME=" + home, "ZDOTDIR=" + home, "BASH_ENV=" + filepath.Join(home, "bash_env"), "ENV=" + filepath.Join(home, ".profile"), "PATH=/usr/bin:/bin"}
}

func shells(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, s := range []string{"sh", "bash", "zsh", "fish", "dash"} {
		if path, err := exec.LookPath(s); err == nil {
			found = append(found, path)
		}
	}
	if len(found) < 2 {
		t.Fatalf("only %v found", found)
	}
	return found
}

// Codex runs the command as `<shell> -c` with the user's shell, or `$SHELL -lc`
// when a turn has none, so it must block under both, with noisy startup files.
func TestCodexHookCommandFailsClosedUnderEveryShell(t *testing.T) {
	dir := t.TempDir()
	fake := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name   string
		binary string
		blocks bool
	}{
		{"no decision", fake("none", "cat >/dev/null; exit 0"), false},
		{"deny", fake("deny", "cat >/dev/null; echo 'Blocked by the whatdies safety hook.' >&2; exit 2"), true},
		{"silent exit 2", fake("silent", "exit 2"), true},
		{"error exit", fake("error", "exit 1"), true},
		{"crash", fake("crash", "kill -SEGV $$"), true},
		{"missing", filepath.Join(dir, "gone"), true},
		{"path with a space", fake("with space", "exit 1"), true},
	}
	env := noisyHome(t)
	for _, shell := range shells(t) {
		for _, flag := range []string{"-c", "-lc"} {
			for _, c := range cases {
				line, err := CodexHookCommand(c.binary)
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(shell, flag, line)
				cmd.Env = env
				cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"x"}}`)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				exit := 0
				if err := cmd.Run(); err != nil {
					var ee *exec.ExitError
					if !errors.As(err, &ee) {
						t.Fatalf("%s %s %s: %v", shell, flag, c.name, err)
					}
					exit = ee.ExitCode()
				}
				if got := codexBlocks(exit, stdout.String(), stderr.String()); got != c.blocks {
					t.Errorf("%s %s %s: blocks=%v (exit %d, stdout %q, stderr %q)", shell, flag, c.name, got, exit, stdout.String(), stderr.String())
				}
				if c.name == "deny" && strings.Contains(stderr.String(), "hook failed") {
					t.Errorf("%s %s: a deny was reported as a failure: %q", shell, flag, stderr.String())
				}
			}
		}
	}
}

func TestVerifyCodex(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	denies := write("denies", "cat >/dev/null; echo blocked >&2; exit 2")
	allows := write("allows", "cat >/dev/null; exit 0")
	command := func(binary string) string {
		c, err := CodexHookCommand(binary)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Setenv("HOME", t.TempDir())
	if err := VerifyCodex(context.Background(), command(denies), "/bin/sh"); err != nil {
		t.Fatalf("a hook that blocks: %v", err)
	}
	if err := VerifyCodex(context.Background(), command(allows), "/bin/sh"); err == nil || !strings.Contains(err.Error(), "/bin/sh -c") {
		t.Fatalf("a hook that lets it run: %v", err)
	}
	// A login profile that hands over to another program never reaches the hook.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCodex(context.Background(), command(denies), "/bin/sh"); err == nil || !strings.Contains(err.Error(), "-lc") {
		t.Fatalf("a profile that exits: %v", err)
	}
	if err := VerifyCodex(context.Background(), command(denies), filepath.Join(dir, "no-such-shell")); err == nil {
		t.Fatal("a missing shell passed")
	}
}

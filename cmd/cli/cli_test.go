package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/install"
)

func execute(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(args)
	checkJSON, checkStateful, checkDir, installClaude, installCodex, installProject, hookCodex = false, nil, "", false, false, false, false
	err := rootCmd.Execute()
	return out.String(), err
}

func code(err error) int {
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 0
}

func TestCheckExitCodes(t *testing.T) {
	cases := []struct {
		cmd  string
		code int
		text string
	}{
		{"ls", 0, "NO DECISION"},
		{"helm uninstall x", exitAsk, "ASK"},
		{"terraform destroy", exitDeny, "DENY"},
	}
	for _, c := range cases {
		out, err := execute(t, "", "check", c.cmd)
		if code(err) != c.code || !strings.Contains(out, c.text) {
			t.Errorf("%s: code=%d out=%q", c.cmd, code(err), out)
		}
	}
}

func TestCheckPrintsReasonsAndAdvice(t *testing.T) {
	out, _ := execute(t, "", "check", "terraform destroy")
	if !strings.Contains(out, "without a saved plan") || !strings.Contains(out, "-> Run") {
		t.Fatalf("out = %s", out)
	}
}

func TestCheckJSON(t *testing.T) {
	out, err := execute(t, "", "check", "--json", "terraform destroy")
	if code(err) != exitDeny || !strings.Contains(out, `"decision":"deny"`) {
		t.Fatalf("code=%d out=%s", code(err), out)
	}
}

func TestCheckStatefulIsLabelled(t *testing.T) {
	out, _ := execute(t, "", "check", "--stateful", "terraform_data", "ls")
	if !strings.Contains(out, "treating terraform_data as stateful") {
		t.Fatalf("out = %s", out)
	}
}

func TestCheckDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("terraform destroy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, "", "check", "--dir", dir, "./x.sh")
	if code(err) != exitAsk {
		t.Fatalf("code = %d", code(err))
	}
}

func TestHookCommand(t *testing.T) {
	in := `{"tool_name":"Bash","cwd":"/w","tool_input":{"command":"terraform destroy"}}`
	out, err := execute(t, in, "hook")
	if err != nil || !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Fatalf("err=%v out=%s", err, out)
	}
	out, err = execute(t, `{"tool_name":"Bash","cwd":"/w","tool_input":{"command":"ls"}}`, "hook")
	if err != nil || out != "" {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestInstallAndUninstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")

	if _, err := execute(t, "", "install"); err == nil {
		t.Fatal("install without --claude must fail")
	}
	if _, err := execute(t, "", "uninstall"); err == nil {
		t.Fatal("uninstall without --claude must fail")
	}
	out, err := execute(t, "", "install", "--claude")
	if err != nil || !strings.Contains(out, path) {
		t.Fatalf("err=%v out=%s", err, out)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "managed by whatdies") {
		t.Fatalf("settings = %s", data)
	}
	out, err = execute(t, "", "uninstall", "--claude")
	if err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	out, err = execute(t, "", "uninstall", "--claude")
	if err != nil || !strings.Contains(out, "No whatdies hook") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestSettingsPaths(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	defer func() { installClaude, installCodex, installProject = false, false, false }()
	cases := []struct {
		claude, codex, project bool
		want                   string
	}{
		{true, false, false, filepath.Join(home, ".claude", "settings.json")},
		{true, false, true, filepath.Join(".claude", "settings.json")},
		{false, true, false, filepath.Join(home, ".codex", "hooks.json")},
		{false, true, true, filepath.Join(".codex", "hooks.json")},
	}
	for _, c := range cases {
		installClaude, installCodex, installProject = c.claude, c.codex, c.project
		codex, got, err := target(true)
		if err != nil || got != c.want || codex != c.codex {
			t.Errorf("%+v: got=%q codex=%v err=%v", c, got, codex, err)
		}
	}
	t.Setenv("CODEX_HOME", "/elsewhere")
	installClaude, installCodex, installProject = false, true, false
	if _, got, _ := target(true); got != filepath.Join("/elsewhere", "hooks.json") {
		t.Errorf("CODEX_HOME: %q", got)
	}
	for _, both := range [][2]bool{{false, false}, {true, true}} {
		installClaude, installCodex = both[0], both[1]
		if _, _, err := target(true); err == nil || !strings.Contains(err.Error(), "--claude or --codex") {
			t.Errorf("%v: err=%v", both, err)
		}
	}
}

func TestInstallAndUninstallCodex(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	var checked string
	verifyCodex = func(_ context.Context, command, shell string) error {
		checked = command
		return nil
	}
	defer func() { verifyCodex = install.VerifyCodex }()
	out, err := execute(t, "", "install", "--codex")
	if !strings.Contains(checked, "hook --codex") {
		t.Fatalf("the shell check did not run: %q", checked)
	}
	if err != nil || !strings.Contains(out, "/hooks") || !strings.Contains(out, "uninstall --codex") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json"))
	if err != nil || !strings.Contains(string(data), "hook --codex") || !strings.Contains(string(data), `"^Bash$"`) {
		t.Fatalf("hooks.json = %s %v", data, err)
	}
	if out, err = execute(t, "", "uninstall", "--codex"); err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestInstallCodexRefusesAShellThatWouldNotBlock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	verifyCodex = func(context.Context, string, string) error { return errors.New("under fish -lc it did not block") }
	defer func() { verifyCodex = install.VerifyCodex }()
	if _, err := execute(t, "", "install", "--codex"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hooks.json was written: %v", err)
	}
}

func TestHookSpeaksCodexWhenAsked(t *testing.T) {
	event := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"helm uninstall x --kubeconfig k"},"cwd":"/w"}`
	out, err := execute(t, event, "hook", "--codex")
	if code(err) != 2 || !strings.Contains(out, "a Codex hook cannot ask") || strings.Contains(out, "permissionDecision") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	out, err = execute(t, event, "hook")
	if err != nil || !strings.Contains(out, `"permissionDecision":"ask"`) {
		t.Fatalf("claude: err=%v out=%s", err, out)
	}
}

func TestExecuteReturnsExitCodes(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	checkJSON, checkStateful, checkDir = false, nil, ""
	rootCmd.SetArgs([]string{"check", "terraform destroy"})
	if got := Execute(); got != exitDeny {
		t.Fatalf("got %d", got)
	}
	rootCmd.SetArgs([]string{"version"})
	if got := Execute(); got != 0 {
		t.Fatalf("got %d", got)
	}
	rootCmd.SetArgs([]string{"no-such-command"})
	if got := Execute(); got != 1 {
		t.Fatalf("got %d", got)
	}
	if !strings.Contains(exitError{3}.Error(), "3") {
		t.Fatal("exitError message")
	}
}

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execute(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	rootCmd.SetArgs(args)
	checkJSON, checkStateful, checkDir, installClaude, installProject = false, nil, "", false, false
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := settingsPath()
	if err != nil || got != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("got=%q err=%v", got, err)
	}
	installProject = true
	defer func() { installProject = false }()
	got, err = settingsPath()
	if err != nil || got != filepath.Join(".claude", "settings.json") {
		t.Fatalf("got=%q err=%v", got, err)
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

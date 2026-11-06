package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexBlocks mirrors parse_completed in openai/codex
// codex-rs/hooks/src/events/pre_tool_use.rs: a PreToolUse hook blocks on exit 0
// with a deny decision, or on exit 2 with a reason on stderr. Anything else is a
// failed hook, and Codex runs the command. Stdout counts as JSON only when it
// starts with { or [ (looks_like_json in engine/output_parser.rs).
func codexBlocks(exit int, stdout, stderr string) bool {
	switch exit {
	case 0:
		var out struct {
			HookSpecificOutput struct {
				HookEventName            string `json:"hookEventName"`
				PermissionDecision       string `json:"permissionDecision"`
				PermissionDecisionReason string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal([]byte(stdout), &out) != nil {
			return false
		}
		h := out.HookSpecificOutput
		return h.HookEventName == "PreToolUse" && h.PermissionDecision == "deny" && strings.TrimSpace(h.PermissionDecisionReason) != ""
	case 2:
		return strings.TrimSpace(stderr) != ""
	}
	return false
}

func TestCodexBlocksMirrorsCodexRules(t *testing.T) {
	deny := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"x"}}`
	for _, c := range []struct {
		exit           int
		stdout, stderr string
		want           bool
	}{
		{0, "", "", false},
		{0, deny, "", true},
		{0, "Welcome\n" + deny, "", false},
		{0, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"x"}}`, "", false},
		{0, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":" "}}`, "", false},
		{2, "", "", false},
		{2, "Welcome", "reason", true},
		{1, "", "reason", false},
	} {
		if got := codexBlocks(c.exit, c.stdout, c.stderr); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestCodexHookCommandRefusesPathsItCannotQuote(t *testing.T) {
	for _, p := range []string{"/a/it's", `/a/"x"`, `/a/b\c`, "/a/$HOME", "/a/`x`"} {
		if _, err := CodexHookCommand(p); err == nil {
			t.Errorf("%q: accepted", p)
		}
		if err := InstallCodex(filepath.Join(t.TempDir(), "hooks.json"), p); err == nil {
			t.Errorf("%q: installed", p)
		}
	}
}

func TestInstallCodexKeepsOtherHooksAndReplacesItsOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	existing := `{"description":"mine","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"my-policy"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := InstallCodex(path, "/opt/whatdies"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), marker) != 1 || !strings.Contains(string(data), "my-policy") || !strings.Contains(string(data), `"description": "mine"`) {
		t.Fatalf("hooks.json = %s", data)
	}
	if !strings.Contains(string(data), `"statusMessage"`) || strings.Contains(string(data), "onFailure") || !strings.Contains(string(data), `"timeout": 120`) {
		t.Fatalf("hooks.json = %s", data)
	}
	if removed, err := Uninstall(path); err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), marker) || !strings.Contains(string(data), "my-policy") {
		t.Fatalf("after uninstall = %s", data)
	}
}

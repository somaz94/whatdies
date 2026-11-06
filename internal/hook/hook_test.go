package hook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/somaz94/whatdies/internal/verdict"
)

func event(tool, command string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": tool, "cwd": "/w",
		"tool_input": map[string]any{"command": command, "description": "x", "timeout": 1000},
	})
	return string(b)
}

func run(t *testing.T, in string, eval Evaluator, deadline time.Duration) map[string]string {
	t.Helper()
	var out strings.Builder
	if err := Run(context.Background(), strings.NewReader(in), &out, io.Discard, eval, deadline, Claude); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return nil
	}
	var doc struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	return doc.HookSpecificOutput
}

func fixed(v verdict.Verdict) Evaluator {
	return func(context.Context, string, string) verdict.Verdict { return v }
}

func TestNoDecisionWritesNothing(t *testing.T) {
	if got := run(t, event("Bash", "ls"), fixed(verdict.Verdict{}), time.Second); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestDecisionsMatchTheProtocol(t *testing.T) {
	got := run(t, event("Bash", "x"), fixed(verdict.DenyBecause("stop", "because")), time.Second)
	if got["hookEventName"] != "PreToolUse" || got["permissionDecision"] != "deny" {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(got["permissionDecisionReason"], "because") || !strings.Contains(got["permissionDecisionReason"], "stop") {
		t.Fatalf("reason = %q", got["permissionDecisionReason"])
	}
	if got := run(t, event("Bash", "x"), fixed(verdict.AskBecause("r")), time.Second); got["permissionDecision"] != "ask" {
		t.Fatalf("got %v", got)
	}
}

func TestNeverEmitsAllow(t *testing.T) {
	for _, v := range []verdict.Verdict{{}, verdict.AskBecause("a"), verdict.DenyBecause("", "d")} {
		if got := run(t, event("Bash", "x"), fixed(v), time.Second); got["permissionDecision"] == "allow" {
			t.Fatal("emitted allow")
		}
	}
}

func TestCommandAndDirectoryReachTheEvaluator(t *testing.T) {
	var command, cwd string
	eval := func(_ context.Context, c, d string) verdict.Verdict {
		command, cwd = c, d
		return verdict.Verdict{}
	}
	run(t, event("Monitor", "tail x"), eval, time.Second)
	if command != "tail x" || cwd != "/w" {
		t.Fatalf("command=%q cwd=%q", command, cwd)
	}
}

func TestOtherToolsAreIgnored(t *testing.T) {
	called := false
	eval := func(context.Context, string, string) verdict.Verdict {
		called = true
		return verdict.Verdict{}
	}
	for _, in := range []string{event("Edit", "x"), event("Bash", "")} {
		if got := run(t, in, eval, time.Second); got != nil {
			t.Fatalf("got %v", got)
		}
	}
	if called {
		t.Fatal("evaluator should not run")
	}
}

// Every path below would let the command run if it exited without a decision.
func TestFailuresBecomeAsk(t *testing.T) {
	panics := func(context.Context, string, string) verdict.Verdict { panic("boom") }
	hangs := func(ctx context.Context, _, _ string) verdict.Verdict {
		<-time.After(5 * time.Second)
		return verdict.Verdict{}
	}
	cases := []struct {
		name string
		in   string
		eval Evaluator
		want string
	}{
		{"bad json", "nope", fixed(verdict.Verdict{}), "decode"},
		{"empty", "", fixed(verdict.Verdict{}), "decode"},
		{"too large", strings.Repeat("x", maxInput+1), fixed(verdict.Verdict{}), "too large"},
		{"panic", event("Bash", "x"), panics, "internal error"},
		{"deadline", event("Bash", "x"), hangs, "did not finish"},
	}
	for _, c := range cases {
		got := run(t, c.in, c.eval, 20*time.Millisecond)
		if got["permissionDecision"] != "ask" || !strings.Contains(got["permissionDecisionReason"], c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestUnreadableStdinIsAsk(t *testing.T) {
	var out strings.Builder
	if err := Run(context.Background(), failingReader{}, &out, io.Discard, fixed(verdict.Verdict{}), time.Second, Claude); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"ask"`) {
		t.Fatalf("out = %s", out.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken") }

func TestWriteFailureIsReturned(t *testing.T) {
	err := Run(context.Background(), strings.NewReader(event("Bash", "x")), failingWriter{}, io.Discard, fixed(verdict.AskBecause("a")), time.Second, Claude)
	if err == nil {
		t.Fatal("want error, so the installed command exits 2")
	}
}

func TestCodexGetsADenyOnStderrOrNothing(t *testing.T) {
	cases := map[string]struct {
		in      string
		eval    Evaluator
		blocked bool
		want    string
	}{
		"none":       {event("Bash", "ls"), fixed(verdict.Verdict{}), false, ""},
		"ask":        {event("Bash", "x"), fixed(verdict.AskBecause("why")), true, "a Codex hook cannot ask"},
		"deny":       {event("Bash", "x"), fixed(verdict.DenyBecause("advice", "why")), true, "advice"},
		"bad event":  {"nope", fixed(verdict.Verdict{}), true, "could not decode"},
		"panic":      {event("Bash", "x"), func(context.Context, string, string) verdict.Verdict { panic("boom") }, true, "internal error"},
		"other tool": {event("apply_patch", "x"), fixed(verdict.DenyBecause("a", "b")), false, ""},
	}
	for name, c := range cases {
		var stdout, stderr strings.Builder
		err := Run(context.Background(), strings.NewReader(c.in), &stdout, &stderr, c.eval, time.Second, Codex)
		if stdout.Len() != 0 {
			t.Errorf("%s: wrote to stdout: %q", name, stdout.String())
		}
		if blocked := errors.Is(err, ErrBlocked); blocked != c.blocked || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: err=%v stderr=%q", name, err, stderr.String())
		}
		if c.blocked && !strings.HasPrefix(stderr.String(), "Blocked by the whatdies safety hook") {
			t.Errorf("%s: stderr does not open with the block: %q", name, stderr.String())
		}
	}
	err := Run(context.Background(), strings.NewReader(event("Bash", "x")), io.Discard, failingWriter{}, fixed(verdict.AskBecause("a")), time.Second, Codex)
	if err == nil || errors.Is(err, ErrBlocked) {
		t.Fatalf("a failed write must be an error, not a block: %v", err)
	}
}

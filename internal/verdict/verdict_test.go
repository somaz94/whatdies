package verdict

import (
	"strings"
	"testing"
)

func TestDecisionString(t *testing.T) {
	for d, want := range map[Decision]string{None: "none", Ask: "ask", Deny: "deny"} {
		if d.String() != want {
			t.Errorf("%d = %q, want %q", d, d.String(), want)
		}
	}
}

func TestJoinKeepsTheStricterDecisionAndAllReasons(t *testing.T) {
	got := Join(AskBecause("a"), DenyBecause("stop", "d"))
	if got.Decision != Deny || got.Advice != "stop" {
		t.Fatalf("got %+v", got)
	}
	if len(got.Reasons) != 2 || got.Reasons[0] != "d" || got.Reasons[1] != "a" {
		t.Fatalf("reasons = %v", got.Reasons)
	}
}

func TestJoinWithNoneChangesNothing(t *testing.T) {
	got := Join(Verdict{}, AskBecause("a"))
	if got.Decision != Ask || len(got.Reasons) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got := Join(Verdict{}, Verdict{}); got.Decision != None {
		t.Fatalf("got %+v", got)
	}
}

func TestJoinKeepsWeakerAdviceWhenStrongerHasNone(t *testing.T) {
	got := Join(Verdict{Decision: Deny}, Verdict{Decision: Ask, Advice: "hint"})
	if got.Advice != "hint" {
		t.Fatalf("advice = %q", got.Advice)
	}
}

func TestMessage(t *testing.T) {
	if m := (Verdict{}).Message(); !strings.Contains(m, "no destructive effect") {
		t.Errorf("empty message = %q", m)
	}
	m := AskBecause("one").Message()
	for _, want := range []string{"safety hook", "has not run", "- one", "do not retry"} {
		if !strings.Contains(m, want) {
			t.Errorf("ask %q missing %q", m, want)
		}
	}
	m = DenyBecause("do this", "one", "two").Message()
	for _, want := range []string{"safety hook", "did not run", "- one", "- two", "do this"} {
		if !strings.Contains(m, want) {
			t.Errorf("deny %q missing %q", m, want)
		}
	}
}

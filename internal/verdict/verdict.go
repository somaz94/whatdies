// Package verdict holds the decision whatdies reaches about a command.
package verdict

import (
	"strings"
	"unicode"
)

// Decision is what the hook tells the agent. There is deliberately no Allow:
// an allow skips the permission prompt the user would otherwise see, and
// whatdies only ever adds friction.
type Decision int

// Decisions, ordered so that a higher value wins when verdicts are joined.
const (
	None Decision = iota
	Ask
	Deny
)

func (d Decision) String() string {
	switch d {
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "none"
	}
}

// Verdict is a decision with the reasons that led to it.
type Verdict struct {
	Decision Decision
	Reasons  []string
	// Advice tells the agent what to do next, shown after the reasons.
	Advice string
}

// AskBecause returns an Ask verdict.
func AskBecause(reason ...string) Verdict { return Verdict{Decision: Ask, Reasons: cleanAll(reason)} }

// DenyBecause returns a Deny verdict with advice for the agent.
func DenyBecause(advice string, reason ...string) Verdict {
	return Verdict{Decision: Deny, Reasons: cleanAll(reason), Advice: clean(advice)}
}

func cleanAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = clean(l)
	}
	return out
}

// clean keeps text on one line. Reasons quote plans, clusters and the command
// itself and reach the agent, so they must not be able to start a new instruction.
// It lives in the constructors because calling it at each quote missed some.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, s)
}

// Join combines two verdicts: the stricter decision and advice win, and the
// reasons of a weaker verdict are dropped only when it decided nothing.
func Join(a, b Verdict) Verdict {
	if b.Decision > a.Decision {
		a, b = b, a
	}
	if b.Decision != None {
		a.Reasons = append(append([]string{}, a.Reasons...), b.Reasons...)
	}
	if a.Advice == "" {
		a.Advice = b.Advice
	}
	return a
}

// askAdvice closes an ask. In a headless run an ask comes back as a refusal, so
// it has to tell the agent what to do then as well.
const askAdvice = "If this is refused, do not retry it in another form. Tell the user what is listed above."

// Message renders the verdict for a person or an agent. It opens by saying a
// safety hook stopped the command: an agent given only the reason took it for
// the tool's own odd output, re-ran the command and inspected the binary.
func (v Verdict) Message() string {
	var b strings.Builder
	switch v.Decision {
	case Deny:
		b.WriteString("Blocked by the whatdies safety hook. The command did not run.")
	case Ask:
		b.WriteString("The whatdies safety hook wants approval for this. It has not run yet.")
	default:
		b.WriteString("whatdies found no destructive effect.")
	}
	for _, r := range v.Reasons {
		b.WriteString("\n- " + r)
	}
	switch {
	case v.Advice != "":
		b.WriteString("\n" + v.Advice)
	case v.Decision == Ask:
		b.WriteString("\n" + askAdvice)
	}
	return b.String()
}

// StopAdvice closes a deny. Without it an agent treats the denial as an obstacle
// and tries another form of the same operation.
const StopAdvice = "Stop here. Do not retry this in another form (other flags, -target, state edits, a script). " +
	"Show the user what is listed above and ask them to run it in their own terminal if they want it."

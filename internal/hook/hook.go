// Package hook speaks the PreToolUse protocol of Claude Code and Codex.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/somaz94/whatdies/internal/verdict"
)

// Deadline stays well under the installed hook timeout, so a slow check ends in a
// decision. Reaching that timeout lets the command run, unless Claude Code's entry has onFailure: block.
const Deadline = 20 * time.Second

const maxInput = 8 << 20

// watched are the tools whose input is a shell command.
var watched = map[string]bool{"Bash": true, "Monitor": true, "PowerShell": true}

type input struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type output struct {
	HookSpecificOutput decision `json:"hookSpecificOutput"`
}

type decision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// Evaluator judges a command line.
type Evaluator func(ctx context.Context, command, cwd string) verdict.Verdict

// Agent is the coding agent whose hook protocol is spoken.
type Agent int

// Agents whatdies can be installed into.
const (
	Claude Agent = iota
	Codex
)

// codexAskAdvice replaces ask, which Codex treats as a failed hook and runs the command.
const codexAskAdvice = "This needs the user's approval, and a Codex hook cannot ask for it, so it was blocked instead. " +
	"Do not retry it in another form. Show the user what is listed above and ask them to run it in their own terminal if they want it."

// ErrBlocked follows a Codex deny written to stderr; the caller exits 2. Codex reads
// stdout as JSON only when nothing precedes it, and a shell startup file can print first.
var ErrBlocked = errors.New("blocked")

// Run reads one hook event and writes the decision. Any failure to reach a verdict
// becomes ask (deny under Codex), because a clean exit without a decision lets the command run.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, eval Evaluator, deadline time.Duration, agent Agent) error {
	v := decide(ctx, stdin, eval, deadline)
	if agent != Codex {
		return write(stdout, v)
	}
	switch v.Decision {
	case verdict.None:
		return nil
	case verdict.Ask:
		v = verdict.DenyBecause(codexAskAdvice, v.Reasons...)
	}
	if _, err := io.WriteString(stderr, v.Message()+"\n"); err != nil {
		return err
	}
	return ErrBlocked
}

func decide(ctx context.Context, stdin io.Reader, eval Evaluator, deadline time.Duration) verdict.Verdict {
	in, problem := readEvent(stdin)
	if problem != "" {
		return verdict.AskBecause(problem)
	}
	if !watched[in.ToolName] || in.ToolInput.Command == "" {
		return verdict.Verdict{}
	}
	return guarded(ctx, deadline, func(ctx context.Context) verdict.Verdict {
		return eval(ctx, in.ToolInput.Command, in.Cwd)
	})
}

func readEvent(stdin io.Reader) (input, string) {
	var in input
	data, err := io.ReadAll(io.LimitReader(stdin, maxInput+1))
	if err != nil {
		return in, fmt.Sprintf("could not read the hook event: %v", err)
	}
	if len(data) > maxInput {
		return in, "the command is too large to read"
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Sprintf("could not decode the hook event: %v", err)
	}
	return in, ""
}

// guarded turns a panic or a missed deadline into ask.
func guarded(ctx context.Context, deadline time.Duration, check func(context.Context) verdict.Verdict) verdict.Verdict {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	done := make(chan verdict.Verdict, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- verdict.AskBecause(fmt.Sprintf("internal error while checking this command: %v", r))
			}
		}()
		done <- check(ctx)
	}()
	select {
	case v := <-done:
		return v
	case <-ctx.Done():
		return verdict.AskBecause("the check did not finish in time, so what this command destroys is unknown")
	}
}

func write(w io.Writer, v verdict.Verdict) error {
	if v.Decision == verdict.None {
		return nil
	}
	return json.NewEncoder(w).Encode(output{decision{
		HookEventName:            "PreToolUse",
		PermissionDecision:       v.Decision.String(),
		PermissionDecisionReason: v.Message(),
	}})
}

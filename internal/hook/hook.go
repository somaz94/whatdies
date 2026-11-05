// Package hook speaks Claude Code's PreToolUse protocol.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/somaz94/whatdies/internal/verdict"
)

// Deadline stays well under the installed hook timeout, so a slow check ends in
// ask. Reaching that timeout lets the command run unless the entry has onFailure: block.
const Deadline = 20 * time.Second

const maxInput = 8 << 20

// watched are the tools whose input is a shell command.
var watched = map[string]bool{"Bash": true, "Monitor": true, "PowerShell": true}

type input struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	Cwd           string `json:"cwd"`
	ToolInput     struct {
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

// Run reads one hook event and writes the decision. Any failure to reach a verdict
// becomes ask, because a clean exit without a decision lets the command run.
func Run(ctx context.Context, stdin io.Reader, stdout io.Writer, eval Evaluator, deadline time.Duration) error {
	return write(stdout, decide(ctx, stdin, eval, deadline))
}

func decide(ctx context.Context, stdin io.Reader, eval Evaluator, deadline time.Duration) verdict.Verdict {
	data, err := io.ReadAll(io.LimitReader(stdin, maxInput+1))
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("could not read the hook event: %v", err))
	}
	if len(data) > maxInput {
		return verdict.AskBecause("the command is too large to read")
	}
	var in input
	if err := json.Unmarshal(data, &in); err != nil {
		return verdict.AskBecause(fmt.Sprintf("could not decode the hook event: %v", err))
	}
	if !watched[in.ToolName] || in.ToolInput.Command == "" {
		return verdict.Verdict{}
	}

	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	done := make(chan verdict.Verdict, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- verdict.AskBecause(fmt.Sprintf("internal error while checking this command: %v", r))
			}
		}()
		done <- eval(ctx, in.ToolInput.Command, in.Cwd)
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

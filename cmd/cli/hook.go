package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/somaz94/whatdies/internal/engine"
	"github.com/somaz94/whatdies/internal/hook"
	"github.com/somaz94/whatdies/internal/verdict"
)

var hookCodex bool

var hookCmd = &cobra.Command{
	Use:   "hook [--codex]",
	Short: "Read a PreToolUse event on stdin and write the decision (run by the agent)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps := engine.Default()
		eval := func(ctx context.Context, command, cwd string) verdict.Verdict {
			return engine.Evaluate(ctx, command, cwd, deps)
		}
		agent := hook.Claude
		if hookCodex {
			agent = hook.Codex
		}
		err := hook.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), eval, hook.Deadline, agent)
		if errors.Is(err, hook.ErrBlocked) {
			return exitError{2}
		}
		return err
	},
}

func init() {
	hookCmd.Flags().BoolVar(&hookCodex, "codex", false, "speak Codex's protocol, where ask becomes deny")
	rootCmd.AddCommand(hookCmd)
}

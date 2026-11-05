package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/somaz94/whatdies/internal/engine"
	"github.com/somaz94/whatdies/internal/hook"
	"github.com/somaz94/whatdies/internal/verdict"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Read a PreToolUse event on stdin and write the decision (run by the agent)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps := engine.Default()
		eval := func(ctx context.Context, command, cwd string) verdict.Verdict {
			return engine.Evaluate(ctx, command, cwd, deps)
		}
		return hook.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), eval, hook.Deadline)
	},
}

func init() { rootCmd.AddCommand(hookCmd) }

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/somaz94/whatdies/internal/engine"
	"github.com/somaz94/whatdies/internal/hook"
	"github.com/somaz94/whatdies/internal/verdict"
)

// Exit codes of check, so a script can tell the outcomes apart.
const (
	exitAsk  = 3
	exitDeny = 4
)

var (
	checkJSON     bool
	checkStateful []string
	checkDir      string
)

var checkCmd = &cobra.Command{
	Use:   "check <command>",
	Short: "Show the verdict for a command without running it",
	Long: `Show the verdict for a command without running it.

Exit codes: 0 no decision, 3 ask, 4 deny, 1 error.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := checkDir
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			dir = wd
		}
		deps := engine.Default()
		deps.ExtraStateful = checkStateful
		ctx, cancel := context.WithTimeout(cmd.Context(), hook.Deadline)
		defer cancel()
		v := engine.Evaluate(ctx, args[0], dir, deps)
		if err := printVerdict(cmd, v); err != nil {
			return err
		}
		switch v.Decision {
		case verdict.Ask:
			return exitError{exitAsk}
		case verdict.Deny:
			return exitError{exitDeny}
		}
		return nil
	},
}

func printVerdict(cmd *cobra.Command, v verdict.Verdict) error {
	w := cmd.OutOrStdout()
	if checkJSON {
		return json.NewEncoder(w).Encode(map[string]any{
			"decision": v.Decision.String(),
			"reasons":  v.Reasons,
			"advice":   v.Advice,
		})
	}
	if len(checkStateful) > 0 {
		fmt.Fprintf(w, "(treating %s as stateful for this run)\n", strings.Join(checkStateful, ", "))
	}
	switch v.Decision {
	case verdict.Deny:
		fmt.Fprintln(w, "DENY")
	case verdict.Ask:
		fmt.Fprintln(w, "ASK")
	default:
		fmt.Fprintln(w, "NO DECISION: nothing destructive found, the normal permission flow applies")
		return nil
	}
	for _, r := range v.Reasons {
		fmt.Fprintln(w, "  "+r)
	}
	if v.Advice != "" {
		fmt.Fprintln(w, "  -> "+v.Advice)
	}
	return nil
}

func init() {
	checkCmd.Flags().BoolVar(&checkJSON, "json", false, "print the verdict as JSON")
	checkCmd.Flags().StringSliceVar(&checkStateful, "stateful", nil, "also treat these resource types as stateful, for this run only")
	checkCmd.Flags().StringVar(&checkDir, "dir", "", "directory the command would run from (default: current)")
	rootCmd.AddCommand(checkCmd)
}

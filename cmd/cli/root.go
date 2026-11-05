package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "whatdies",
	Short:         "See what an AI agent's infrastructure command would destroy before it runs",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `whatdies is a pre-tool hook for AI coding agents. Before a shell command runs, it
works out what terraform or kubectl would actually destroy and asks or refuses.`,
}

// exitError ends the process with a code and no message.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Execute runs the root command and returns the process exit code.
func Execute() int {
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	var exit exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	fmt.Fprintln(os.Stderr, "whatdies:", err)
	return 1
}

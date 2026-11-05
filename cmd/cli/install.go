package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/somaz94/whatdies/internal/install"
)

var (
	installClaude  bool
	installProject bool
)

func settingsPath() (string, error) {
	if installProject {
		return filepath.Join(".claude", "settings.json"), nil
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

var installCmd = &cobra.Command{
	Use:   "install --claude",
	Short: "Add the hook to Claude Code's settings",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !installClaude {
			return errors.New("name the agent: --claude is the one supported so far")
		}
		path, err := settingsPath()
		if err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		if err := install.Install(path, binary); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Added the whatdies hook to %s\nIf this binary moves or is removed, every shell command is blocked until you reinstall or run: whatdies uninstall --claude\n", path)
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall --claude",
	Short: "Remove the hook from Claude Code's settings",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !installClaude {
			return errors.New("name the agent: --claude is the one supported so far")
		}
		path, err := settingsPath()
		if err != nil {
			return err
		}
		removed, err := install.Uninstall(path)
		if err != nil {
			return err
		}
		if removed {
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the whatdies hook from %s\n", path)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "No whatdies hook in %s\n", path)
		}
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{installCmd, uninstallCmd} {
		c.Flags().BoolVar(&installClaude, "claude", false, "target Claude Code")
		c.Flags().BoolVar(&installProject, "project", false, "edit ./.claude/settings.json instead of the user settings")
		rootCmd.AddCommand(c)
	}
}

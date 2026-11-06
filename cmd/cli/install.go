package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/somaz94/whatdies/internal/install"
)

var (
	installClaude  bool
	installCodex   bool
	installProject bool
)

// verifyCodex is replaced in tests, whose binary is not whatdies.
var verifyCodex = install.VerifyCodex

func target(adding bool) (codex bool, path string, err error) {
	switch {
	case installClaude == installCodex:
		return false, "", errors.New("name the agent: --claude or --codex")
	case adding && installCodex && runtime.GOOS == "windows":
		return false, "", errors.New("on Windows, Codex runs hooks with PowerShell, where whatdies cannot make a failure block yet")
	}
	dir, file, env := ".claude", "settings.json", "CLAUDE_CONFIG_DIR"
	if installCodex {
		dir, file, env = ".codex", "hooks.json", "CODEX_HOME"
	}
	if installProject {
		return installCodex, filepath.Join(dir, file), nil
	}
	if d := os.Getenv(env); d != "" {
		return installCodex, filepath.Join(d, file), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "", err
	}
	return installCodex, filepath.Join(home, dir, file), nil
}

var installCmd = &cobra.Command{
	Use:   "install --claude | --codex",
	Short: "Add the hook to Claude Code's settings or Codex's hooks.json",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		codex, path, err := target(true)
		if err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		agent, add := "claude", install.Install
		if codex {
			agent, add = "codex", install.InstallCodex
			if err := checkCodexShell(cmd.Context(), binary); err != nil {
				return err
			}
		}
		if err := add(path, binary); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Added the whatdies hook to %s\n", path)
		if codex {
			fmt.Fprintln(out, "Codex runs a new or changed hook only after you trust it: start Codex and review it in /hooks. Until then Codex skips it and warns at startup.")
		}
		fmt.Fprintf(out, "If this binary moves or is removed, every shell command is blocked until you reinstall or run: whatdies uninstall --%s\n", agent)
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall --claude | --codex",
	Short: "Remove the hook from Claude Code's settings or Codex's hooks.json",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		_, path, err := target(false)
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

// checkCodexShell refuses an install that the user's shell would turn into a hook
// that never blocks.
func checkCodexShell(ctx context.Context, binary string) error {
	command, err := install.CodexHookCommand(binary)
	if err != nil {
		return err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := verifyCodex(ctx, command, shell); err != nil {
		return fmt.Errorf("not installed: %w. Check your shell's startup files", err)
	}
	return nil
}

func init() {
	for _, c := range []*cobra.Command{installCmd, uninstallCmd} {
		c.Flags().BoolVar(&installClaude, "claude", false, "target Claude Code")
		c.Flags().BoolVar(&installCodex, "codex", false, "target Codex")
		c.Flags().BoolVar(&installProject, "project", false, "edit ./.claude/settings.json or ./.codex/hooks.json instead of the user's")
		rootCmd.AddCommand(c)
	}
}

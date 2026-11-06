package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

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
	checkColor    string
	checkWidth    int
)

const (
	red    = "\x1b[1;31m"
	yellow = "\x1b[1;33m"
	reset  = "\x1b[0m"
)

// style is how check prints for a person: colour and wrapping only on a terminal
// unless asked, so piped output and scripts see plain lines.
type style struct {
	color bool
	width int
}

func styleFor(w io.Writer) (style, error) {
	tty, cols := false, 0
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		tty = true
		if c, _, err := term.GetSize(int(f.Fd())); err == nil {
			cols = c
		}
	}
	s := style{width: checkWidth}
	if checkWidth < 0 {
		return s, fmt.Errorf("--width must not be negative")
	}
	if s.width == 0 {
		s.width = cols
	}
	switch checkColor {
	case "always":
		s.color = true
	case "never":
	case "auto":
		s.color = tty && os.Getenv("NO_COLOR") == ""
	default:
		return s, fmt.Errorf("--color must be auto, always or never, not %q", checkColor)
	}
	return s, nil
}

func (s style) paint(code, text string) string {
	if !s.color {
		return text
	}
	return code + text + reset
}

// wrap breaks text at spaces so a line fits the width, indenting the lines after
// the first to sit under its text. A word longer than the width keeps its line.
func (s style) wrap(first, rest, text string) string {
	if s.width <= len(first)+20 {
		return first + text
	}
	var b strings.Builder
	line := first
	for i, word := range strings.Fields(text) {
		switch {
		case i == 0:
			line += word
		case len(line)+1+len(word) > s.width:
			b.WriteString(line + "\n")
			line = rest + word
		default:
			line += " " + word
		}
	}
	b.WriteString(line)
	return b.String()
}

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
	s, err := styleFor(w)
	if err != nil {
		return err
	}
	if len(checkStateful) > 0 {
		fmt.Fprintf(w, "(treating %s as stateful for this run)\n", strings.Join(checkStateful, ", "))
	}
	switch v.Decision {
	case verdict.Deny:
		fmt.Fprintln(w, s.paint(red, "DENY"))
	case verdict.Ask:
		fmt.Fprintln(w, s.paint(yellow, "ASK"))
	default:
		fmt.Fprintln(w, s.wrap("", "  ", "NO DECISION: nothing destructive found, the normal permission flow applies"))
		return nil
	}
	for _, r := range v.Reasons {
		fmt.Fprintln(w, s.wrap("  ", "    ", r))
	}
	if v.Advice != "" {
		fmt.Fprintln(w, s.wrap("  -> ", "     ", v.Advice))
	}
	return nil
}

func init() {
	checkCmd.Flags().BoolVar(&checkJSON, "json", false, "print the verdict as JSON")
	checkCmd.Flags().StringSliceVar(&checkStateful, "stateful", nil, "also treat these resource types as stateful, for this run only")
	checkCmd.Flags().StringVar(&checkDir, "dir", "", "directory the command would run from (default: current)")
	checkCmd.Flags().StringVar(&checkColor, "color", "auto", "colour the verdict: auto (on a terminal, unless NO_COLOR is set), always or never")
	checkCmd.Flags().IntVar(&checkWidth, "width", 0, "wrap lines at this many columns (default: the terminal's width, or no wrapping when not a terminal)")
	rootCmd.AddCommand(checkCmd)
}

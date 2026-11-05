package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/somaz94/whatdies/internal/script"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

func judgeMake(inv shell.Invocation) verdict.Verdict {
	if !inv.DirKnown {
		return verdict.Verdict{}
	}
	dir, file := inv.Dir, ""
	var targets []string
	vars := map[string]string{}
	for i := 0; i < len(inv.Args); i++ {
		a := inv.Args[i]
		if !a.Literal {
			continue
		}
		next := func() (string, bool) {
			i++
			if i < len(inv.Args) && inv.Args[i].Literal {
				return inv.Args[i].Value, true
			}
			return "", false
		}
		v := a.Value
		// A cluster such as -sC ends in the flag that takes the next word.
		if m := makeCluster.FindStringSubmatch(v); m != nil {
			v = "-" + m[1]
		}
		switch {
		case v == "-C" || v == "--directory":
			if value, ok := next(); ok {
				dir = resolve(dir, value)
			}
		case strings.HasPrefix(v, "--directory="):
			dir = resolve(dir, strings.TrimPrefix(v, "--directory="))
		case strings.HasPrefix(v, "-C"):
			dir = resolve(dir, v[2:])
		case v == "-f" || v == "--file" || v == "--makefile":
			if value, ok := next(); ok {
				file = value
			}
		case strings.HasPrefix(v, "--file="), strings.HasPrefix(v, "--makefile="):
			_, file, _ = strings.Cut(v, "=")
		case strings.HasPrefix(v, "-f") && !strings.HasPrefix(v, "--"):
			file = v[2:]
		case v == "-j" || v == "--jobs" || v == "-l" || v == "--load-average" || v == "--max-load":
			// These take a number only if one follows, as in make -j 2 test.
			if i+1 < len(inv.Args) && isNumber(inv.Args[i+1].Value) {
				i++
			}
		case makeValueFlags[v]:
			next()
		case v == "-E" || v == "--eval" || strings.HasPrefix(v, "--eval="):
			text := strings.TrimPrefix(v, "--eval=")
			if !strings.HasPrefix(v, "--eval=") {
				text, _ = next()
			}
			if m := script.Mention(text); m != "" {
				return verdict.AskBecause(fmt.Sprintf("make evaluates text that runs %q", m))
			}
		case strings.HasPrefix(v, "-"):
		case strings.Contains(v, "="):
			name, value, _ := strings.Cut(v, "=")
			vars[name] = value
		default:
			targets = append(targets, v)
		}
	}
	if m := script.Make(dir, file, targets, literalEnv(inv), vars); m != "" {
		return verdict.AskBecause(fmt.Sprintf("this make target runs %q", m))
	}
	return verdict.Verdict{}
}

var makeCluster = regexp.MustCompile(`^-[a-zA-Z]+([Cf])$`)

var makeValueFlags = map[string]bool{
	"-I": true, "--include-dir": true, "-o": true, "--old-file": true, "--assume-old": true,
	"-W": true, "--what-if": true, "--new-file": true, "--assume-new": true,
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func literalEnv(inv shell.Invocation) map[string]string {
	env := make(map[string]string, len(inv.Env))
	for k, v := range inv.Env {
		if v.Literal {
			env[k] = v.Value
		}
	}
	return env
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// fileWriters put content in a file named among their arguments.
var fileWriters = map[string]bool{
	"cp": true, "curl": true, "dd": true, "install": true, "ln": true, "mv": true,
	"rsync": true, "scp": true, "tee": true, "wget": true,
}

func judgeScript(inv shell.Invocation, earlier []shell.Invocation) verdict.Verdict {
	if len(inv.Args) == 0 || !inv.Args[0].Literal {
		return verdict.Verdict{}
	}
	path := inv.Args[0].Value
	if m := script.Mention(strings.Join(inv.Literals()[1:], " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s appears to run %q", path, m))
	}
	// A write can be conditional or an append, so it never hides the file on disk.
	var out verdict.Verdict
	for _, writer := range earlier {
		out = verdict.Join(out, judgeWritten(path, writer))
	}
	full, known := scriptPath(inv, path)
	if !known {
		return out
	}
	if m := script.File(full); m != "" {
		out = verdict.Join(out, verdict.AskBecause(fmt.Sprintf("%s runs %q", path, m)))
	}
	return out
}

// scriptPath finds a script on disk. A relative path needs a known directory; an
// absolute one or one under ~/ does not.
func scriptPath(inv shell.Invocation, path string) (string, bool) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		return filepath.Join(home, rest), err == nil
	}
	if filepath.IsAbs(path) {
		return path, true
	}
	return filepath.Join(inv.Dir, path), inv.DirKnown
}

// judgeWritten handles a script the same command line creates, which is not on
// disk yet when the hook runs. Agents do this often with harmless scripts, so
// text the line itself holds is read rather than refused.
func judgeWritten(path string, writer shell.Invocation) verdict.Verdict {
	redirected := writer.WritesFile(path)
	if !redirected && !(fileWriters[writer.Tool] && writer.ArgNamesFile(path)) {
		return verdict.Verdict{}
	}
	var text string
	switch {
	case !redirected:
		return verdict.AskBecause(fmt.Sprintf("%s is written by %s just before it runs", path, writer.Tool))
	case writer.HasHeredoc && writer.HeredocLiteral:
		text = writer.Heredoc
	case (writer.Tool == "echo" || writer.Tool == "printf") && len(writer.Literals()) == len(writer.Args):
		text = strings.Join(writer.Literals(), " ")
		text = strings.ReplaceAll(text, `\n`, "\n")
	default:
		return verdict.AskBecause(fmt.Sprintf("%s is written by %s just before it runs", path, writer.Tool))
	}
	if m := script.Mention(text); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s runs %q", path, m))
	}
	return verdict.Verdict{}
}

func judgeCode(inv shell.Invocation) verdict.Verdict {
	if len(inv.Args) == 0 {
		return verdict.Verdict{}
	}
	if !inv.Args[0].Literal {
		return verdict.AskBecause("inline program text is computed at run time")
	}
	if m := script.Mention(inv.Args[0].Value); m != "" {
		return verdict.AskBecause(fmt.Sprintf("inline program text runs %q", m))
	}
	return verdict.Verdict{}
}

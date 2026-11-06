package engine

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/somaz94/whatdies/internal/script"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

// quietHeads read their arguments as text: naming a tool there is not running it.
// sed and rg can execute, so judgeReader looks at those two.
var quietHeads = map[string]bool{
	"ag": true, "bat": true, "cat": true, "echo": true, "egrep": true, "fgrep": true, "gh": true,
	"glab": true, "grep": true, "head": true, "jq": true, "less": true, "ls": true, "man": true,
	"more": true, "printf": true, "tail": true, "tee": true, "touch": true, "type": true,
	"wc": true, "whatdies": true, "which": true, "yq": true,
	// These treat every argument as a name, so terraform/apply is only a directory.
	"basename": true, "chmod": true, "chown": true, "cp": true, "cut": true, "diff": true, "dirname": true,
	"du": true, "file": true, "ln": true, "mkdir": true, "mv": true, "readlink": true, "realpath": true,
	"rm": true, "rmdir": true, "stat": true, "tr": true,
}

var mutatingWords = map[string]bool{
	"apply": true, "del": true, "delete": true, "destroy": true, "drain": true, "exec": true,
	"force-unlock": true, "import": true, "mv": true, "patch": true, "replace": true, "rm": true,
	"rollback": true, "scale": true, "sync": true, "taint": true, "un": true, "uninstall": true, "upgrade": true,
}

func judgeComputedHead(inv shell.Invocation) verdict.Verdict {
	for _, name := range slices.Sorted(maps.Keys(inv.Env)) {
		value := inv.Env[name]
		if !value.Literal {
			continue
		}
		if m := script.Mention(value.Value); m != "" {
			return verdict.AskBecause(fmt.Sprintf("the program is computed at run time and %s holds %q", name, m))
		}
	}
	for _, v := range inv.Literals() {
		if mutatingWords[v] {
			return verdict.AskBecause(fmt.Sprintf("the program is computed at run time and is passed %q", v))
		}
	}
	return verdict.Verdict{}
}

// judgeOther catches wrappers no list can enumerate, such as `ssh host kubectl
// delete ...` or `aws-vault exec prod -- terraform destroy`.
func judgeOther(inv shell.Invocation) verdict.Verdict {
	if quietHeads[inv.Tool] {
		return verdict.Verdict{}
	}
	text := inv.Tool + " " + strings.Join(inv.Literals(), " ")
	if inv.HasHeredoc {
		text += "\n" + inv.Heredoc
	}
	if m := script.Mention(text); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s appears to run %q", inv.Tool, m))
	}
	return verdict.Verdict{}
}

// gitConfigRuns are settings whose value git runs as a command.
var gitConfigRuns = map[string]bool{
	"core.askpass": true, "core.editor": true, "core.fsmonitor": true, "core.pager": true, "core.sshcommand": true,
	"credential.helper": true, "diff.external": true, "gpg.program": true, "gpg.ssh.program": true, "sequence.editor": true,
}

// gitEnvRuns are the variables git runs as a command.
var gitEnvRuns = []string{
	"GIT_ASKPASS", "GIT_EDITOR", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_SEQUENCE_EDITOR", "GIT_SSH", "GIT_SSH_COMMAND",
}

func isGitRunSetting(word string) bool {
	key, _, _ := strings.Cut(strings.ToLower(word), "=")
	return gitConfigRuns[key] || strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "pager.") ||
		key == "--upload-pack" || key == "--receive-pack"
}

// judgeGit stays quiet about commit messages and the like, and looks only where
// git runs what it is given: settings and variables holding a command, rebase,
// bisect run and submodule foreach.
func judgeGit(inv shell.Invocation) verdict.Verdict {
	for _, name := range gitEnvRuns {
		if value := inv.Env[name]; value.Literal {
			if m := script.Mention(value.Value); m != "" {
				return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
			}
		}
	}
	if computedAfter(inv, "-x", "--exec", "run") {
		return verdict.AskBecause("git would run a command computed at run time")
	}
	words := inv.Literals()
	for i, w := range words {
		if !isGitRunSetting(w) {
			continue
		}
		// Only the setting: the rest of the line can be a commit message.
		text := w
		if !strings.Contains(w, "=") && i+1 < len(words) {
			text += " " + words[i+1]
		}
		if m := script.Mention(text); m != "" {
			return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
		}
	}
	switch sub := gitSubcommand(inv.Args); {
	case sub == "rebase":
	case sub == "bisect" && slices.Contains(words, "run"):
	case sub == "submodule" && slices.Contains(words, "foreach"):
	default:
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(words, " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
	}
	return verdict.Verdict{}
}

// gitSubcommand is the first word that is not a global option or its value, and is
// empty when that word is computed.
func gitSubcommand(args []shell.Arg) string {
	for i := 0; i < len(args); i++ {
		switch v := args[i].Value; {
		case !args[i].Literal:
			return ""
		case gitValueFlags[v]:
			i++
		case !strings.HasPrefix(v, "-"):
			return v
		}
	}
	return ""
}

var gitValueFlags = map[string]bool{
	"-c": true, "-C": true, "--config-env": true, "--exec-path": true, "--git-dir": true, "--namespace": true, "--work-tree": true,
}

func computedAfter(inv shell.Invocation, flags ...string) bool {
	for i := 1; i < len(inv.Args); i++ {
		if !inv.Args[i].Literal && inv.Args[i-1].Literal && slices.Contains(flags, inv.Args[i-1].Value) {
			return true
		}
	}
	return false
}

// judgeReader stays quiet about search text, and looks only where sed or rg run
// what they are given: sed's e command and flag, and rg --pre. A sed word is read
// alone because the words around it are file names.
func judgeReader(inv shell.Invocation) verdict.Verdict {
	if inv.Tool == "rg" {
		return judgeRipgrep(inv)
	}
	for _, w := range inv.Literals() {
		if sedRuns(w) {
			if m := script.Mention(w); m != "" {
				return verdict.AskBecause(fmt.Sprintf("%s would run %q", inv.Tool, m))
			}
		}
	}
	return verdict.Verdict{}
}

func judgeRipgrep(inv shell.Invocation) verdict.Verdict {
	if computedAfter(inv, "--pre") {
		return verdict.AskBecause("rg would run a command computed at run time")
	}
	words := inv.Literals()
	if !slices.ContainsFunc(words, func(w string) bool { return w == "--pre" || strings.HasPrefix(w, "--pre=") }) {
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(words, " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("rg would run %q", m))
	}
	return verdict.Verdict{}
}

var sedScriptFlag = regexp.MustCompile(`^(?:-[a-zA-Z]*e|--expression=)`)

// sedRuns reports whether a sed script uses the e command or the s command's e flag,
// either of which runs text as a command. It reads the script rather than matching
// it, because only the delimiter says where an s command's flags begin.
func sedRuns(word string) bool {
	script := sedScriptFlag.ReplaceAllString(word, "")
	if strings.HasPrefix(script, "-") {
		return false
	}
	for i := 0; i < len(script); {
		i = skipSedAddress(script, i)
		if i >= len(script) {
			return false
		}
		switch script[i] {
		case 'e':
			return true
		case 's', 'y':
			end := skipDelimited(script, i+1, 2)
			flags := end
			for flags < len(script) && !strings.ContainsRune("; }\n", rune(script[flags])) {
				flags++
			}
			if script[i] == 's' && strings.Contains(script[end:flags], "e") {
				return true
			}
			i = flags
		case 'a', 'i', 'c', 'r', 'w', 'R', 'W', 'b', 't', 'T', ':', '#':
			next := strings.IndexByte(script[i:], '\n')
			if next < 0 {
				return false
			}
			i += next
		default:
			i++
		}
	}
	return false
}

// skipSedAddress moves past separators and any address, such as 1,3 or /re/I!.
func skipSedAddress(s string, i int) int {
	for i < len(s) && strings.ContainsRune(" \t;{}\n", rune(s[i])) {
		i++
	}
	for i < len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9', c == '$', c == ',', c == '~', c == '+', c == '!', c == ' ', c == '\t':
			i++
		case c == '/':
			i = skipDelimited(s, i, 1)
			for i < len(s) && (s[i] == 'I' || s[i] == 'M') {
				i++
			}
		case c == '\\' && i+1 < len(s):
			i = skipDelimited(s, i+1, 1)
		default:
			return i
		}
	}
	return i
}

// skipDelimited moves past count delimited parts, where s[start] is the delimiter.
func skipDelimited(s string, start, count int) int {
	if start >= len(s) {
		return len(s)
	}
	delim := s[start]
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case delim:
			count--
			if count == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

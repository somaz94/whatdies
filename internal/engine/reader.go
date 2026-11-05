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
	if m := script.Mention(text); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s appears to run %q", inv.Tool, m))
	}
	return verdict.Verdict{}
}

// gitConfigRuns are settings whose value git runs as a command.
var gitConfigRuns = []string{
	"alias.", "core.askpass=", "core.editor=", "core.fsmonitor=", "core.pager=", "core.sshcommand=",
	"credential.helper=", "diff.external=", "gpg.program=", "pager.", "sequence.editor=",
}

// gitEnvRuns are the variables git runs as a command.
var gitEnvRuns = []string{
	"GIT_ASKPASS", "GIT_EDITOR", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_SEQUENCE_EDITOR", "GIT_SSH", "GIT_SSH_COMMAND",
}

// judgeGit stays quiet about commit messages and the like, and looks only where
// git runs what it is given: settings and variables holding a command, rebase
// --exec, bisect run and submodule foreach.
func judgeGit(inv shell.Invocation) verdict.Verdict {
	for _, name := range gitEnvRuns {
		if value := inv.Env[name]; value.Literal {
			if m := script.Mention(value.Value); m != "" {
				return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
			}
		}
	}
	words := inv.Literals()
	runs := false
	for i, w := range words {
		next := ""
		if i+1 < len(words) {
			next = words[i+1]
		}
		lower := strings.ToLower(w)
		switch {
		case w == "rebase", w == "bisect" && next == "run", w == "submodule" && next == "foreach":
			runs = true
		case slices.ContainsFunc(gitConfigRuns, func(key string) bool { return strings.HasPrefix(lower, key) }):
			runs = true
		}
	}
	if !runs {
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(words, " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
	}
	return verdict.Verdict{}
}

const sedAddress = `(?:[0-9]+|\$|/(?:[^/\\]|\\.)*/)`

// sedExecute matches a sed script using the e command or the s command's e flag,
// either of which runs text as a command.
var sedExecute = regexp.MustCompile(`(?:^(?:-e|--expression=)?|[;{\n][ \t]*)` +
	`(?:` + sedAddress + `(?:,` + sedAddress + `)?!?[ \t]*)?` +
	`(?:e(?:[ \t]|$)|s[^a-zA-Z0-9 \t\n].*[a-zA-Z0-9]*e[a-zA-Z0-9]*[ \t]*(?:$|[;}\n]))`)

// judgeReader stays quiet about search text, and looks only where sed or rg run
// what they are given: sed's e command and flag, and rg --pre. A sed word is read
// alone because the words around it are file names.
func judgeReader(inv shell.Invocation) verdict.Verdict {
	pre := false
	for _, w := range inv.Literals() {
		if w == "--pre" || strings.HasPrefix(w, "--pre=") {
			pre = true
		}
		if sedExecute.MatchString(w) {
			if m := script.Mention(w); m != "" {
				return verdict.AskBecause(fmt.Sprintf("%s would run %q", inv.Tool, m))
			}
		}
	}
	if !pre {
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(inv.Literals(), " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s would run %q", inv.Tool, m))
	}
	return verdict.Verdict{}
}

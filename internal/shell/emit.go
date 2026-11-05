package shell

import (
	"path/filepath"
	"regexp"
	"strings"
)

// versioned matches side-by-side installs such as terraform-1.9 and helm3.
var versioned = regexp.MustCompile(`^(helm|helmfile|kubectl|oc|terraform|terragrunt|tofu)[-_]?v?[0-9][0-9.]*$`)

// aliases are common enough to appear in agent commands through startup files.
var aliases = map[string]string{
	"k":         "kubectl",
	"kubecolor": "kubectl",
	"tf":        "terraform",
	"tg":        "terragrunt",
}

type wrapper struct {
	valueFlags map[string]bool
	// positional counts leading non-flag words that belong to the wrapper.
	positional int
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// wrappers run the command that follows their own arguments.
var wrappers = map[string]wrapper{
	"builtin":    {},
	"caffeinate": {valueFlags: set("-t", "-w")},
	"chronic":    {},
	"command":    {},
	"doas":       {valueFlags: set("-C", "-u")},
	"exec":       {valueFlags: set("-a")},
	"gtimeout":   {valueFlags: set("-k", "-s", "--kill-after", "--signal"), positional: 1},
	"ionice":     {valueFlags: set("-c", "-n", "-p", "--class", "--classdata")},
	"nice":       {valueFlags: set("-n", "--adjustment")},
	"nohup":      {},
	"setsid":     {},
	"stdbuf":     {valueFlags: set("-i", "-o", "-e", "--input", "--output", "--error")},
	"sudo":       {valueFlags: set("-C", "-D", "-g", "-h", "-p", "-R", "-r", "-T", "-t", "-U", "-u")},
	"taskpolicy": {valueFlags: set("-c", "-g", "-l", "-p", "-t")},
	"time":       {valueFlags: set("-f", "-o", "--format", "--output")},
	"timeout":    {valueFlags: set("-k", "-s", "--kill-after", "--signal"), positional: 1},
	"unbuffer":   {},
}

var shells = set("ash", "bash", "dash", "fish", "ksh", "sh", "zsh")

var interpreters = set("node", "perl", "php", "python", "python2", "python3", "ruby")

// inlineCodeFlags take program text on an interpreter's command line.
var inlineCodeFlags = set("--eval", "-E", "-p", "-r")

// inlineCodeCluster matches -c and -e and clusters ending in them, as in perl -ne
// and python3 -Ic.
var inlineCodeCluster = regexp.MustCompile(`^-[A-Za-z]*[ce]$`)

var interpreterValueFlags = set("-I", "-W", "-X")

// shellValueOptions take the next word, which must not be read as a script.
var shellValueOptions = set("-o", "+o", "-O", "+O", "--init-file", "--rcfile")

func (a *analyzer) emit(args []Arg, inline map[string]Arg, in stdin, f flags) {
	if len(a.out.Invocations) >= maxInvocations {
		a.problem("too many commands to read")
		return
	}
	if len(args) == 0 {
		return
	}
	head := args[0]
	if !head.Literal {
		if head.Subst {
			a.problem("the program to run comes from another command")
		}
		a.add(Invocation{Args: args[1:]}, inline, in)
		return
	}
	name, bare := baseName(head.Value)
	name = strings.ToLower(name)
	rest := args[1:]

	if w, ok := wrappers[name]; ok {
		a.unwrap(name, w, rest, inline, in, f)
		return
	}
	switch {
	case name == "env":
		a.env1(rest, inline, in, f)
		return
	case name == "xargs":
		a.xargs(rest, inline, in, f)
		return
	case name == "parallel":
		a.parallel(rest, f)
		return
	case name == "watch":
		a.watch(rest, f)
		return
	case name == "find":
		a.find(rest, inline, in, f)
	case name == "eval":
		a.eval(rest, f)
		return
	case shells[name]:
		a.shell(rest, in, f)
		return
	case name == "source" || name == ".":
		a.script(rest, inline, in)
		return
	case interpreters[name]:
		a.interpreter(rest, inline, in)
		return
	case name == "cd" || name == "pushd":
		a.cd(rest)
		return
	case name == "popd":
		a.dirKnown = false
		return
	}

	if canon, ok := aliases[name]; ok {
		name = canon
	}
	if m := versioned.FindStringSubmatch(name); m != nil {
		name, bare = m[1], false
	}
	if !bare && !knownTool(name) {
		a.add(Invocation{Tool: ScriptTool, Args: args}, inline, in)
		return
	}
	a.add(Invocation{Tool: name, Bare: bare, Args: rest}, inline, in)
	a.noteTargetChange(name, rest)
}

// ScriptTool marks an invocation whose first argument is a program file to read.
const ScriptTool = "<script>"

// CodeTool marks an invocation whose first argument is program text to read.
const CodeTool = "<code>"

// KnownTools are the infrastructure programs whatdies understands.
var KnownTools = []string{"helm", "helmfile", "kubectl", "oc", "terraform", "terragrunt", "tofu"}

func knownTool(name string) bool {
	for _, t := range KnownTools {
		if t == name {
			return true
		}
	}
	return false
}

func (a *analyzer) add(inv Invocation, inline map[string]Arg, in stdin) {
	env := make(map[string]Arg, len(a.env)+len(inline))
	for k, v := range a.env {
		env[k] = v
	}
	for k, v := range inline {
		env[k] = v
	}
	inv.Env = env
	inv.Dir, inv.DirKnown = a.dir, a.dirKnown
	inv.Piped = in.piped || in.file != nil
	inv.HasHeredoc, inv.HeredocLiteral, inv.Heredoc = in.hasHeredoc, in.heredocLiteral, in.heredoc
	inv.TargetChange = a.targetChange
	inv.Writes = in.writes
	a.out.Invocations = append(a.out.Invocations, inv)
}

// noteTargetChange remembers steps that change what later commands act on.
func (a *analyzer) noteTargetChange(name string, args []Arg) {
	words := make([]string, 0, len(args))
	for _, x := range args {
		words = append(words, x.Value)
	}
	joined := " " + strings.Join(words, " ") + " "
	has := func(s string) bool { return strings.Contains(joined, " "+s+" ") }
	var reason string
	switch name {
	case "kubectx", "kubens", "kctx":
		reason = name
	case "kubectl", "oc":
		for _, s := range []string{"config use-context", "config use", "config set-context", "config set-cluster"} {
			if has(s) {
				reason = name + " " + s
			}
		}
	case "terraform", "tofu":
		if has("workspace select") || has("workspace new") {
			reason = name + " workspace"
		}
	case "aws":
		if has("eks update-kubeconfig") {
			reason = "aws eks update-kubeconfig"
		}
	case "gcloud":
		if has("container clusters get-credentials") {
			reason = "gcloud get-credentials"
		}
	case "az":
		if has("aks get-credentials") {
			reason = "az aks get-credentials"
		}
	}
	if reason != "" {
		a.targetChange = reason
	}
}

func (a *analyzer) unwrap(name string, w wrapper, args []Arg, inline map[string]Arg, in stdin, f flags) {
	if name == "command" {
		for _, x := range args {
			if x.Value == "-v" || x.Value == "-V" {
				return
			}
			if !strings.HasPrefix(x.Value, "-") {
				break
			}
		}
	}
	i := 0
	for i < len(args) {
		v := args[i].Value
		if !args[i].Literal {
			break
		}
		if v == "--" {
			i++
			break
		}
		if !strings.HasPrefix(v, "-") || v == "-" {
			break
		}
		if name == "sudo" && (v == "--chdir" || strings.HasPrefix(v, "-D") || strings.HasPrefix(v, "--chdir=")) {
			a.dirKnown = false
		}
		i++
		if w.valueFlags[v] && i < len(args) {
			i++
		}
	}
	for skip := w.positional; skip > 0 && i < len(args); skip-- {
		i++
	}
	if name == "sudo" || name == "doas" {
		for i < len(args) && args[i].Literal && isAssignment(args[i].Value) {
			k, v, _ := strings.Cut(args[i].Value, "=")
			inline = withEnv(inline, k, Arg{Value: v, Literal: true})
			i++
		}
	}
	a.emit(args[i:], inline, in, f)
}

func isAssignment(s string) bool {
	k, _, ok := strings.Cut(s, "=")
	return ok && k != "" && !strings.ContainsAny(k, "-/. ")
}

func withEnv(env map[string]Arg, k string, v Arg) map[string]Arg {
	out := make(map[string]Arg, len(env)+1)
	for key, val := range env {
		out[key] = val
	}
	out[k] = v
	return out
}

func (a *analyzer) env1(args []Arg, inline map[string]Arg, in stdin, f flags) {
	i := 0
	for i < len(args) {
		x := args[i]
		if !x.Literal {
			break
		}
		switch {
		case x.Value == "-S" || strings.HasPrefix(x.Value, "--split-string"):
			a.problem("env -S builds its command at run time")
			return
		case x.Value == "-u" || x.Value == "--unset" || x.Value == "-C" || x.Value == "--chdir":
			if x.Value == "-C" || x.Value == "--chdir" {
				a.dirKnown = false
			}
			i += 2
		case x.Value == "--":
			i++
		case strings.HasPrefix(x.Value, "-"):
			if strings.HasPrefix(x.Value, "-C") || strings.HasPrefix(x.Value, "--chdir=") {
				a.dirKnown = false
			}
			i++
		case isAssignment(x.Value):
			k, v, _ := strings.Cut(x.Value, "=")
			inline = withEnv(inline, k, Arg{Value: v, Literal: true})
			i++
		default:
			a.emit(args[i:], inline, in, f)
			return
		}
	}
	if i < len(args) {
		a.emit(args[i:], inline, in, f)
	}
}

// computed marks arguments appended at run time by xargs or find.
var computed = Arg{}

func (a *analyzer) xargs(args []Arg, inline map[string]Arg, in stdin, f flags) {
	valueFlags := set("-a", "-d", "-E", "-I", "-J", "-L", "-n", "-P", "-R", "-S", "-s",
		"--arg-file", "--delimiter", "--max-args", "--max-chars", "--max-lines", "--max-procs", "--replace")
	i := skipFlags(args, valueFlags)
	if i >= len(args) {
		return
	}
	a.emit(append(append([]Arg{}, args[i:]...), computed), inline, in, f)
}

// parallel and watch hand their words to a shell, so one quoted word can be a
// whole command line.
func (a *analyzer) parallel(args []Arg, f flags) {
	valueFlags := set("-a", "-j", "-n", "-S", "--arg-file", "--jobs", "--max-args", "--sshlogin")
	i := skipFlags(args, valueFlags)
	end := i
	for end < len(args) && args[end].Value != ":::" && args[end].Value != "::::" {
		end++
	}
	if i >= end {
		return
	}
	text, ok := joined(args[i:end])
	if !ok {
		a.problem("parallel runs a command built at run time")
		return
	}
	if !strings.Contains(text, "{}") {
		text += " {}"
	}
	a.nested(text, f)
}

func (a *analyzer) watch(args []Arg, f flags) {
	valueFlags := set("-d", "-n", "--differences", "--interval")
	i := skipFlags(args, valueFlags)
	if i >= len(args) {
		return
	}
	text, ok := joined(args[i:])
	if !ok {
		a.problem("watch runs a command built at run time")
		return
	}
	a.nested(text, f)
}

func joined(args []Arg) (string, bool) {
	words := make([]string, 0, len(args))
	for _, x := range args {
		if !x.Literal {
			return "", false
		}
		words = append(words, x.Value)
	}
	return strings.Join(words, " "), true
}

func skipFlags(args []Arg, valueFlags map[string]bool) int {
	i := 0
	for i < len(args) && args[i].Literal && strings.HasPrefix(args[i].Value, "-") {
		if args[i].Value == "--" {
			return i + 1
		}
		if valueFlags[args[i].Value] {
			i++
		}
		i++
	}
	return i
}

func (a *analyzer) find(args []Arg, inline map[string]Arg, in stdin, f flags) {
	for i := 0; i < len(args); i++ {
		switch args[i].Value {
		case "-exec", "-execdir", "-ok", "-okdir":
			end := i + 1
			for end < len(args) && args[end].Value != ";" && args[end].Value != "+" {
				end++
			}
			if i+1 < end {
				cmd := make([]Arg, 0, end-i)
				for _, x := range args[i+1 : end] {
					if x.Value == "{}" {
						x = computed
					}
					cmd = append(cmd, x)
				}
				a.emit(cmd, inline, in, f)
			}
			i = end
		}
	}
}

func (a *analyzer) eval(args []Arg, f flags) {
	text, ok := joined(args)
	if !ok {
		a.problem("eval builds its command at run time")
		return
	}
	a.nested(text, f)
}

func (a *analyzer) nested(text string, f flags) {
	f.depth++
	a.parse(text, f)
}

func (a *analyzer) shell(args []Arg, in stdin, f flags) {
	command := false
	for i := 0; i < len(args); i++ {
		x := args[i]
		if !x.Literal {
			if command {
				a.problem("a shell runs a command built at run time")
			} else {
				a.problem("a shell runs a script named at run time")
			}
			return
		}
		v := x.Value
		switch {
		case v == "--":
			if i+1 < len(args) {
				a.shellOperand(args[i+1:], command, in, f)
				return
			}
		case shellValueOptions[v]:
			i++
		case isCommandFlag(v):
			command = true
		case strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+"):
		default:
			a.shellOperand(args[i:], command, in, f)
			return
		}
	}
	if command {
		return
	}
	switch {
	case in.hasHeredoc && in.heredocLiteral:
		a.nested(in.heredoc, f)
	case in.hasHeredoc:
		a.problem("a here-document with expansions is fed to a shell")
	case in.file != nil && in.file.Literal:
		a.add(Invocation{Tool: ScriptTool, Args: []Arg{*in.file}}, nil, stdin{})
	case in.piped || in.file != nil:
		a.problem("text is piped into a shell")
	}
}

func (a *analyzer) shellOperand(args []Arg, command bool, in stdin, f flags) {
	switch {
	case !args[0].Literal && command:
		a.problem("a shell runs a command built at run time")
	case !args[0].Literal:
		a.problem("a shell runs a script named at run time")
	case command:
		a.nested(args[0].Value, f)
	default:
		a.add(Invocation{Tool: ScriptTool, Args: args}, nil, in)
	}
}

// isCommandFlag recognises -c, clusters holding it such as -lc and -ce, and
// fish's --command.
func isCommandFlag(v string) bool {
	if v == "--command" {
		return true
	}
	return len(v) >= 2 && v[0] == '-' && v[1] != '-' && strings.Contains(v, "c")
}

func (a *analyzer) script(args []Arg, inline map[string]Arg, in stdin) {
	if len(args) == 0 {
		return
	}
	if args[0].Subst {
		a.problem("a script is read from another command")
		return
	}
	a.add(Invocation{Tool: ScriptTool, Args: args[:1]}, inline, in)
}

func (a *analyzer) interpreter(args []Arg, inline map[string]Arg, in stdin) {
	for i := 0; i < len(args); i++ {
		x := args[i]
		if !x.Literal {
			return
		}
		if (inlineCodeFlags[x.Value] || inlineCodeCluster.MatchString(x.Value)) && i+1 < len(args) {
			a.add(Invocation{Tool: CodeTool, Args: args[i+1 : i+2]}, inline, in)
			return
		}
		if x.Value == "-m" {
			return
		}
		if interpreterValueFlags[x.Value] {
			i++
			continue
		}
		if !strings.HasPrefix(x.Value, "-") {
			a.add(Invocation{Tool: ScriptTool, Args: args[i : i+1]}, inline, in)
			return
		}
	}
	switch {
	case in.hasHeredoc && in.heredocLiteral:
		a.add(Invocation{Tool: CodeTool, Args: []Arg{{Value: in.heredoc, Literal: true}}}, inline, in)
	case in.hasHeredoc:
		a.problem("a here-document with expansions is fed to an interpreter")
	case in.file != nil && in.file.Literal:
		a.add(Invocation{Tool: ScriptTool, Args: []Arg{*in.file}}, inline, stdin{})
	case in.piped || in.file != nil:
		a.problem("text is piped into an interpreter")
	}
}

func (a *analyzer) cd(args []Arg) {
	for _, x := range args {
		if x.Literal && (x.Value == "-P" || x.Value == "-L" || x.Value == "-e" || x.Value == "--") {
			continue
		}
		if !x.Literal || x.Value == "-" || !a.dirKnown {
			a.dirKnown = false
			return
		}
		if filepath.IsAbs(x.Value) {
			a.dir = filepath.Clean(x.Value)
		} else {
			a.dir = filepath.Join(a.dir, x.Value)
		}
		return
	}
	a.dirKnown = false
}

package shell

import (
	"maps"
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

var interpreterName = regexp.MustCompile(`^(node|perl|php|python|ruby)[0-9.]*$`)

// interpreterSpec is per language because the same flag differs: -p is code for
// node and a loop for perl, -r is code for php and a library for ruby.
type interpreterSpec struct {
	code map[string]bool
	// cluster matches a flag group ending in the code flag, as in perl -ne.
	cluster *regexp.Regexp
	value   map[string]bool
	// attached are flags whose value can follow directly, so ruby -rtime is a
	// library whose name happens to end in the code flag.
	attached []string
}

func (s interpreterSpec) isCode(v string) bool {
	if s.code[v] {
		return true
	}
	for _, flag := range s.attached {
		if strings.HasPrefix(v, flag) {
			return false
		}
	}
	return s.cluster != nil && s.cluster.MatchString(v)
}

var interpreterSpecs = map[string]interpreterSpec{
	"node":   {code: set("-e", "--eval", "-p", "--print", "-pe"), value: set("-r", "--require", "--import", "--loader", "-C", "--conditions")},
	"perl":   {cluster: regexp.MustCompile(`^-[A-Za-z0-9]*[eE]$`), value: set("-I"), attached: []string{"-I", "-M", "-m"}},
	"php":    {code: set("-r", "-B", "-R", "-E"), value: set("-d", "-c", "-z")},
	"python": {cluster: regexp.MustCompile(`^-[A-Za-z]*c$`), value: set("-W", "-X"), attached: []string{"-W", "-X"}},
	"ruby":   {cluster: regexp.MustCompile(`^-[A-Za-z]*e$`), value: set("-r", "-I", "-C", "-E"), attached: []string{"-r", "-I", "-C", "-E"}},
}

var stdinPaths = set("/dev/stdin", "/dev/fd/0", "/proc/self/fd/0")

// shellValueOptions take the next word, which must not be read as a script.
var shellValueOptions = set("--init-file", "--rcfile")

// optionCluster matches a flag group holding o or O, as in -euo pipefail, where the
// next word is the option's name.
var optionCluster = regexp.MustCompile(`^[-+][A-Za-z]*[oO][A-Za-z]*$`)

func (a *analyzer) emit(args []Arg, inline map[string]Arg, in stdin, f flags) {
	if len(a.out.Invocations) >= maxInvocations {
		a.problem("too many commands to read")
		return
	}
	if len(args) == 0 {
		return
	}
	head := args[0].program()
	args = append([]Arg{head}, args[1:]...)
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
		a.parallel(rest, in, f)
		return
	case name == "watch":
		a.watch(rest, f)
		return
	case name == "find":
		a.find(rest, inline, in, f)
		a.add(Invocation{Tool: name}, inline, in)
		return
	case name == "eval":
		a.eval(rest, f)
		return
	case shells[name]:
		outer := a.env
		a.env = maps.Clone(outer)
		maps.Copy(a.env, inline)
		a.shell(rest, in, f)
		a.env = outer
		return
	case name == "unset":
		for _, x := range rest {
			a.env[x.Value] = computed
		}
		return
	case name == "source" || name == ".":
		a.script(rest, inline, in, f)
		return
	case interpreterName.MatchString(name):
		a.interpreter(interpreterName.FindStringSubmatch(name)[1], rest, inline, in)
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
		// What make runs is in the Makefile, not in the make binary.
		if name == "make" || name == "gmake" {
			a.add(Invocation{Tool: name, Args: rest}, inline, in)
		}
		return
	}
	a.add(Invocation{Tool: name, Bare: bare, Args: rest}, inline, in)
	a.noteTargetChange(name, rest)
}

// ScriptTool marks an invocation whose first argument is a program file to read.
const ScriptTool = "<script>"

// BlockTool marks a command group that redirects its output. It reads as words
// because reasons quote it: "x is written by a command group".
const BlockTool = "a command group"

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
	hasAll := func(items ...string) bool {
		for _, s := range items {
			if !has(s) {
				return false
			}
		}
		return true
	}
	var reason string
	switch name {
	case "kubectx", "kubens", "kctx":
		reason = name
	case "kubectl", "oc":
		if name == "oc" && (has("project") || has("login")) {
			reason = "oc project or login"
		}
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
		if hasAll("eks", "update-kubeconfig") {
			reason = "aws eks update-kubeconfig"
		}
	case "gcloud":
		if hasAll("container", "get-credentials") {
			reason = "gcloud get-credentials"
		}
	case "az":
		if hasAll("aks", "get-credentials") {
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
		case strings.HasPrefix(x.Value, "-S") || strings.HasPrefix(x.Value, "--split-string"):
			a.problem("env -S builds its command at run time")
			return
		case x.Value == "-u" || x.Value == "--unset":
			if i+1 < len(args) {
				inline = withEnv(inline, args[i+1].Value, computed)
			}
			i += 2
		case strings.HasPrefix(x.Value, "--unset="):
			inline = withEnv(inline, strings.TrimPrefix(x.Value, "--unset="), computed)
			i++
		case strings.HasPrefix(x.Value, "-u"):
			inline = withEnv(inline, x.Value[2:], computed)
			i++
		case x.Value == "-i" || x.Value == "--ignore-environment":
			inline = withEnv(inline, "KUBECONFIG", computed)
			i++
		case x.Value == "-C" || x.Value == "--chdir":
			a.dirKnown = false
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
func (a *analyzer) parallel(args []Arg, in stdin, f flags) {
	valueFlags := set("-a", "-j", "-n", "-S", "--arg-file", "--jobs", "--max-args", "--sshlogin")
	i := skipFlags(args, valueFlags)
	end := i
	for end < len(args) && args[end].Value != ":::" && args[end].Value != "::::" {
		end++
	}
	if i >= end {
		a.parallelInput(args, end, in, f)
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

// parallelInput handles parallel with no command word, where each input is itself
// a command line. operands is where ::: or :::: starts, if either was given.
func (a *analyzer) parallelInput(args []Arg, operands int, in stdin, f flags) {
	if operands < len(args) && args[operands].Value == ":::" {
		for _, x := range args[operands+1:] {
			if !x.Literal {
				a.problem("parallel runs a command built at run time")
				return
			}
			a.nested(x.Value, f)
		}
		return
	}
	fromFile := operands < len(args)
	for _, x := range args[:operands] {
		if x.Value == "-a" || strings.HasPrefix(x.Value, "--arg-file") {
			fromFile = true
		}
	}
	if fromFile || in.piped || in.file != nil || in.hasHeredoc {
		a.problem("parallel runs commands read from its input")
	}
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
	command, fromStdin := false, false
loop:
	for i := 0; i < len(args); i++ {
		x := args[i].program()
		if !x.Literal {
			if fromStdin && !command {
				break
			}
			if command {
				a.problem("a shell runs a command built at run time")
			} else {
				a.problem("a shell runs a script named at run time")
			}
			return
		}
		v := x.Value
		if optionCluster.MatchString(v) {
			i++
		}
		switch {
		case v == "--":
			if fromStdin && !command {
				break loop
			}
			if i+1 < len(args) {
				a.shellOperand(args[i+1:], command, in, f)
				return
			}
		case shellValueOptions[v]:
			i++
		case strings.HasPrefix(v, "--command="):
			a.nested(strings.TrimPrefix(v, "--command="), f)
			return
		case isCommandFlag(v):
			command = true
		case isStdinFlag(v):
			fromStdin = true
		case strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+"):
		default:
			if fromStdin && !command {
				break loop
			}
			a.shellOperand(args[i:], command, in, f)
			return
		}
	}
	if !command {
		a.shellStdin(in, f)
	}
}

func (a *analyzer) shellOperand(args []Arg, command bool, in stdin, f flags) {
	x := args[0].program()
	switch {
	case !x.Literal && command:
		a.problem("a shell runs a command built at run time")
	case !x.Literal:
		a.problem("a shell runs a script named at run time")
	case command:
		a.nested(x.Value, f)
	case stdinPaths[x.Value]:
		a.shellStdin(in, f)
	default:
		a.add(Invocation{Tool: ScriptTool, Args: append([]Arg{x}, args[1:]...)}, nil, in)
	}
}

func (a *analyzer) shellStdin(in stdin, f flags) {
	a.stdinProgram("a shell", in,
		func(text string) { a.nested(text, f) },
		func(file Arg) { a.add(Invocation{Tool: ScriptTool, Args: []Arg{file}}, nil, stdin{}) })
}

// stdinProgram handles program text arriving on stdin: bash -s, whose following
// words are arguments, or a path in stdinPaths.
func (a *analyzer) stdinProgram(what string, in stdin, text func(string), file func(Arg)) {
	switch {
	case in.hasHeredoc && in.heredocLiteral:
		text(in.heredoc)
	case in.hasHeredoc:
		a.problem("a here-document with expansions is fed to " + what)
	case in.file != nil && in.file.Literal:
		file(*in.file)
	case in.piped || in.file != nil:
		a.problem("text is piped into " + what)
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

// isStdinFlag recognises -s and clusters holding it such as -es.
func isStdinFlag(v string) bool {
	return len(v) >= 2 && v[0] == '-' && v[1] != '-' && strings.Contains(v, "s")
}

func (a *analyzer) script(args []Arg, inline map[string]Arg, in stdin, f flags) {
	if len(args) == 0 {
		return
	}
	x := args[0].program()
	if x.Subst {
		a.problem("a script is read from another command")
		return
	}
	if stdinPaths[x.Value] {
		a.shellStdin(in, f)
		return
	}
	a.add(Invocation{Tool: ScriptTool, Args: []Arg{x}}, inline, in)
	// A sourced file can set KUBECONFIG or TF_WORKSPACE, and the hook does not run it.
	a.targetChange = strings.TrimSpace("source " + x.Value)
}

func (a *analyzer) interpreter(name string, args []Arg, inline map[string]Arg, in stdin) {
	spec := interpreterSpecs[name]
	hadCode := false
	for i := 0; i < len(args); i++ {
		x := args[i].program()
		if !x.Literal {
			if !hadCode {
				a.problem("an interpreter runs a script named at run time")
			}
			return
		}
		v := x.Value
		switch {
		case spec.isCode(v):
			if i+1 == len(args) {
				return
			}
			i++
			a.add(Invocation{Tool: CodeTool, Args: args[i : i+1]}, inline, in)
			hadCode = true
		case name == "python" && v == "-m":
			a.add(Invocation{Tool: name, Args: args[i+1:]}, inline, in)
			return
		case spec.value[v]:
			i++
		case v == "-":
			if !hadCode {
				a.interpreterStdin(inline, in)
			}
			return
		case strings.HasPrefix(v, "-"):
			// The flag may hold the program, as in perl -e'...' and node --eval=...
			if len(v) > 2 {
				a.add(Invocation{Tool: CodeTool, Args: []Arg{x}}, inline, in)
			}
		case hadCode:
			return
		case stdinPaths[v]:
			a.interpreterStdin(inline, in)
			return
		default:
			a.add(Invocation{Tool: ScriptTool, Args: []Arg{x}}, inline, in)
			return
		}
	}
	if !hadCode {
		a.interpreterStdin(inline, in)
	}
}

func (a *analyzer) interpreterStdin(inline map[string]Arg, in stdin) {
	a.stdinProgram("an interpreter", in,
		func(text string) {
			a.add(Invocation{Tool: CodeTool, Args: []Arg{{Value: text, Literal: true}}}, inline, in)
		},
		func(file Arg) { a.add(Invocation{Tool: ScriptTool, Args: []Arg{file}}, inline, stdin{}) })
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

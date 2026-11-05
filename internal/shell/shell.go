// Package shell turns a command line into the program invocations it would
// start, in order, as far as that can be known without running anything.
package shell

import (
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Arg is one word of a command. Literal is false when the shell would compute
// its value at run time, so Value cannot be trusted.
type Arg struct {
	Value   string
	Literal bool
	// Subst is true when the value comes from running a command.
	Subst bool
	// Home is true for a plain ~/path. Its value still depends on the environment,
	// so it stays non-literal except where a program file is being named.
	Home bool
}

// program reads the word as a program file, so one named from ~/ is still read.
func (x Arg) program() Arg {
	if x.Home {
		x.Literal = true
	}
	return x
}

// Invocation is one program the command would start.
type Invocation struct {
	// Tool is the program's base name after aliases resolve. Empty means the
	// program's name is itself computed at run time.
	Tool string
	// Bare is true when the program is found through PATH under its plain name. A
	// path the command chose or a versioned name such as terraform-1.9 is not bare,
	// and only a bare program is ever run by a probe.
	Bare bool
	Args []Arg
	// Env holds assignments made inline or earlier in the same command line.
	Env map[string]Arg
	// Dir is the working directory; DirKnown is false when the text cannot settle it.
	Dir      string
	DirKnown bool
	// Piped means stdin comes from an earlier command or a file.
	Piped bool
	// HasHeredoc is true when stdin is a here-document; HeredocLiteral when its
	// text has no expansions, in which case Heredoc holds it.
	HasHeredoc     bool
	HeredocLiteral bool
	Heredoc        string
	// TargetChange names an earlier step that changes the cluster context or
	// workspace, which makes anything probed now describe the wrong target.
	TargetChange string
	// Writes lists the literal files its output is redirected to.
	Writes []string
}

// NamesFile reports whether a literal argument or redirect names the file,
// including in the option=value form.
func (i Invocation) NamesFile(path string) bool {
	base := filepath.Base(path)
	for _, w := range i.Writes {
		if filepath.Base(w) == base {
			return true
		}
	}
	return i.ArgNamesFile(path)
}

// ArgNamesFile is NamesFile without the redirects.
func (i Invocation) ArgNamesFile(path string) bool {
	base := filepath.Base(path)
	for _, v := range i.Literals() {
		if strings.HasPrefix(v, "-") && !strings.HasPrefix(v, "--") && len(v) > 2 && filepath.Base(v[2:]) == base {
			return true
		}
		if _, after, ok := strings.Cut(v, "="); ok {
			v = after
		}
		if filepath.Base(v) == base {
			return true
		}
	}
	return false
}

// Literals returns the literal argument values, skipping computed ones.
func (i Invocation) Literals() []string {
	out := make([]string, 0, len(i.Args))
	for _, a := range i.Args {
		if a.Literal {
			out = append(out, a.Value)
		}
	}
	return out
}

// Analysis is the outcome of reading a command line.
type Analysis struct {
	Invocations []Invocation
	// Problems lists constructs that hide what would run.
	Problems []string
	// ParseError is set when the text is not valid shell.
	ParseError error
}

const (
	maxDepth       = 5
	maxInvocations = 2000
)

// Analyze reads command as bash, starting in cwd.
func Analyze(command, cwd string) Analysis {
	a := &analyzer{dir: cwd, dirKnown: cwd != "", env: map[string]Arg{}}
	a.parse(command, flags{})
	return a.out
}

type flags struct {
	piped bool
	depth int
}

type analyzer struct {
	out          Analysis
	dir          string
	dirKnown     bool
	env          map[string]Arg
	targetChange string
}

func (a *analyzer) parse(text string, f flags) {
	if f.depth > maxDepth {
		a.problem("commands nested too deeply to read")
		return
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
	if err != nil {
		if f.depth == 0 {
			a.out.ParseError = err
		} else {
			a.problem("a nested command could not be parsed")
		}
		return
	}
	a.stmts(file.Stmts, f)
}

func (a *analyzer) problem(p string) {
	for _, have := range a.out.Problems {
		if have == p {
			return
		}
	}
	a.out.Problems = append(a.out.Problems, p)
}

func (a *analyzer) stmts(list []*syntax.Stmt, f flags) {
	for _, s := range list {
		a.stmt(s, f)
	}
}

func (a *analyzer) stmt(s *syntax.Stmt, f flags) {
	if s == nil {
		return
	}
	for _, r := range s.Redirs {
		a.substs(r.Word, f)
		a.substs(r.Hdoc, f)
	}
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		a.call(c, s, f)
	case *syntax.BinaryCmd:
		switch c.Op {
		case syntax.Pipe, syntax.PipeAll:
			a.isolated(func() { a.stmt(c.X, f) })
			piped := f
			piped.piped = true
			a.isolated(func() { a.stmt(c.Y, piped) })
		case syntax.OrStmt:
			a.stmt(c.X, f)
			a.conditional(func() { a.stmt(c.Y, f) })
		default:
			a.andChain(c, f)
		}
	case *syntax.Block:
		a.stmts(c.Stmts, f)
	case *syntax.Subshell:
		a.isolated(func() { a.stmts(c.Stmts, f) })
	case *syntax.IfClause:
		a.conditional(func() {
			for cl := c; cl != nil; cl = cl.Else {
				a.stmts(cl.Cond, f)
				a.stmts(cl.Then, f)
			}
		})
	case *syntax.WhileClause:
		a.conditional(func() {
			a.stmts(c.Cond, f)
			a.stmts(c.Do, f)
		})
	case *syntax.ForClause:
		a.substs(c.Loop, f)
		a.conditional(func() { a.stmts(c.Do, f) })
	case *syntax.CaseClause:
		a.substs(c.Word, f)
		a.conditional(func() {
			for _, item := range c.Items {
				for _, p := range item.Patterns {
					a.substs(p, f)
				}
				a.stmts(item.Stmts, f)
			}
		})
	case *syntax.FuncDecl:
		a.conditional(func() { a.stmt(c.Body, f) })
	case *syntax.TimeClause:
		a.stmt(c.Stmt, f)
	case *syntax.CoprocClause:
		a.stmt(c.Stmt, f)
	case *syntax.DeclClause:
		for _, as := range c.Args {
			a.substs(as, f)
			if as.Name != nil {
				a.env[as.Name.Value] = assignArg(as)
			}
		}
	default:
		if c != nil {
			a.substs(c, f)
		}
	}
}

// andChain keeps the directory certain inside `a && b && c`, where each step only
// runs if the last one worked. Afterwards it is certain only if the first step
// made the change, because any later step may not have run.
func (a *analyzer) andChain(c *syntax.BinaryCmd, f flags) {
	var leaves []*syntax.Stmt
	var cur syntax.Command = c
	for {
		b, ok := cur.(*syntax.BinaryCmd)
		if !ok || b.Op != syntax.AndStmt {
			break
		}
		leaves = append([]*syntax.Stmt{b.Y}, leaves...)
		if b.X == nil {
			break
		}
		if _, nested := b.X.Cmd.(*syntax.BinaryCmd); !nested {
			leaves = append([]*syntax.Stmt{b.X}, leaves...)
			break
		}
		cur = b.X.Cmd
		if inner := cur.(*syntax.BinaryCmd); inner.Op != syntax.AndStmt {
			leaves = append([]*syntax.Stmt{b.X}, leaves...)
			break
		}
	}
	a.stmt(leaves[0], f)
	a.conditional(func() {
		for _, leaf := range leaves[1:] {
			a.stmt(leaf, f)
		}
	})
}

// conditional runs fn for steps that may not run: a directory change inside is
// visible to the steps after it in fn, but unknown once fn is over.
func (a *analyzer) conditional(fn func()) {
	dir := a.dir
	fn()
	if a.dir != dir {
		a.dirKnown = false
	}
}

// isolated runs fn and restores the state a subshell cannot change for its parent.
func (a *analyzer) isolated(fn func()) {
	dir, known, change := a.dir, a.dirKnown, a.targetChange
	env := make(map[string]Arg, len(a.env))
	for k, v := range a.env {
		env[k] = v
	}
	fn()
	a.dir, a.dirKnown, a.targetChange, a.env = dir, known, change, env
}

// substs finds commands hidden in words, such as $(...) and <(...).
func (a *analyzer) substs(node syntax.Node, f flags) {
	if isNil(node) {
		return
	}
	syntax.Walk(node, func(n syntax.Node) bool {
		switch c := n.(type) {
		case *syntax.CmdSubst:
			a.isolated(func() { a.stmts(c.Stmts, flags{depth: f.depth}) })
			return false
		case *syntax.ProcSubst:
			a.isolated(func() { a.stmts(c.Stmts, flags{depth: f.depth}) })
			return false
		}
		return true
	})
}

func isNil(n syntax.Node) bool {
	switch v := n.(type) {
	case nil:
		return true
	case *syntax.Word:
		return v == nil
	case *syntax.Assign:
		return v == nil
	}
	return false
}

func (a *analyzer) call(c *syntax.CallExpr, s *syntax.Stmt, f flags) {
	inline := map[string]Arg{}
	for _, as := range c.Assigns {
		a.substs(as, f)
		if as.Name != nil {
			inline[as.Name.Value] = assignArg(as)
		}
	}
	args := make([]Arg, 0, len(c.Args))
	for _, w := range c.Args {
		a.substs(w, f)
		args = append(args, wordArg(w))
	}
	if len(args) == 0 {
		for k, v := range inline {
			a.env[k] = v
		}
		return
	}
	in := stdinOf(s, f.piped)
	a.emit(args, inline, in, f)
}

// stdin describes a statement's redirects: where it reads from and the files it writes.
type stdin struct {
	piped          bool
	file           *Arg
	writes         []string
	hasHeredoc     bool
	heredocLiteral bool
	heredoc        string
}

func stdinOf(s *syntax.Stmt, piped bool) stdin {
	in := stdin{piped: piped}
	for _, r := range s.Redirs {
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			in.hasHeredoc = true
			arg := wordArg(r.Hdoc)
			if r.Hdoc == nil {
				arg = Arg{Literal: true}
			}
			in.heredoc, in.heredocLiteral = arg.Value, arg.Literal
		case syntax.WordHdoc:
			in.hasHeredoc = true
			arg := wordArg(r.Word)
			in.heredoc, in.heredocLiteral = arg.Value, arg.Literal
		case syntax.RdrIn:
			arg := wordArg(r.Word)
			in.file = &arg
		case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
			if arg := wordArg(r.Word); arg.Literal {
				in.writes = append(in.writes, arg.Value)
			}
		}
	}
	return in
}

// assignArg reads an assignment. An array keeps its words joined, so that
// `cmd=(terraform destroy)` can be searched like `cmd="terraform destroy"`.
func assignArg(as *syntax.Assign) Arg {
	if as.Array == nil {
		return wordArg(as.Value)
	}
	words := make([]string, 0, len(as.Array.Elems))
	for _, el := range as.Array.Elems {
		arg := wordArg(el.Value)
		if !arg.Literal {
			return Arg{}
		}
		words = append(words, arg.Value)
	}
	return Arg{Value: strings.Join(words, " "), Literal: true}
}

// wordArg resolves a word to its value when no expansion can change it.
func wordArg(w *syntax.Word) Arg {
	if w == nil {
		return Arg{Literal: true}
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if rest, home := strings.CutPrefix(p.Value, "~/"); home && len(w.Parts) == 1 && !expands(rest) {
				return Arg{Value: unescape(p.Value), Home: true}
			}
			if expands(p.Value) {
				return Arg{Value: p.Value}
			}
			b.WriteString(unescape(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return Arg{Value: p.Value}
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return Arg{Subst: runsCommand(inner)}
				}
				b.WriteString(unescapeDouble(lit.Value))
			}
		default:
			return Arg{Subst: runsCommand(part)}
		}
	}
	return Arg{Value: b.String(), Literal: true}
}

// braceList matches what bash expands: {a,b} and {1..3}. It leaves {} and {x} alone,
// so xargs -I{} is a plain word.
var braceList = regexp.MustCompile(`\{[^}]*(?:,|\.\.)[^}]*\}`)

func expands(s string) bool {
	return strings.ContainsAny(s, "*?[~") || braceList.MatchString(s)
}

func runsCommand(part syntax.WordPart) bool {
	switch part.(type) {
	case *syntax.CmdSubst, *syntax.ProcSubst:
		return true
	}
	return false
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// baseName returns the program name and whether it was named without a path.
func baseName(head string) (string, bool) {
	bare := !strings.ContainsAny(head, `/\`)
	name := filepath.Base(strings.ReplaceAll(head, `\`, "/"))
	return strings.TrimSuffix(name, ".exe"), bare
}

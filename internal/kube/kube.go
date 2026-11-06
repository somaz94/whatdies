// Package kube judges kubectl commands by what they would remove in the cluster.
package kube

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

// Deps are the outside inputs of a verdict.
type Deps struct {
	Runner runner.Runner
	// NoProbe, when set, is why the cluster must not be queried at all.
	NoProbe string
	// Getenv reads the environment whatdies runs in; nil means os.Getenv.
	Getenv func(string) string
}

// Each probe is bounded on its own as well as by the hook's deadline, because
// an unreachable API server would otherwise use all of it on the first call.
const requestTimeout = "--request-timeout=3s"

const maxTargets = 10

// validName is checked on every name passed to a probe, including names from the
// cluster. A name shaped like --kubeconfig=x would become an option of kubectl get
// and load a config the command chose, running its exec plugin before approval.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// globalValueFlags take a value and may appear before the verb.
var globalValueFlags = map[string]bool{
	"--as": true, "--as-group": true, "--as-uid": true, "--cache-dir": true, "--certificate-authority": true,
	"--client-certificate": true, "--client-key": true, "--cluster": true, "--context": true, "--kubeconfig": true,
	"--namespace": true, "-n": true, "--password": true, "--profile": true, "--profile-output": true,
	"--request-timeout": true, "--server": true, "-s": true, "--tls-server-name": true, "--token": true,
	"--user": true, "--username": true, "--v": true, "-v": true, "--vmodule": true,
}

// verbValueFlags take a value after the verb.
var verbValueFlags = map[string]bool{
	"--cascade": true, "--dry-run": true, "--field-selector": true, "--filename": true, "-f": true,
	"--grace-period": true, "--kustomize": true, "-k": true, "--output": true, "-o": true,
	"--patch": true, "--patch-file": true, "-p": true, "--raw": true, "--replicas": true, "--selector": true, "-l": true, "--timeout": true,
}

// targetFlags point kubectl at something other than the user's configuration.
var targetFlags = []string{"--as", "--as-group", "--as-uid", "--certificate-authority", "--client-certificate",
	"--client-key", "--cluster", "--kubeconfig", "--password", "--server", "-s", "--tls-server-name", "--token",
	"--user", "--username"}

// ocQuiet are oc's own verbs that remove nothing.
var ocQuiet = map[string]bool{
	"login": true, "logout": true, "new-app": true, "new-project": true, "project": true,
	"projects": true, "status": true, "whoami": true,
}

var quiet = map[string]bool{
	"annotate": true, "api-resources": true, "api-versions": true, "auth": true, "autoscale": true,
	"certificate": true, "cluster-info": true, "completion": true, "config": true, "cordon": true, "cp": true,
	"create": true, "describe": true, "diff": true, "events": true, "explain": true, "expose": true, "get": true,
	"help": true, "kustomize": true, "label": true, "logs": true, "options": true, "plugin": true, "port-forward": true,
	"rollout": true, "set": true, "top": true, "uncordon": true, "version": true, "wait": true,
}

type command struct {
	tool       string
	verb       string
	positional []shell.Arg
	flags      map[string]string
	computed   bool
	// dashDash is true when words follow --, the command for run or exec.
	dashDash bool
}

func (c command) has(names ...string) bool {
	for _, n := range names {
		if _, ok := c.flags[n]; ok {
			return true
		}
	}
	return false
}

func (c command) value(names ...string) string {
	for _, n := range names {
		if v, ok := c.flags[n]; ok {
			return v
		}
	}
	return ""
}

func parse(args []shell.Arg) (command, string) {
	c := command{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.Literal {
			c.computed = true
			if c.verb == "" {
				return c, "the verb or an option before it is computed at run time"
			}
			continue
		}
		v := a.Value
		if v == "--" {
			c.dashDash = i+1 < len(args)
			break
		}
		if !strings.HasPrefix(v, "-") || v == "-" {
			if c.verb == "" {
				c.verb = v
			} else {
				c.positional = append(c.positional, a)
			}
			continue
		}
		name, value, hasValue := strings.Cut(v, "=")
		if !strings.HasPrefix(v, "--") && len(v) > 2 && !hasValue {
			if short := v[:2]; globalValueFlags[short] || verbValueFlags[short] {
				name, value, hasValue = short, v[2:], true
			}
		}
		takesValue := globalValueFlags[name] || verbValueFlags[name]
		if takesValue && !hasValue {
			i++
			if i >= len(args) {
				break
			}
			if !args[i].Literal {
				c.computed = true
			}
			value = args[i].Value
		}
		if !takesValue && c.verb == "" {
			return c, fmt.Sprintf("unknown option %q before the verb", v)
		}
		c.flags[name] = value
	}
	return c, ""
}

// Evaluate judges one kubectl invocation.
func Evaluate(ctx context.Context, inv shell.Invocation, d Deps) verdict.Verdict {
	c, problem := parse(inv.Args)
	c.tool = inv.Tool
	if problem != "" {
		return verdict.AskBecause(c.tool + ": " + problem)
	}
	if c.verb == "" || quiet[c.verb] || (c.tool == "oc" && ocQuiet[c.verb]) {
		return verdict.Verdict{}
	}
	if dry := c.value("--dry-run"); dry == "client" || dry == "server" {
		return verdict.Verdict{}
	}
	switch c.verb {
	case "apply":
		if c.has("--prune") {
			return verdict.AskBecause(c.tool + " apply --prune deletes objects missing from the input")
		}
		if c.has("--force") {
			return verdict.AskBecause(c.tool + " apply --force deletes and recreates objects")
		}
		return verdict.Verdict{}
	case "replace":
		if c.has("--force") {
			return verdict.AskBecause(c.tool + " replace --force deletes and recreates objects")
		}
		return verdict.Verdict{}
	case "scale":
		if v := c.value("--replicas"); v == "0" || c.computed {
			return verdict.AskBecause(c.tool + " scale to zero stops every replica")
		}
		return verdict.Verdict{}
	case "patch":
		if c.computed || c.has("--patch-file") || containsLiteral(inv, "finalizers") {
			return verdict.AskBecause(c.tool + " patch may clear finalizers, which skips cleanup and can orphan data")
		}
		return verdict.Verdict{}
	case "drain":
		return verdict.AskBecause(c.tool + " drain evicts every pod on the node")
	case "taint":
		return verdict.AskBecause(c.tool + " taint can evict pods from the node")
	case "exec", "debug", "attach":
		return verdict.AskBecause(c.tool + " " + c.verb + " runs commands inside the cluster")
	case "run":
		if c.dashDash {
			return verdict.AskBecause(c.tool + " run with a command runs it inside the cluster")
		}
		return verdict.Verdict{}
	case "delete":
		return evaluateDelete(ctx, inv, c, d)
	}
	return verdict.AskBecause(fmt.Sprintf("%s %s is not a verb whatdies knows", c.tool, strconv.Quote(c.verb)))
}

func containsLiteral(inv shell.Invocation, needle string) bool {
	for _, v := range inv.Literals() {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

// target is one kind of object named by a delete.
type target struct {
	kind  string
	names []string
}

var kinds = map[string]string{
	"ns": "namespace", "namespace": "namespace", "namespaces": "namespace",
	"pvc": "pvc", "persistentvolumeclaim": "pvc", "persistentvolumeclaims": "pvc",
	"pv": "pv", "persistentvolume": "pv", "persistentvolumes": "pv",
	"sts": "sts", "statefulset": "sts", "statefulsets": "sts", "statefulset.apps": "sts", "statefulsets.apps": "sts",
	"crd": "crd", "crds": "crd", "customresourcedefinition": "crd", "customresourcedefinitions": "crd",
	"customresourcedefinition.apiextensions.k8s.io": "crd", "customresourcedefinitions.apiextensions.k8s.io": "crd",
}

func evaluateDelete(ctx context.Context, inv shell.Invocation, c command, d Deps) verdict.Verdict {
	if reason := staticDelete(c); reason != "" {
		return verdict.AskBecause(reason)
	}
	targets, reason := parseTargets(c)
	if reason != "" {
		return verdict.AskBecause(reason)
	}
	reason = d.NoProbe
	if reason == "" {
		reason = noProbe(inv, c)
	}
	if reason != "" {
		return verdict.AskBecause(describe(targets) + "; not looked up because " + reason)
	}
	p := prober{run: d.Runner, context: c.value("--context"), namespace: c.value("-n", "--namespace")}
	var out verdict.Verdict
	for _, t := range targets {
		for _, name := range t.names {
			out = verdict.Join(out, p.judge(ctx, t.kind, name, c))
		}
	}
	return out
}

// staticDelete returns why a delete's objects cannot be listed ahead of time.
func staticDelete(c command) string {
	switch {
	case c.computed:
		return c.tool + " delete has an argument computed at run time, so its target is unknown"
	case c.has("--raw"):
		return c.tool + " delete --raw sends a raw API request"
	case c.has("-f", "--filename"):
		return c.tool + " delete -f removes whatever the input names"
	case c.has("-k", "--kustomize"):
		return c.tool + " delete -k removes whatever the kustomization builds"
	case c.has("-l", "--selector", "--field-selector"):
		return c.tool + " delete with a selector removes every match"
	case c.has("--all", "-A", "--all-namespaces"):
		return c.tool + " delete --all removes every object of the type"
	}
	return ""
}

func parseTargets(c command) ([]target, string) {
	if len(c.positional) == 0 {
		return nil, c.tool + " delete without a named target"
	}
	var out []target
	var current *target
	for _, a := range c.positional {
		v := a.Value
		if kind, name, ok := strings.Cut(v, "/"); ok {
			out = append(out, target{kind: canonical(kind), names: []string{name}})
			current = nil
			continue
		}
		if current == nil {
			if strings.Contains(v, ",") {
				return nil, c.tool + " delete names several resource types at once: " + v
			}
			if v == "all" {
				return nil, c.tool + " delete all removes most resources in the namespace"
			}
			out = append(out, target{kind: canonical(v)})
			current = &out[len(out)-1]
			continue
		}
		current.names = append(current.names, v)
	}
	count := 0
	for _, t := range out {
		if len(t.names) == 0 {
			return nil, c.tool + " delete " + t.kind + " has no object name"
		}
		for _, name := range t.names {
			if !validName.MatchString(name) {
				return nil, fmt.Sprintf("%s delete names an object that is not a valid name: %q", c.tool, name)
			}
		}
		count += len(t.names)
	}
	if count > maxTargets {
		return nil, fmt.Sprintf("%s delete names %d objects, too many to look up", c.tool, count)
	}
	return out, ""
}

func canonical(kind string) string {
	if k, ok := kinds[strings.ToLower(kind)]; ok {
		return k
	}
	return kind
}

// noProbe returns why the cluster must not be queried for this command. A
// kubeconfig can run a credential plugin, so one the command chose could run
// code the agent wrote before the user approved anything.
func noProbe(inv shell.Invocation, c command) string {
	if !inv.Bare {
		return "this is not the plain kubectl on PATH"
	}
	for _, v := range []string{c.value("--context"), c.value("-n", "--namespace")} {
		if strings.HasPrefix(v, "-") {
			return "the context or namespace looks like an option: " + v
		}
	}
	for _, f := range targetFlags {
		if c.has(f) {
			return "the command sets " + f
		}
	}
	for _, env := range []string{"KUBECONFIG", "PATH", "HOME"} {
		if _, ok := inv.Env[env]; ok {
			return "the command sets " + env
		}
	}
	if inv.TargetChange != "" {
		return "an earlier step (" + inv.TargetChange + ") changes the target first"
	}
	return ""
}

func describe(targets []target) string {
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		parts = append(parts, t.kind+" "+strings.Join(t.names, " "))
	}
	return "deletes " + strings.Join(parts, ", ")
}

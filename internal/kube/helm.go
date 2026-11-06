package kube

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

const (
	maxRelease       = 32 << 20
	maxManifestDepth = 3
)

var helmValueFlags = map[string]bool{
	"--burst-limit": true, "--cascade": true, "--description": true, "--history-max": true,
	"--kube-context": true, "--namespace": true, "-n": true, "--qps": true, "--registry-config": true,
	"--repository-cache": true, "--repository-config": true, "--timeout": true,
	"--kube-apiserver": true, "--kube-as-group": true, "--kube-as-user": true, "--kube-ca-file": true,
	"--kube-tls-server-name": true, "--kube-token": true, "--kubeconfig": true,
}

var helmBoolFlags = map[string]bool{
	"--cleanup-on-fail": true, "--debug": true, "--dry-run": true, "--force": true, "--ignore-not-found": true,
	"--keep-history": true, "--kube-insecure-skip-tls-verify": true, "--no-hooks": true,
	"--recreate-pods": true, "--wait": true, "--wait-for-jobs": true,
}

// helmTargetFlags point helm somewhere a lookup with kubectl would not follow.
var helmTargetFlags = []string{"--kube-apiserver", "--kube-as-group", "--kube-as-user", "--kube-ca-file",
	"--kube-insecure-skip-tls-verify", "--kube-tls-server-name", "--kube-token", "--kubeconfig"}

// helmTargetEnv move where helm keeps releases, set inline or in whatdies's own environment.
var helmTargetEnv = []string{"HELM_DRIVER", "HELM_NAMESPACE", "HELM_KUBECONTEXT", "HELM_KUBEAPISERVER",
	"HELM_KUBEASUSER", "HELM_KUBEASGROUPS", "HELM_KUBECAFILE", "HELM_KUBETOKEN",
	"HELM_KUBEINSECURE_SKIP_TLS_VERIFY", "HELM_KUBETLS_SERVER_NAME"}

type helmCommand struct {
	positional []string
	values     map[string]string
	on         map[string]bool
}

// parseHelm reads helm's arguments the way its flag parser does: a boolean flag
// can carry =false, and the last occurrence wins.
func parseHelm(args []shell.Arg) (helmCommand, string) {
	c := helmCommand{values: map[string]string{}, on: map[string]bool{}}
	verb := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.Literal {
			return c, "an argument is computed at run time"
		}
		v := a.Value
		if !strings.HasPrefix(v, "-") || v == "-" {
			if verb {
				c.positional = append(c.positional, v)
			}
			verb = true
			continue
		}
		name, value, hasValue := strings.Cut(v, "=")
		if strings.HasPrefix(v, "-n") && !strings.HasPrefix(v, "--") && len(v) > 2 && !hasValue {
			name, value, hasValue = "-n", v[2:], true
		}
		switch {
		case helmBoolFlags[name]:
			b := true
			if hasValue {
				var err error
				if b, err = strconv.ParseBool(value); err != nil {
					return c, fmt.Sprintf("the value of %s is not a boolean", name)
				}
			}
			c.on[name] = b
		case helmValueFlags[name]:
			if !hasValue {
				i++
				if i >= len(args) || !args[i].Literal {
					return c, "the value of " + name + " is missing or computed"
				}
				value = args[i].Value
			}
			c.values[name] = value
		default:
			return c, fmt.Sprintf("the option %q is not one whatdies knows", v)
		}
	}
	return c, ""
}

func (c helmCommand) value(names ...string) string {
	for _, n := range names {
		if v, ok := c.values[n]; ok {
			return v
		}
	}
	return ""
}

func (c helmCommand) has(name string) bool {
	_, ok := c.values[name]
	return ok || c.on[name]
}

// EvaluateHelm judges helm uninstall and rollback from the release they act on.
func EvaluateHelm(ctx context.Context, inv shell.Invocation, verb string, d Deps) verdict.Verdict {
	c, problem := parseHelm(inv.Args)
	if problem != "" {
		return verdict.AskBecause(fmt.Sprintf("helm %s: %s", verb, problem))
	}
	if c.on["--dry-run"] {
		return verdict.Verdict{}
	}
	if len(c.positional) == 0 {
		return verdict.AskBecause("helm " + verb + " names no release")
	}
	what := "helm " + verb + " " + strings.Join(c.positional, " ")
	if reason := helmNoProbe(inv, c, d); reason != "" {
		return verdict.AskBecause(what + " can remove what the release owns; not looked up because " + reason)
	}
	p := prober{run: d.Runner, context: c.value("--kube-context"), namespace: c.value("-n", "--namespace")}
	if verb == "rollback" {
		if len(c.positional) > 2 {
			return verdict.AskBecause(what + " has more arguments than a release and a revision")
		}
		revision := 0
		if len(c.positional) == 2 {
			n, err := strconv.Atoi(c.positional[1])
			if err != nil || n < 0 {
				return verdict.AskBecause(what + " names a revision that is not a number")
			}
			revision = n
		}
		return p.judgeRollback(ctx, c.positional[0], revision, c)
	}
	var out verdict.Verdict
	for _, name := range c.positional {
		out = verdict.Join(out, p.judgeUninstall(ctx, name, c))
	}
	return out
}

func helmNoProbe(inv shell.Invocation, c helmCommand, d Deps) string {
	if d.NoProbe != "" {
		return d.NoProbe
	}
	if !inv.Bare {
		return "this is not the plain helm on PATH"
	}
	if len(c.positional) > maxTargets {
		return "it names more releases than whatdies will look up"
	}
	for _, f := range helmTargetFlags {
		if c.has(f) {
			return "the command sets " + f
		}
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, env := range helmTargetEnv {
		if _, ok := inv.Env[env]; ok {
			return "the command sets " + env
		}
		if v := getenv(env); v != "" && !(env == "HELM_DRIVER" && (v == "secret" || v == "secrets")) {
			return env + " is set, so helm may not keep its releases where kubectl would look"
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
	if inv.RunAs != "" {
		return inv.RunAs + " runs it as another user, whose kubeconfig is not this one"
	}
	// A context can be an ARN, so it gets kubectl's rule; it is passed as its own argument.
	if kc := c.value("--kube-context"); strings.HasPrefix(kc, "-") {
		return "the context looks like an option: " + kc
	}
	for _, v := range append([]string{c.value("-n", "--namespace")}, c.positional...) {
		if v != "" && !validName.MatchString(v) {
			return fmt.Sprintf("%q is not a valid name", v)
		}
	}
	return ""
}

// release decodes only what a verdict needs: the same Secret holds the chart and
// the values, which can carry passwords. manifestObject skips rendered Secret data too.
type release struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status string `json:"status"`
	} `json:"info"`
	Hooks []struct {
		Events []string `json:"events"`
	} `json:"hooks"`
	// stored is the record as kept in the Secret; the manifest is decoded from it
	// only for the revisions a verdict compares.
	stored []byte
}

func (r release) manifest() ([]manifestObject, error) {
	raw, err := unpackRelease(r.stored)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Manifest string `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, errRelease
	}
	return manifestObjects(doc.Manifest)
}

// releases returns every stored revision of a release, oldest first. Helm reads
// every Secret the labels select, so one it would read but whatdies cannot is ask.
func (p *prober) releases(ctx context.Context, name string) ([]release, error) {
	out, err := p.fetch(ctx, append([]string{"secret", "-l", "owner=helm,name=" + name}, p.nsArgs()...)...)
	if err != nil {
		return nil, err
	}
	var secrets struct {
		Items []struct {
			Type string `json:"type"`
			Data struct {
				Release []byte `json:"release"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &secrets); err != nil {
		return nil, fmt.Errorf("decode kubectl output: %w", err)
	}
	var rels []release
	for _, s := range secrets.Items {
		raw, err := unpackRelease(s.Data.Release)
		if err != nil || s.Type != "helm.sh/release.v1" {
			return nil, errRelease
		}
		var r release
		if err := json.Unmarshal(raw, &r); err != nil || r.Version <= 0 || r.Name != name {
			return nil, errRelease
		}
		r.stored = s.Data.Release
		rels = append(rels, r)
	}
	slices.SortFunc(rels, func(a, b release) int { return cmp.Compare(a.Version, b.Version) })
	return rels, nil
}

var errRelease = errors.New("a release record is not in the form helm writes")

func unpackRelease(stored []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(string(stored))
	if err != nil {
		return nil, errRelease
	}
	if bytes.HasPrefix(raw, []byte{0x1f, 0x8b, 0x08}) {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, errRelease
		}
		raw, err = io.ReadAll(io.LimitReader(zr, maxRelease+1))
		if err != nil || len(raw) > maxRelease {
			return nil, errRelease
		}
	}
	return raw, nil
}

type manifestObject struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string `yaml:"name"`
		Namespace   string `yaml:"namespace"`
		Annotations struct {
			Policy string `yaml:"helm.sh/resource-policy"`
		} `yaml:"annotations"`
	} `yaml:"metadata"`
	// Any object with items is a list to helm, which acts on the items instead.
	Items []manifestObject `yaml:"items"`
}

func (o manifestObject) key(releaseNS string) string {
	ns := o.Metadata.Namespace
	if ns == "" {
		ns = releaseNS
	}
	return o.APIVersion + "/" + o.Kind + "/" + ns + "/" + o.Metadata.Name
}

// kept reads the policy the way helm does: trimmed and in any case.
func (o manifestObject) kept() bool {
	return strings.EqualFold(strings.TrimSpace(o.Metadata.Annotations.Policy), "keep")
}

func manifestObjects(manifest string) ([]manifestObject, error) {
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	var out []manifestObject
	for {
		var o manifestObject
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, errors.New("its manifest could not be read")
		}
		if o.Kind == "" && o.Metadata.Name == "" && len(o.Items) == 0 {
			continue
		}
		if out, err = flatten(o, 0, out); err != nil {
			return nil, err
		}
	}
}

func flatten(o manifestObject, depth int, out []manifestObject) ([]manifestObject, error) {
	if len(o.Items) == 0 {
		if o.Kind == "" || o.Metadata.Name == "" {
			return nil, errors.New("its manifest has an object without a kind or a name")
		}
		if !validName.MatchString(o.Kind) {
			return nil, errors.New("its manifest has an object kind that is not valid")
		}
		return append(out, o), nil
	}
	if depth >= maxManifestDepth {
		return nil, errors.New("its manifest nests lists too deeply")
	}
	for _, item := range o.Items {
		var err error
		if out, err = flatten(item, depth+1, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// heldKinds are judged by looking them up, since what they hold can outlive the
// release or go with it.
var heldKinds = map[string]string{
	"v1/PersistentVolumeClaim": "pvc", "v1/PersistentVolume": "pv", "v1/Namespace": "namespace",
	"apps/v1/StatefulSet": "sts", "apiextensions.k8s.io/v1/CustomResourceDefinition": "crd",
}

func (p *prober) judgeUninstall(ctx context.Context, name string, c helmCommand) verdict.Verdict {
	where := fmt.Sprintf("release %s on %s", name, p.contextLabel(ctx))
	rels, err := p.releases(ctx, name)
	switch {
	case err != nil:
		return verdict.AskBecause(fmt.Sprintf("uninstalls %s; could not read it: %v", where, err))
	case len(rels) == 0:
		return verdict.AskBecause("uninstalls " + where + "; no release record was found, so what it owns is unknown")
	}
	cur := rels[len(rels)-1]
	if cur.Info.Status == "uninstalled" {
		return verdict.AskBecause("uninstalls " + where + ", which is already uninstalled with its history kept")
	}
	objects, err := cur.manifest()
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("uninstalls %s; %v", where, err))
	}
	summary := fmt.Sprintf("uninstalls %s at revision %d", where, cur.Version)
	// Uninstall reads keep from the manifest, as helm does.
	return p.judgeRemoved(ctx, summary, cur, objects, c, true, "pre-delete", "post-delete")
}

func (p *prober) judgeRollback(ctx context.Context, name string, revision int, c helmCommand) verdict.Verdict {
	where := fmt.Sprintf("release %s on %s", name, p.contextLabel(ctx))
	rels, err := p.releases(ctx, name)
	switch {
	case err != nil:
		return verdict.AskBecause(fmt.Sprintf("rolls back %s; could not read it: %v", where, err))
	case len(rels) == 0:
		return verdict.AskBecause("rolls back " + where + "; no release record was found, so what it changes is unknown")
	}
	cur := rels[len(rels)-1]
	if revision == 0 {
		revision = cur.Version - 1
	}
	i := slices.IndexFunc(rels, func(r release) bool { return r.Version == revision })
	if i < 0 {
		return verdict.AskBecause(fmt.Sprintf("rolls back %s to revision %d, which was not found", where, revision))
	}
	now, err := cur.manifest()
	if err == nil {
		var then []manifestObject
		if then, err = rels[i].manifest(); err == nil {
			keep := map[string]bool{}
			for _, o := range then {
				keep[o.key(rels[i].Namespace)] = true
			}
			now = slices.DeleteFunc(now, func(o manifestObject) bool { return keep[o.key(cur.Namespace)] })
		}
	}
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("rolls back %s; %v", where, err))
	}
	summary := fmt.Sprintf("rolls back %s from revision %d to %d", where, cur.Version, revision)
	// A rollback reads keep from the live object, which the manifest may not match.
	out := p.judgeRemoved(ctx, summary, rels[i], now, c, false, "pre-rollback", "post-rollback")
	if c.on["--force"] {
		out = verdict.Join(out, verdict.AskBecause("--force replaces objects instead of patching them, overwriting changes made outside helm"))
	}
	return out
}

// judgeRemoved is ask at least, listing what goes, and looks up the objects that
// can take data with them. hooksFrom is the revision whose hooks run and whose
// namespace unqualified objects get.
func (p *prober) judgeRemoved(ctx context.Context, summary string, hooksFrom release, objects []manifestObject, c helmCommand, manifestKeep bool, events ...string) verdict.Verdict {
	counts := map[string]int{}
	var kept, held []manifestObject
	for _, o := range objects {
		switch {
		case manifestKeep && o.kept():
			kept = append(kept, o)
		case heldKinds[o.APIVersion+"/"+o.Kind] != "":
			held = append(held, o)
			counts[o.Kind]++
		default:
			counts[o.Kind]++
		}
	}
	lines := []string{summary + ", which removes " + countKinds(counts)}
	for _, o := range kept {
		lines = append(lines, fmt.Sprintf("keeps %s %s, marked helm.sh/resource-policy: keep", o.Kind, o.Metadata.Name))
	}
	if !c.on["--no-hooks"] {
		if n := countHooks(hooksFrom, events...); n > 0 {
			lines = append(lines, fmt.Sprintf("runs %d %s hook(s), which run commands the manifest does not show", n, strings.Join(events, " or ")))
		}
	}
	// Looked up separately so the summary leads even when an object is the deny.
	var found verdict.Verdict
	sts := command{flags: map[string]string{"--cascade": c.value("--cascade")}}
	for i, o := range held {
		if i == maxTargets {
			found = verdict.Join(found, verdict.AskBecause(fmt.Sprintf("and %d more objects that may hold data, not looked up", len(held)-maxTargets)))
			break
		}
		ns := o.Metadata.Namespace
		if ns == "" {
			ns = hooksFrom.Namespace
		}
		if !validName.MatchString(o.Metadata.Name) || (ns != "" && !validName.MatchString(ns)) {
			found = verdict.Join(found, verdict.AskBecause(fmt.Sprintf("%s has a name that is not valid, so it was not looked up", o.Kind)))
			continue
		}
		q := *p
		q.namespace = ns
		found = verdict.Join(found, q.judge(ctx, heldKinds[o.APIVersion+"/"+o.Kind], o.Metadata.Name, sts))
	}
	lines = append(lines, found.Reasons...)
	if found.Decision == verdict.Deny {
		return verdict.DenyBecause(found.Advice, lines...)
	}
	return verdict.AskBecause(lines...)
}

func countKinds(counts map[string]int) string {
	if len(counts) == 0 {
		return "no objects"
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

func countHooks(r release, events ...string) int {
	n := 0
	for _, h := range r.Hooks {
		if slices.ContainsFunc(h.Events, func(e string) bool { return slices.Contains(events, e) }) {
			n++
		}
	}
	return n
}

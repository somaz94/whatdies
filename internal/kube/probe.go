package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/verdict"
)

// prober only reads the cluster: kubectl get, plus config current-context for the
// label. A server-side dry run of the delete was rejected: it needs delete rights,
// shows no cascade, and a rewritten delete is one flag away from a real one.
type prober struct {
	run       runner.Runner
	context   string
	namespace string
	label     string
}

type object struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name       string   `json:"name"`
		Namespace  string   `json:"namespace"`
		Finalizers []string `json:"finalizers"`
	} `json:"metadata"`
	Spec struct {
		VolumeName string `json:"volumeName"`
		Reclaim    string `json:"persistentVolumeReclaimPolicy"`
		Replicas   *int   `json:"replicas"`
		Capacity   struct {
			Storage string `json:"storage"`
		} `json:"capacity"`
		Retention struct {
			WhenDeleted string `json:"whenDeleted"`
		} `json:"persistentVolumeClaimRetentionPolicy"`
		Templates []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"volumeClaimTemplates"`
		Group string `json:"group"`
		Names struct {
			Plural string `json:"plural"`
		} `json:"names"`
	} `json:"spec"`
}

type list struct {
	Items []object `json:"items"`
}

// fetch returns kubectl get's raw JSON for callers that decode their own shape (helm Secrets).
func (p *prober) fetch(ctx context.Context, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+6)
	if p.context != "" {
		full = append(full, "--context", p.context)
	}
	full = append(full, "get")
	full = append(full, args...)
	full = append(full, "-o", "json", requestTimeout)
	return p.run.Run(ctx, runner.Options{}, "kubectl", full...)
}

func (p *prober) get(ctx context.Context, args ...string) (list, error) {
	out, err := p.fetch(ctx, args...)
	if err != nil {
		return list{}, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return list{}, nil
	}
	// kubectl get returns a List for several names and a bare object for one.
	var l list
	if err := json.Unmarshal(out, &l); err != nil {
		return list{}, fmt.Errorf("decode kubectl output: %w", err)
	}
	if l.Items == nil {
		var one object
		if err := json.Unmarshal(out, &one); err != nil {
			return list{}, fmt.Errorf("decode kubectl output: %w", err)
		}
		if one.Metadata.Name != "" {
			l.Items = []object{one}
		}
	}
	return l, nil
}

// orMissing names the case where kubectl worked and returned nothing, which would
// otherwise print as <nil>.
func orMissing(err error) error {
	if err == nil {
		return errors.New("no such object")
	}
	return err
}

func (p *prober) contextLabel(ctx context.Context) string {
	if p.label != "" {
		return p.label
	}
	p.label = p.context
	if p.label == "" {
		out, err := p.run.Run(ctx, runner.Options{}, "kubectl", "config", "current-context")
		p.label = strings.TrimSpace(string(out))
		if err != nil || p.label == "" {
			p.label = "the current context"
		}
	}
	return p.label
}

func (p *prober) nsArgs() []string {
	if p.namespace == "" {
		return nil
	}
	return []string{"-n", p.namespace}
}

func (p *prober) judge(ctx context.Context, kind, name string, c command) verdict.Verdict {
	switch kind {
	case "namespace":
		return p.judgeNamespace(ctx, name)
	case "pvc":
		return p.judgePVC(ctx, name)
	case "pv":
		return p.judgePV(ctx, name)
	case "sts":
		return p.judgeStatefulSet(ctx, name, c)
	case "crd":
		return p.judgeCRD(ctx, name)
	}
	return verdict.AskBecause(fmt.Sprintf("deletes %s %s", kind, name))
}

func (p *prober) judgeNamespace(ctx context.Context, ns string) verdict.Verdict {
	where := fmt.Sprintf("namespace %s on %s", ns, p.contextLabel(ctx))
	objects, err := p.get(ctx, "pvc,statefulset,deployment,pod", "-n", ns)
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not list its contents: %v", where, err))
	}
	counts := map[string]int{}
	var claims []object
	for _, o := range objects.Items {
		counts[o.Kind]++
		if o.Kind == "PersistentVolumeClaim" {
			claims = append(claims, o)
		}
	}
	contents := fmt.Sprintf("%d pod(s), %d deployment(s), %d statefulset(s), %d PVC(s)",
		counts["Pod"], counts["Deployment"], counts["StatefulSet"], counts["PersistentVolumeClaim"])
	return p.judgeClaims(ctx, "deletes "+where+" with "+contents, claims)
}

// named adds the namespace when the command or a release set one; otherwise it is
// the context's default, which a lookup does not learn.
func (p *prober) named(kind, name string) string {
	if p.namespace == "" {
		return kind + " " + name
	}
	return fmt.Sprintf("%s %s in namespace %s", kind, name, p.namespace)
}

func (p *prober) judgePVC(ctx context.Context, name string) verdict.Verdict {
	where := fmt.Sprintf("%s on %s", p.named("PVC", name), p.contextLabel(ctx))
	objects, err := p.get(ctx, append([]string{"pvc", name}, p.nsArgs()...)...)
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not read it: %v", where, err))
	}
	return p.judgeClaims(ctx, "deletes "+where, objects.Items)
}

// judgeClaims decides from the reclaim policy of the volumes behind claims.
func (p *prober) judgeClaims(ctx context.Context, summary string, claims []object) verdict.Verdict {
	byVolume := map[string]string{}
	var volumes []string
	for _, c := range claims {
		if c.Spec.VolumeName != "" {
			if !validName.MatchString(c.Spec.VolumeName) {
				return verdict.AskBecause(summary + "; a volume has a name that is not valid, so it was not looked up")
			}
			volumes = append(volumes, c.Spec.VolumeName)
			byVolume[c.Spec.VolumeName] = c.Metadata.Name
		}
	}
	if len(volumes) == 0 {
		return verdict.AskBecause(summary + "; no persistent volumes are bound")
	}
	pvs, err := p.get(ctx, append([]string{"pv"}, volumes...)...)
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("%s; could not read the reclaim policy of its volumes: %v", summary, err))
	}
	var gone, kept, unknown []string
	for _, pv := range pvs.Items {
		line := fmt.Sprintf("PVC %s -> PV %s", byVolume[pv.Metadata.Name], pv.Metadata.Name)
		if s := pv.Spec.Capacity.Storage; s != "" {
			line += " (" + s + ")"
		}
		switch pv.Spec.Reclaim {
		case "Retain":
			kept = append(kept, line+" reclaim=Retain")
		case "Delete", "Recycle":
			gone = append(gone, line+" reclaim="+pv.Spec.Reclaim+": the volume and its data are deleted")
		default:
			unknown = append(unknown, line+" has an unknown reclaim policy")
		}
	}
	if len(gone) > 0 {
		return verdict.DenyBecause(verdict.StopAdvice, append(append(append([]string{summary}, gone...), kept...), unknown...)...)
	}
	if len(pvs.Items) < len(volumes) || len(unknown) > 0 {
		return verdict.AskBecause(append(append([]string{summary + "; not every volume could be read"}, unknown...), kept...)...)
	}
	return verdict.AskBecause(append([]string{summary + "; its volumes have reclaim=Retain, so they outlive it"}, kept...)...)
}

func (p *prober) judgePV(ctx context.Context, name string) verdict.Verdict {
	where := fmt.Sprintf("PV %s on %s", name, p.contextLabel(ctx))
	objects, err := p.get(ctx, "pv", name)
	if err != nil || len(objects.Items) == 0 {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not read its reclaim policy: %v", where, orMissing(err)))
	}
	switch policy := objects.Items[0].Spec.Reclaim; policy {
	case "Delete", "Recycle":
		return verdict.DenyBecause(verdict.StopAdvice, fmt.Sprintf("deletes %s with reclaim=%s: the volume and its data are deleted", where, policy))
	case "Retain":
		return verdict.AskBecause(fmt.Sprintf("deletes %s with reclaim=Retain", where))
	}
	return verdict.AskBecause(fmt.Sprintf("deletes %s, whose reclaim policy is unknown", where))
}

// judgeStatefulSet separates the two meanings of deleting one: by default the
// claims outlive it, so it is not data loss unless the set says otherwise.
func (p *prober) judgeStatefulSet(ctx context.Context, name string, c command) verdict.Verdict {
	where := fmt.Sprintf("%s on %s", p.named("statefulset", name), p.contextLabel(ctx))
	if c.value("--cascade") == "orphan" {
		return verdict.AskBecause("deletes " + where + " but orphans its pods")
	}
	objects, err := p.get(ctx, append([]string{"statefulset", name}, p.nsArgs()...)...)
	if err != nil || len(objects.Items) == 0 {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not read it: %v", where, orMissing(err)))
	}
	sts := objects.Items[0]
	if sts.Spec.Retention.WhenDeleted != "Delete" {
		return verdict.AskBecause("deletes " + where + "; its PVCs are kept, so the data stays")
	}
	replicas := 1
	if sts.Spec.Replicas != nil {
		replicas = *sts.Spec.Replicas
	}
	var names []string
	for _, tpl := range sts.Spec.Templates {
		for i := 0; i < replicas && len(names) < maxTargets; i++ {
			claim := fmt.Sprintf("%s-%s-%d", tpl.Metadata.Name, sts.Metadata.Name, i)
			if !validName.MatchString(claim) {
				return verdict.AskBecause("deletes " + where + "; a claim has a name that is not valid")
			}
			names = append(names, claim)
		}
	}
	summary := "deletes " + where + " with whenDeleted=Delete, which also removes its PVCs"
	if len(names) == 0 {
		return verdict.AskBecause(summary)
	}
	args := append(append([]string{"pvc"}, names...), "--ignore-not-found")
	claims, err := p.get(ctx, append(args, p.nsArgs()...)...)
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("%s; could not read them: %v", summary, err))
	}
	return p.judgeClaims(ctx, summary, claims.Items)
}

func (p *prober) judgeCRD(ctx context.Context, name string) verdict.Verdict {
	where := fmt.Sprintf("CRD %s on %s, which removes every instance of it", name, p.contextLabel(ctx))
	objects, err := p.get(ctx, "crd", name)
	if err != nil || len(objects.Items) == 0 {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not read it: %v", where, orMissing(err)))
	}
	crd := objects.Items[0]
	if !validName.MatchString(crd.Spec.Names.Plural) || !validName.MatchString(crd.Spec.Group) {
		return verdict.AskBecause("deletes " + where)
	}
	instances, err := p.get(ctx, crd.Spec.Names.Plural+"."+crd.Spec.Group, "-A")
	if err != nil {
		return verdict.AskBecause(fmt.Sprintf("deletes %s; could not count them: %v", where, err))
	}
	return verdict.AskBecause(fmt.Sprintf("deletes %s (%d found)", where, len(instances.Items)))
}

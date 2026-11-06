package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

type fakeRunner struct {
	replies map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, _ runner.Options, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	key = strings.TrimSuffix(key, " -o json "+requestTimeout)
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if key == "config current-context" {
		if r, ok := f.replies[key]; ok {
			return []byte(r), nil
		}
		return []byte("kind-test\n"), nil
	}
	r, ok := f.replies[key]
	if !ok {
		return nil, errors.New("unexpected call: " + key)
	}
	return []byte(r), nil
}

func eval(t *testing.T, cmd string, f *fakeRunner) verdict.Verdict {
	t.Helper()
	a := shell.Analyze(cmd, "/w")
	if a.ParseError != nil {
		t.Fatal(a.ParseError)
	}
	return Evaluate(context.Background(), a.Invocations[len(a.Invocations)-1], Deps{Runner: f})
}

func reasons(v verdict.Verdict) string { return strings.Join(v.Reasons, " | ") }

func TestReadsAndOrdinaryWritesDecideNothing(t *testing.T) {
	for _, cmd := range []string{
		`kubectl`, `kubectl get pods`, `kubectl -n x get pods`, `kubectl -nx get pods`, `kubectl --namespace=x get pods`,
		`kubectl --context c get pods`, `kubectl describe pod x`, `kubectl logs -f x`, `kubectl top nodes`, `kubectl config view`,
		`kubectl auth can-i delete pods`, `kubectl diff -f x.yaml`, `kubectl apply -f x.yaml`, `cat x | kubectl apply -f -`,
		`kubectl create ns x`, `kubectl rollout restart deploy/x`, `kubectl label pod x a=b`, `kubectl scale deploy x --replicas=3`,
		`kubectl replace -f x.yaml`, `kubectl patch deploy x -p '{"spec":{"replicas":2}}'`,
		`kubectl delete ns x --dry-run=client`, `kubectl delete ns x --dry-run=server`, `kubectl delete -f x.yaml --dry-run=client -o yaml`,
	} {
		f := &fakeRunner{}
		if v := eval(t, cmd, f); v.Decision != verdict.None {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: probed %v", cmd, f.calls)
		}
	}
}

func TestDangerousVerbsAskWithoutProbing(t *testing.T) {
	cases := map[string]string{
		`kubectl drain node-1`:                                         "drain",
		`kubectl taint node n a=b:NoExecute`:                           "taint",
		`kubectl exec x -- rm -rf /data`:                               "exec",
		`kubectl debug x`:                                              "debug",
		`kubectl replace --force -f x.yaml`:                            "replace --force",
		`kubectl apply --prune -l a=b -f x.yaml`:                       "prune",
		`kubectl apply --force -f x.yaml`:                              "--force",
		`kubectl scale deploy x --replicas=0`:                          "zero",
		`kubectl scale deploy x --replicas 0`:                          "zero",
		`kubectl scale deploy x --replicas=$N`:                         "zero",
		`kubectl patch pvc x -p '{"metadata":{"finalizers":null}}'`:    "finalizers",
		`kubectl patch pvc x --patch='{"metadata":{"finalizers":[]}}'`: "finalizers",
		`kubectl patch pvc x -p "$P"`:                                  "finalizers",
		`kubectl neat x`:                                               "not a verb",
		`kubectl $VERB x`:                                              "computed",
		`kubectl --unknown=1 delete ns x`:                              "unknown option",
		`kubectl delete pod $POD`:                                      "computed",
		`xargs kubectl delete pod`:                                     "computed",
		`kubectl delete --raw /api/v1/namespaces/x`:                    "--raw",
		`kubectl delete -f x.yaml`:                                     "-f",
		`kubectl delete --filename=x.yaml`:                             "-f",
		`kubectl delete -f https://example.test/x.yaml`:                "-f",
		`kubectl delete -k overlays/prod`:                              "-k",
		`kubectl delete pods -l app=x`:                                 "selector",
		`kubectl delete pods --field-selector=a=b`:                     "selector",
		`kubectl delete pods --all`:                                    "--all",
		`kubectl delete pods --all -A`:                                 "--all",
		`kubectl delete all --all`:                                     "--all",
		`kubectl delete all x`:                                         "most resources",
		`kubectl delete pvc,pv x`:                                      "several",
		`kubectl delete`:                                               "without a named target",
		`kubectl delete pod`:                                           "no object name",
		`kubectl delete pod a b c d e f g h i j k`:                     "too many",
	}
	for cmd, want := range cases {
		f := &fakeRunner{}
		v := eval(t, cmd, f)
		if v.Decision != verdict.Ask {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
			continue
		}
		if !strings.Contains(reasons(v), want) {
			t.Errorf("%s: %s, want %q", cmd, reasons(v), want)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: probed %v", cmd, f.calls)
		}
	}
}

func TestChosenTargetsAreNeverProbed(t *testing.T) {
	cases := map[string]string{
		`kubectl --kubeconfig=/tmp/k delete ns x`:                   "--kubeconfig",
		`kubectl delete ns x --kubeconfig /tmp/k`:                   "--kubeconfig",
		`kubectl --server=https://x delete ns x`:                    "--server",
		`kubectl -s https://x delete ns x`:                          "-s",
		`kubectl --token=t delete ns x`:                             "--token",
		`kubectl --user=u delete ns x`:                              "--user",
		`KUBECONFIG=/tmp/k kubectl delete ns x`:                     "KUBECONFIG",
		`export KUBECONFIG=/tmp/k; kubectl delete ns x`:             "KUBECONFIG",
		`PATH=/tmp kubectl delete ns x`:                             "PATH",
		`HOME=/tmp kubectl delete ns x`:                             "HOME",
		`./kubectl delete ns x`:                                     "plain kubectl on PATH",
		`kubectl config use-context prod && kubectl delete ns x`:    "changes the target",
		`aws eks update-kubeconfig --name x && kubectl delete ns x`: "changes the target",
	}
	for cmd, want := range cases {
		f := &fakeRunner{}
		v := eval(t, cmd, f)
		if v.Decision != verdict.Ask {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
			continue
		}
		if !strings.Contains(reasons(v), want) || !strings.Contains(reasons(v), "deletes namespace x") {
			t.Errorf("%s: %s", cmd, reasons(v))
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: probed %v", cmd, f.calls)
		}
	}
}

func TestNoProbeFromTheCaller(t *testing.T) {
	a := shell.Analyze(`oc delete ns x`, "/w")
	f := &fakeRunner{}
	v := Evaluate(context.Background(), a.Invocations[0], Deps{Runner: f, NoProbe: "oc is not supported"})
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "oc is not supported") || len(f.calls) != 0 {
		t.Fatalf("%v %s %v", v.Decision, reasons(v), f.calls)
	}
}

const shop = `{"kind":"List","items":[
 {"kind":"PersistentVolumeClaim","metadata":{"name":"data"},"spec":{"volumeName":"pv-1"}},
 {"kind":"Pod","metadata":{"name":"db"},"spec":{}},
 {"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"replicas":1}}]}`

func pv(name, reclaim string) string {
	return `{"kind":"PersistentVolume","metadata":{"name":"` + name + `"},"spec":{"persistentVolumeReclaimPolicy":"` + reclaim + `","capacity":{"storage":"1Gi"}}}`
}

// The same command, opposite verdicts: this is what a text match cannot do.
func TestNamespaceVerdictFollowsLiveState(t *testing.T) {
	lookup := "get pvc,statefulset,deployment,pod -n shop"
	f := &fakeRunner{replies: map[string]string{lookup: shop, "get pv pv-1": pv("pv-1", "Delete")}}
	v := eval(t, `kubectl delete ns shop`, f)
	if v.Decision != verdict.Deny || v.Advice != verdict.StopAdvice {
		t.Fatalf("Delete: %v %s", v.Decision, reasons(v))
	}
	for _, want := range []string{"namespace shop on kind-test", "1 pod(s)", "1 PVC(s)", "PVC data -> PV pv-1 (1Gi)", "reclaim=Delete"} {
		if !strings.Contains(reasons(v), want) {
			t.Errorf("reasons %q missing %q", reasons(v), want)
		}
	}

	f = &fakeRunner{replies: map[string]string{lookup: shop, "get pv pv-1": pv("pv-1", "Retain")}}
	if v := eval(t, `kubectl delete ns shop`, f); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "outlive") {
		t.Fatalf("Retain: %v %s", v.Decision, reasons(v))
	}
}

func TestNamespaceForms(t *testing.T) {
	lookup := "get pvc,statefulset,deployment,pod -n x"
	empty := `{"kind":"List","items":[]}`
	for _, cmd := range []string{`kubectl delete ns x`, `kubectl delete namespace x`, `kubectl delete namespaces x`, `kubectl delete ns/x`, `k delete NS x`, `kubectl -n other delete ns x`} {
		f := &fakeRunner{replies: map[string]string{lookup: empty}}
		v := eval(t, cmd, f)
		if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "no persistent volumes") {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
		}
	}
}

func TestContextIsPassedAndNamed(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"--context prod get pvc,statefulset,deployment,pod -n x": `{"items":[]}`}}
	v := eval(t, `kubectl --context prod delete ns x`, f)
	if !strings.Contains(reasons(v), "on prod") {
		t.Fatalf("%s", reasons(v))
	}
	for _, c := range f.calls {
		if c == "config current-context" {
			t.Fatal("no lookup is needed when the command names the context")
		}
	}
}

func TestFailedLookupsAreAsk(t *testing.T) {
	boom := errors.New("Unauthorized")
	lookup := "get pvc,statefulset,deployment,pod -n x"
	cases := []struct {
		name string
		cmd  string
		f    *fakeRunner
		want string
	}{
		{"namespace", `kubectl delete ns x`, &fakeRunner{errs: map[string]error{lookup: boom}}, "could not list"},
		{"bad json", `kubectl delete ns x`, &fakeRunner{replies: map[string]string{lookup: `nope`}}, "could not list"},
		{"pv forbidden", `kubectl delete ns x`, &fakeRunner{
			replies: map[string]string{lookup: `{"items":[{"kind":"PersistentVolumeClaim","metadata":{"name":"d"},"spec":{"volumeName":"v"}}]}`},
			errs:    map[string]error{"get pv v": boom}}, "could not read the reclaim"},
		{"pvc", `kubectl delete pvc d`, &fakeRunner{errs: map[string]error{"get pvc d": boom}}, "could not read it"},
		{"pv", `kubectl delete pv v`, &fakeRunner{errs: map[string]error{"get pv v": boom}}, "could not read its reclaim"},
		{"sts", `kubectl delete sts s`, &fakeRunner{errs: map[string]error{"get statefulset s": boom}}, "could not read it"},
		{"crd", `kubectl delete crd c`, &fakeRunner{errs: map[string]error{"get crd c": boom}}, "could not read it"},
	}
	for _, c := range cases {
		v := eval(t, c.cmd, c.f)
		if v.Decision != verdict.Ask || !strings.Contains(reasons(v), c.want) {
			t.Errorf("%s: %v %s", c.name, v.Decision, reasons(v))
		}
	}
}

func TestContextLookupFailureStillWorks(t *testing.T) {
	f := &fakeRunner{
		errs:    map[string]error{"config current-context": errors.New("none")},
		replies: map[string]string{"get pvc,statefulset,deployment,pod -n x": `{"items":[]}`},
	}
	if v := eval(t, `kubectl delete ns x`, f); !strings.Contains(reasons(v), "the current context") {
		t.Fatalf("%s", reasons(v))
	}
}

func TestPVC(t *testing.T) {
	claim := `{"kind":"PersistentVolumeClaim","metadata":{"name":"d"},"spec":{"volumeName":"v"}}`
	f := &fakeRunner{replies: map[string]string{"get pvc d -n x": claim, "get pv v": pv("v", "Delete")}}
	if v := eval(t, `kubectl delete pvc d -n x`, f); v.Decision != verdict.Deny {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get pvc d": claim, "get pv v": pv("v", "Retain")}}
	if v := eval(t, `kubectl delete persistentvolumeclaim/d`, f); v.Decision != verdict.Ask {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get pvc d": claim, "get pv v": pv("v", "Recycle")}}
	if v := eval(t, `kubectl delete pvc d`, f); v.Decision != verdict.Deny {
		t.Fatalf("Recycle: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get pvc d": claim, "get pv v": pv("v", "Other")}}
	if v := eval(t, `kubectl delete pvc d`, f); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "unknown reclaim") {
		t.Fatalf("Other: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get pvc d": `{"kind":"PersistentVolumeClaim","metadata":{"name":"d"},"spec":{}}`}}
	if v := eval(t, `kubectl delete pvc d`, f); v.Decision != verdict.Ask {
		t.Fatalf("pending: %v %s", v.Decision, reasons(v))
	}
}

func TestSeveralClaimsWhereSomeVolumesAreMissing(t *testing.T) {
	two := `{"items":[
	 {"kind":"PersistentVolumeClaim","metadata":{"name":"a"},"spec":{"volumeName":"va"}},
	 {"kind":"PersistentVolumeClaim","metadata":{"name":"b"},"spec":{"volumeName":"vb"}}]}`
	lookup := "get pvc,statefulset,deployment,pod -n x"
	f := &fakeRunner{replies: map[string]string{lookup: two, "get pv va vb": pv("va", "Retain")}}
	if v := eval(t, `kubectl delete ns x`, f); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "not every volume") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{lookup: two, "get pv va vb": `{"items":[` + pv("va", "Retain") + `,` + pv("vb", "Delete") + `]}`}}
	if v := eval(t, `kubectl delete ns x`, f); v.Decision != verdict.Deny || !strings.Contains(reasons(v), "reclaim=Retain") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
}

func TestPV(t *testing.T) {
	cases := map[string]verdict.Decision{"Delete": verdict.Deny, "Recycle": verdict.Deny, "Retain": verdict.Ask, "": verdict.Ask}
	for policy, want := range cases {
		f := &fakeRunner{replies: map[string]string{"get pv v": pv("v", policy)}}
		if v := eval(t, `kubectl delete pv v`, f); v.Decision != want {
			t.Errorf("%q: %v %s", policy, v.Decision, reasons(v))
		}
	}
}

func TestStatefulSetKeepsItsClaimsByDefault(t *testing.T) {
	sts := `{"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"replicas":2,"volumeClaimTemplates":[{"metadata":{"name":"data"}}]}}`
	f := &fakeRunner{replies: map[string]string{"get statefulset db": sts}}
	v := eval(t, `kubectl delete sts db`, f)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "PVCs are kept") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
}

func TestStatefulSetThatDeletesItsClaims(t *testing.T) {
	sts := `{"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"replicas":2,
	 "persistentVolumeClaimRetentionPolicy":{"whenDeleted":"Delete"},
	 "volumeClaimTemplates":[{"metadata":{"name":"data"}}]}}`
	claims := `{"items":[{"kind":"PersistentVolumeClaim","metadata":{"name":"data-db-0"},"spec":{"volumeName":"v0"}}]}`
	f := &fakeRunner{replies: map[string]string{
		"get statefulset db -n x":                             sts,
		"get pvc data-db-0 data-db-1 --ignore-not-found -n x": claims,
		"get pv v0": pv("v0", "Delete"),
	}}
	if v := eval(t, `kubectl delete statefulset db -n x`, f); v.Decision != verdict.Deny {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f.errs = map[string]error{"get pvc data-db-0 data-db-1 --ignore-not-found -n x": errors.New("forbidden")}
	if v := eval(t, `kubectl delete statefulset db -n x`, f); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "could not read them") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{
		"get statefulset db":                `{"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"persistentVolumeClaimRetentionPolicy":{"whenDeleted":"Delete"},"volumeClaimTemplates":[{"metadata":{"name":"d"}}]}}`,
		"get pvc d-db-0 --ignore-not-found": "",
	}}
	if v := eval(t, `kubectl delete sts db`, f); v.Decision != verdict.Ask {
		t.Fatalf("no claims left: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get statefulset db": `{"kind":"StatefulSet","metadata":{"name":"db"},"spec":{"persistentVolumeClaimRetentionPolicy":{"whenDeleted":"Delete"}}}`}}
	if v := eval(t, `kubectl delete sts db`, f); v.Decision != verdict.Ask {
		t.Fatalf("no templates: %v %s", v.Decision, reasons(v))
	}
}

func TestStatefulSetOrphan(t *testing.T) {
	f := &fakeRunner{}
	v := eval(t, `kubectl delete sts db --cascade=orphan`, f)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "orphans") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
}

func TestCRDCountsItsInstances(t *testing.T) {
	crd := `{"kind":"CustomResourceDefinition","metadata":{"name":"widgets.example.test"},"spec":{"group":"example.test","names":{"plural":"widgets"}}}`
	f := &fakeRunner{replies: map[string]string{
		"get crd widgets.example.test": crd,
		"get widgets.example.test -A":  `{"items":[{"kind":"Widget","metadata":{"name":"a"}},{"kind":"Widget","metadata":{"name":"b"}}]}`,
	}}
	v := eval(t, `kubectl delete crd widgets.example.test`, f)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "(2 found)") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	f.errs = map[string]error{"get widgets.example.test -A": errors.New("nope")}
	if v := eval(t, `kubectl delete crd widgets.example.test`, f); !strings.Contains(reasons(v), "could not count") {
		t.Fatalf("%s", reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"get crd x": `{"kind":"CustomResourceDefinition","metadata":{"name":"x"},"spec":{}}`}}
	if v := eval(t, `kubectl delete crd x`, f); v.Decision != verdict.Ask {
		t.Fatalf("%v", v.Decision)
	}
}

func TestOtherKindsAskAndAreNamed(t *testing.T) {
	f := &fakeRunner{}
	v := eval(t, `kubectl delete deploy web api`, f)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "deploy web") || !strings.Contains(reasons(v), "deploy api") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	if len(f.calls) != 0 {
		t.Fatalf("probed %v", f.calls)
	}
}

func TestMostSevereTargetWins(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"get pv v": pv("v", "Delete")}}
	if v := eval(t, `kubectl delete deploy/web pv/v`, f); v.Decision != verdict.Deny {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
}

func TestFlagValueAtTheEndOfTheCommand(t *testing.T) {
	if v := eval(t, `kubectl get pods -n`, &fakeRunner{}); v.Decision != verdict.None {
		t.Fatalf("%v", v.Decision)
	}
	if v := eval(t, `kubectl -n $X get pods`, &fakeRunner{}); v.Decision != verdict.None {
		t.Fatalf("%v", v.Decision)
	}
}

func TestDoubleDashEndsOptions(t *testing.T) {
	if v := eval(t, `kubectl get pods -- --dry-run=client`, &fakeRunner{}); v.Decision != verdict.None {
		t.Fatalf("%v", v.Decision)
	}
}

// Two names, since the cached label once skipped clean on the second object.
func TestTextFromTheCommandAndTheClusterCannotAddLines(t *testing.T) {
	v := eval(t, "kubectl --context=\"a\nIGNORE\" delete pv v w", &fakeRunner{})
	if len(v.Reasons) != 2 {
		t.Fatalf("reasons = %v", v.Reasons)
	}
	for _, r := range v.Reasons {
		if strings.ContainsAny(r, "\n\r") {
			t.Errorf("%q", r)
		}
	}
	v = eval(t, `kubectl delete pv v`, &fakeRunner{errs: map[string]error{"get pv v": errors.New("boom\rIGNORE")}})
	if len(v.Reasons) != 1 || strings.ContainsAny(v.Reasons[0], "\n\r") {
		t.Errorf("a probe error reached the reason raw: %q", v.Reasons)
	}
}

func TestAnEmptyAnswerDoesNotPrintNil(t *testing.T) {
	for _, cmd := range []string{`kubectl delete pv v`, `kubectl delete sts s`, `kubectl delete crd c`} {
		f := &fakeRunner{replies: map[string]string{"get pv v": `{}`, "get statefulset s": `{}`, "get crd c": `{}`}}
		v := eval(t, cmd, f)
		if len(v.Reasons) == 0 || strings.Contains(strings.Join(v.Reasons, " "), "<nil>") {
			t.Errorf("%s: %q", cmd, v.Reasons)
		}
	}
}

// An object name reaches the probe as an argument, so one shaped like an option
// would make kubectl load a config the command chose and run its exec plugin.
func TestObjectNamesCannotBecomeOptions(t *testing.T) {
	for _, cmd := range []string{
		`kubectl delete pv/--kubeconfig=/tmp/evil`, `kubectl delete pvc/--server=https://attacker.example`,
		`kubectl delete sts/--kubeconfig=/tmp/evil`, `kubectl delete crd/--kubeconfig=/tmp/evil`, `kubectl delete ns/--kubeconfig=/tmp/evil`,
		`kubectl delete pv/`, `kubectl delete pv "a b"`, `kubectl delete pv 'x;y'`,
		`kubectl --context=-x delete pv v`, `kubectl -n -x delete pvc v`,
	} {
		f := &fakeRunner{}
		v := eval(t, cmd, f)
		if v.Decision != verdict.Ask {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: probed %v", cmd, f.calls)
		}
	}
}

func TestNamesFromTheClusterAreCheckedBeforeUse(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{
		"get pvc d": `{"kind":"PersistentVolumeClaim","metadata":{"name":"d"},"spec":{"volumeName":"--kubeconfig=/tmp/evil"}}`,
	}}
	if v := eval(t, `kubectl delete pvc d`, f); v.Decision != verdict.Ask {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	for _, c := range f.calls {
		if strings.Contains(c, "evil") {
			t.Fatalf("probed %q", c)
		}
	}
	f = &fakeRunner{replies: map[string]string{
		"get crd c": `{"kind":"CustomResourceDefinition","metadata":{"name":"c"},"spec":{"group":"--kubeconfig=/tmp/evil","names":{"plural":"x"}}}`,
	}}
	if v := eval(t, `kubectl delete crd c`, f); v.Decision != verdict.Ask {
		t.Fatalf("%v", v.Decision)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "evil") {
			t.Fatalf("probed %q", c)
		}
	}
}

func TestLookupLinesNameTheNamespaceTheCommandChose(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{
		"get pvc data -n shop": `{"kind":"PersistentVolumeClaim","metadata":{"name":"data"},"spec":{}}`,
		"get pvc data":         `{"kind":"PersistentVolumeClaim","metadata":{"name":"data"},"spec":{}}`,
	}}
	if v := eval(t, `kubectl delete pvc data -n shop`, f); !strings.Contains(reasons(v), "PVC data in namespace shop on kind-test") {
		t.Errorf("with -n: %s", reasons(v))
	}
	if v := eval(t, `kubectl delete pvc data`, f); !strings.Contains(reasons(v), "deletes PVC data on kind-test") {
		t.Errorf("without -n: %s", reasons(v))
	}
}

package kube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

const releaseQuery = "get secret -l owner=helm,name=shop -n shop"

// The fixture is the Secrets real helm wrote for a synthetic chart, three
// revisions: a bound PVC data, a PVC kept by resource-policy, a Secret holding
// fixture-secret-value, a pre-delete hook, and a ConfigMap only in revisions 1 and 3.
func shopFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/helm-shop-secrets.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func boundPVC(reclaim string) map[string]string {
	return map[string]string{
		"get pvc data -n shop": `{"kind":"PersistentVolumeClaim","metadata":{"name":"data","namespace":"shop"},"spec":{"volumeName":"pv1"}}`,
		"get pv pv1":           `{"kind":"PersistentVolume","metadata":{"name":"pv1"},"spec":{"persistentVolumeReclaimPolicy":"` + reclaim + `","capacity":{"storage":"1Gi"}}}`,
	}
}

func evalHelm(t *testing.T, cmd string, f *fakeRunner, env map[string]string) verdict.Verdict {
	t.Helper()
	a := shell.Analyze(cmd, "/w")
	if a.ParseError != nil {
		t.Fatal(a.ParseError)
	}
	inv := a.Invocations[len(a.Invocations)-1]
	verb := "uninstall"
	if strings.Contains(cmd, "rollback") {
		verb = "rollback"
	}
	return EvaluateHelm(context.Background(), inv, verb, Deps{Runner: f, Getenv: func(k string) string { return env[k] }})
}

func with(replies map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range []map[string]string{replies, extra} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestUninstallIsDecidedByWhatTheReleaseOwns(t *testing.T) {
	f := &fakeRunner{replies: with(boundPVC("Delete"), map[string]string{releaseQuery: shopFixture(t)})}
	v := evalHelm(t, `helm uninstall shop -n shop`, f, nil)
	if v.Decision != verdict.Deny || v.Advice != verdict.StopAdvice {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	for _, want := range []string{
		"uninstalls release shop on kind-test at revision 3, which removes 2 ConfigMap, 1 PersistentVolumeClaim, 1 Pod, 1 Secret",
		"keeps PersistentVolumeClaim kept, marked helm.sh/resource-policy: keep",
		"runs 1 pre-delete or post-delete hook(s)",
		"reclaim=Delete: the volume and its data are deleted",
	} {
		if !strings.Contains(reasons(v), want) {
			t.Errorf("missing %q in %s", want, reasons(v))
		}
	}
	if !strings.HasPrefix(v.Reasons[0], "uninstalls release shop") {
		t.Errorf("the summary does not lead: %q", v.Reasons[0])
	}
	if strings.Contains(v.Message(), "fixture-secret-value") {
		t.Fatalf("a value from the release reached the message: %s", v.Message())
	}

	f = &fakeRunner{replies: with(boundPVC("Retain"), map[string]string{releaseQuery: shopFixture(t)})}
	if v := evalHelm(t, `helm uninstall shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "reclaim=Retain") {
		t.Fatalf("retain: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: with(boundPVC("Retain"), map[string]string{releaseQuery: shopFixture(t)})}
	if v := evalHelm(t, `helm uninstall shop -n shop --no-hooks --wait --timeout 5m`, f, nil); strings.Contains(reasons(v), "hook") {
		t.Fatalf("--no-hooks: %s", reasons(v))
	}
}

func TestRollbackJudgesWhatTheTargetRevisionLacks(t *testing.T) {
	cases := map[string]struct {
		cmd  string
		want string
	}{
		"previous":  {`helm rollback shop -n shop`, "from revision 3 to 2, which removes 1 ConfigMap, 1 Pod"},
		"named":     {`helm rollback shop 1 -n shop`, "from revision 3 to 1, which removes 1 Pod"},
		"same":      {`helm rollback shop 3 -n shop`, "which removes no objects"},
		"missing":   {`helm rollback shop 9 -n shop`, "revision 9, which was not found"},
		"forced":    {`helm rollback shop 2 -n shop --force`, "--force replaces objects"},
		"not a num": {`helm rollback shop two -n shop`, "not a number"},
		"too many":  {`helm rollback shop 1 2 -n shop`, "more arguments"},
	}
	for name, c := range cases {
		f := &fakeRunner{replies: map[string]string{releaseQuery: shopFixture(t)}}
		v := evalHelm(t, c.cmd, f, nil)
		if v.Decision != verdict.Ask || !strings.Contains(reasons(v), c.want) {
			t.Errorf("%s: %v %s", name, v.Decision, reasons(v))
		}
	}
}

func TestHelmTargetsAreNotLookedUp(t *testing.T) {
	cases := map[string]struct {
		cmd string
		env map[string]string
	}{
		"kubeconfig":       {`helm uninstall shop --kubeconfig x`, nil},
		"token":            {`helm uninstall shop --kube-token=x`, nil},
		"insecure":         {`helm uninstall shop --kube-insecure-skip-tls-verify`, nil},
		"inline namespace": {`HELM_NAMESPACE=x helm uninstall shop`, nil},
		"inline driver":    {`HELM_DRIVER=secret helm uninstall shop`, nil},
		"inline config":    {`KUBECONFIG=x helm uninstall shop`, nil},
		"driver":           {`helm uninstall shop`, map[string]string{"HELM_DRIVER": "configmap"}},
		"ambient ns":       {`helm uninstall shop`, map[string]string{"HELM_NAMESPACE": "other"}},
		"ambient context":  {`helm uninstall shop`, map[string]string{"HELM_KUBECONTEXT": "other"}},
		"not on PATH":      {`./helm uninstall shop`, nil},
		"earlier step":     {`kubectl config use-context x && helm uninstall shop`, nil},
		"option context":   {`helm uninstall shop --kube-context=-x`, nil},
		"many releases":    {`helm uninstall a b c d e f g h i j k`, nil},
		"bad name":         {`helm uninstall 'shop;x'`, nil},
	}
	for name, c := range cases {
		f := &fakeRunner{}
		v := evalHelm(t, c.cmd, f, c.env)
		if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "not looked up") || len(f.calls) != 0 {
			t.Errorf("%s: %v %s calls=%v", name, v.Decision, reasons(v), f.calls)
		}
	}
	f := &fakeRunner{replies: with(boundPVC("Delete"), map[string]string{releaseQuery: shopFixture(t)})}
	if v := evalHelm(t, `helm uninstall shop -n shop`, f, map[string]string{"HELM_DRIVER": "secrets"}); v.Decision != verdict.Deny {
		t.Errorf("HELM_DRIVER=secrets is the default storage: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"--context c " + releaseQuery: shopFixture(t), "--context c get pvc data -n shop": `{}`}}
	if v := evalHelm(t, `helm uninstall shop -nshop --kube-context=c`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "on c at revision 3") {
		t.Errorf("context: %v %s %v", v.Decision, reasons(v), f.calls)
	}
}

func TestHelmCommandsThatCannotBeRead(t *testing.T) {
	for cmd, want := range map[string]string{
		`helm uninstall`:                 "names no release",
		`helm uninstall "$R"`:            "computed",
		`helm uninstall shop --new-flag`: "not one whatdies knows",
		`helm uninstall shop --timeout`:  "missing",
		`helm uninstall shop -n "$NS"`:   "missing or computed",
	} {
		f := &fakeRunner{}
		if v := evalHelm(t, cmd, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), want) || len(f.calls) != 0 {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
		}
	}
}

// storedRelease encodes a release the way helm stores it: JSON, gzipped, base64,
// and base64 again as Secret data.
func storedRelease(t *testing.T, r map[string]any, compress bool) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if compress {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		raw = buf.Bytes()
	}
	return base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(raw)))
}

func secretList(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func secretItem(typ, data string) string {
	return fmt.Sprintf(`{"type":%q,"data":{"release":%q}}`, typ, data)
}

func rel(version int, status, manifest string) map[string]any {
	return map[string]any{"name": "shop", "namespace": "shop", "version": version, "info": map[string]any{"status": status}, "manifest": manifest, "config": map[string]any{"password": "hunter2"}}
}

func TestReleaseRecordsThatCannotBeRead(t *testing.T) {
	garbageGzip := base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString([]byte{0x1f, 0x8b, 0x08, 0, 0})))
	cases := map[string]struct {
		reply string
		want  string
	}{
		"no records":      {secretList(), "no release record"},
		"other type":      {secretList(secretItem("Opaque", storedRelease(t, rel(1, "deployed", ""), true))), "not in the form helm writes"},
		"other release":   {secretList(secretItem("helm.sh/release.v1", storedRelease(t, map[string]any{"name": "x", "version": 1}, true))), "not in the form helm writes"},
		"not a document":  {secretList(secretItem("helm.sh/release.v1", base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString([]byte("[")))))), "not in the form helm writes"},
		"not base64":      {secretList(secretItem("helm.sh/release.v1", base64.StdEncoding.EncodeToString([]byte("!!")))), "not in the form helm writes"},
		"bad gzip":        {secretList(secretItem("helm.sh/release.v1", garbageGzip)), "not in the form helm writes"},
		"no version":      {secretList(secretItem("helm.sh/release.v1", storedRelease(t, map[string]any{"name": "shop"}, false))), "not in the form helm writes"},
		"not json":        {"nope", "decode kubectl output"},
		"bad manifest":    {secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", "kind: [x"), true))), "could not be read"},
		"nameless object": {secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", "kind: ConfigMap\n"), true))), "without a kind or a name"},
		"uninstalled":     {secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "uninstalled", ""), true))), "already uninstalled"},
	}
	for name, c := range cases {
		f := &fakeRunner{replies: map[string]string{releaseQuery: c.reply}}
		v := evalHelm(t, `helm uninstall shop -n shop`, f, nil)
		if v.Decision != verdict.Ask || !strings.Contains(reasons(v), c.want) {
			t.Errorf("%s: %v %s", name, v.Decision, reasons(v))
		}
		if strings.Contains(v.Message(), "hunter2") {
			t.Errorf("%s: leaked a value", name)
		}
	}
	f := &fakeRunner{errs: map[string]error{releaseQuery: errors.New("secrets is forbidden")}}
	if v := evalHelm(t, `helm uninstall shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "forbidden") {
		t.Errorf("forbidden: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{errs: map[string]error{releaseQuery: errors.New("forbidden")}}
	if v := evalHelm(t, `helm rollback shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "could not read") {
		t.Errorf("rollback forbidden: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{releaseQuery: secretList()}}
	if v := evalHelm(t, `helm rollback shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "no release record") {
		t.Errorf("rollback none: %v %s", v.Decision, reasons(v))
	}
	for name, manifests := range map[string][2]string{"now": {"kind: [x", "a: b"}, "then": {"a: b", "kind: [x"}} {
		reply := secretList(
			secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "superseded", manifests[1]), true)),
			secretItem("helm.sh/release.v1", storedRelease(t, rel(2, "deployed", manifests[0]), true)),
		)
		f := &fakeRunner{replies: map[string]string{releaseQuery: reply}}
		if v := evalHelm(t, `helm rollback shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "could not be read") {
			t.Errorf("rollback manifest %s: %v %s", name, v.Decision, reasons(v))
		}
	}
}

func TestReleaseObjectsAreLookedUpByKindWithALimit(t *testing.T) {
	var docs []string
	for i := range maxTargets + 2 {
		docs = append(docs, fmt.Sprintf("apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: c%d\n", i))
	}
	docs = append(docs,
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: owned\n",
		"apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: --kubeconfig=x\n",
		"apiVersion: example.com/v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: lookalike\n",
		"# only a comment\n",
	)
	reply := secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", strings.Join(docs, "---\n")), false)))
	replies := map[string]string{releaseQuery: reply}
	for i := range maxTargets {
		replies[fmt.Sprintf("get pvc c%d -n shop", i)] = `{}`
	}
	f := &fakeRunner{replies: replies}
	v := evalHelm(t, `helm uninstall shop -n shop`, f, nil)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "and 4 more objects that may hold data") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	if strings.Contains(strings.Join(f.calls, " "), "kubeconfig") || strings.Contains(strings.Join(f.calls, " "), "lookalike") {
		t.Fatalf("looked up something it must not: %v", f.calls)
	}

	reply = secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", docs[maxTargets+2]+"---\n"+docs[maxTargets+3]), true)))
	f = &fakeRunner{replies: map[string]string{releaseQuery: reply, "get pvc,statefulset,deployment,pod -n owned": `{"items":[]}`}}
	v = evalHelm(t, `helm uninstall shop -n shop`, f, nil)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "deletes namespace owned") || !strings.Contains(reasons(v), "has a name that is not valid") {
		t.Fatalf("%v %s %v", v.Decision, reasons(v), f.calls)
	}
}

func TestHelmLookupsFollowTheProcessEnvironmentAndNoProbe(t *testing.T) {
	inv := shell.Analyze(`helm uninstall shop`, "/w").Invocations[0]
	t.Setenv("HELM_DRIVER", "sql")
	f := &fakeRunner{}
	if v := EvaluateHelm(context.Background(), inv, "uninstall", Deps{Runner: f}); !strings.Contains(reasons(v), "HELM_DRIVER is set") || len(f.calls) != 0 {
		t.Errorf("os environment: %s %v", reasons(v), f.calls)
	}
	if v := EvaluateHelm(context.Background(), inv, "uninstall", Deps{Runner: f, NoProbe: "lookups are off"}); !strings.Contains(reasons(v), "lookups are off") || len(f.calls) != 0 {
		t.Errorf("NoProbe: %s %v", reasons(v), f.calls)
	}
}

func TestHelmFlagsAreReadAsHelmReadsThem(t *testing.T) {
	for cmd, want := range map[string]verdict.Decision{
		`helm uninstall shop --dry-run`:                 verdict.None,
		`helm uninstall shop --dry-run=true`:            verdict.None,
		`helm uninstall shop --dry-run --dry-run=false`: verdict.Ask,
		`helm uninstall shop --dry-run=false --dry-run`: verdict.None,
		`helm uninstall shop --description --dry-run`:   verdict.Ask,
		`helm uninstall shop -- --dry-run`:              verdict.Ask,
		`helm uninstall shop --dry-run=maybe`:           verdict.Ask,
		`helm rollback shop 1 --dry-run --dry-run=0`:    verdict.Ask,
	} {
		f := &fakeRunner{}
		if v := evalHelm(t, cmd, f, map[string]string{"HELM_NAMESPACE": "x"}); v.Decision != want {
			t.Errorf("%s: %v %s", cmd, v.Decision, reasons(v))
		}
	}
	f := &fakeRunner{replies: map[string]string{releaseQuery: shopFixture(t)}}
	if v := evalHelm(t, `helm uninstall shop -n shop --no-hooks=false --wait=true`, f, nil); !strings.Contains(reasons(v), "hook") {
		t.Errorf("--no-hooks=false still runs hooks: %s", reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{releaseQuery: shopFixture(t)}}
	if v := evalHelm(t, `helm rollback shop -n shop --force=false`, f, nil); strings.Contains(reasons(v), "--force") {
		t.Errorf("--force=false: %s", reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{"--context arn:aws:eks:us-east-1:111122223333:cluster/prod " + releaseQuery: secretList()}}
	if v := evalHelm(t, `helm uninstall shop -n shop --kube-context arn:aws:eks:us-east-1:111122223333:cluster/prod`, f, nil); !strings.Contains(reasons(v), "no release record") {
		t.Errorf("an ARN context is looked up: %s %v", reasons(v), f.calls)
	}
}

func TestListsInTheManifestAreFlattenedAsHelmDoes(t *testing.T) {
	pvc := "apiVersion: v1\nkind: List\nmetadata:\n  name: bundle\nitems:\n  - apiVersion: v1\n    kind: PersistentVolumeClaim\n    metadata:\n      name: data\n"
	hidden := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: looks-harmless\nitems:\n  - apiVersion: v1\n    kind: ConfigMap\n    metadata:\n      name: hidden-item\n"
	reply := secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", pvc+"---\n"+hidden), true)))
	f := &fakeRunner{replies: with(boundPVC("Delete"), map[string]string{releaseQuery: reply})}
	v := evalHelm(t, `helm uninstall shop -n shop`, f, nil)
	if v.Decision != verdict.Deny || !strings.Contains(reasons(v), "removes 1 ConfigMap, 1 PersistentVolumeClaim") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	deep := "kind: List\nmetadata:\n  name: a\nitems:\n- kind: List\n  items:\n  - kind: List\n    items:\n    - kind: List\n      items:\n      - kind: ConfigMap\n        metadata:\n          name: x\n"
	for manifest, want := range map[string]string{
		deep: "nests lists too deeply",
		"kind: List\nitems:\n- kind: ConfigMap\n":    "without a kind or a name",
		"kind: 'Config Map'\nmetadata:\n  name: x\n": "kind that is not valid",
	} {
		reply := secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", manifest), true)))
		f := &fakeRunner{replies: map[string]string{releaseQuery: reply}}
		if v := evalHelm(t, `helm uninstall shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), want) {
			t.Errorf("%q: %v %s", want, v.Decision, reasons(v))
		}
	}
}

// helm checks keep on the live object during a rollback, so the manifest's word for
// it is not enough to skip a lookup.
func TestRollbackLooksUpObjectsTheManifestMarksKept(t *testing.T) {
	kept := "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data\n  annotations:\n    helm.sh/resource-policy: ' Keep '\n"
	reply := secretList(
		secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "superseded", ""), true)),
		secretItem("helm.sh/release.v1", storedRelease(t, rel(2, "deployed", kept), true)),
	)
	f := &fakeRunner{replies: with(boundPVC("Delete"), map[string]string{releaseQuery: reply})}
	if v := evalHelm(t, `helm rollback shop -n shop`, f, nil); v.Decision != verdict.Deny {
		t.Fatalf("rollback: %v %s", v.Decision, reasons(v))
	}
	f = &fakeRunner{replies: map[string]string{releaseQuery: reply}}
	if v := evalHelm(t, `helm uninstall shop -n shop`, f, nil); v.Decision != verdict.Ask || !strings.Contains(reasons(v), "keeps PersistentVolumeClaim data") {
		t.Fatalf("uninstall: %v %s", v.Decision, reasons(v))
	}
}

func TestRollbackReadsOnlyTheRevisionsItCompares(t *testing.T) {
	hook := map[string]any{"events": []string{"pre-rollback"}}
	target := rel(1, "superseded", "")
	target["hooks"] = []any{hook}
	reply := secretList(
		secretItem("helm.sh/release.v1", storedRelease(t, target, true)),
		secretItem("helm.sh/release.v1", storedRelease(t, rel(2, "superseded", "kind: [broken"), true)),
		secretItem("helm.sh/release.v1", storedRelease(t, rel(3, "deployed", ""), true)),
	)
	f := &fakeRunner{replies: map[string]string{releaseQuery: reply}}
	v := evalHelm(t, `helm rollback shop 1 -n shop`, f, nil)
	if v.Decision != verdict.Ask || !strings.Contains(reasons(v), "runs 1 pre-rollback or post-rollback hook(s)") {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
}

func TestUninstallOfSeveralReleasesKeepsTheStrictest(t *testing.T) {
	other := secretList(secretItem("helm.sh/release.v1", storedRelease(t, map[string]any{"name": "a", "namespace": "shop", "version": 1, "manifest": ""}, true)))
	f := &fakeRunner{replies: with(boundPVC("Delete"), map[string]string{
		"get secret -l owner=helm,name=a -n shop": other,
		releaseQuery: shopFixture(t),
	})}
	if v := evalHelm(t, `helm uninstall a shop -n shop`, f, nil); v.Decision != verdict.Deny {
		t.Fatalf("%v %s", v.Decision, reasons(v))
	}
	sts := "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: db\n"
	reply := secretList(secretItem("helm.sh/release.v1", storedRelease(t, rel(1, "deployed", sts), true)))
	f = &fakeRunner{replies: map[string]string{releaseQuery: reply}}
	if v := evalHelm(t, `helm uninstall shop -n shop --cascade orphan`, f, nil); !strings.Contains(reasons(v), "orphans its pods") {
		t.Fatalf("cascade: %v %s", v.Decision, reasons(v))
	}
}

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/verdict"
)

type fakeRunner struct {
	replies map[string]string
	calls   int
}

func (f *fakeRunner) Run(_ context.Context, _ runner.Options, _ string, args ...string) ([]byte, error) {
	f.calls++
	key := strings.TrimSuffix(strings.Join(args, " "), " -o json --request-timeout=3s")
	if r, ok := f.replies[key]; ok {
		return []byte(r), nil
	}
	return nil, errors.New("unavailable")
}

func decide(t *testing.T, cmd, dir string, f *fakeRunner) verdict.Verdict {
	t.Helper()
	if f == nil {
		f = &fakeRunner{}
	}
	return Evaluate(context.Background(), cmd, dir, Deps{Runner: f})
}

// A false no-decision here means a destructive command passes unnoticed, so every
// spelling that reached a review of this design is listed.
func TestEverySpellingOfADestructiveCommandIsCaught(t *testing.T) {
	for _, cmd := range []string{
		`terraform destroy`, `\terraform destroy`, `"terraform" destroy`, `/usr/bin/terraform destroy`, `tf destroy`,
		`command terraform destroy`, `env A=1 terraform destroy`, `sudo terraform destroy`, `time terraform destroy`,
		`timeout 60 terraform destroy`, `nohup terraform destroy`, `yes | terraform destroy`, `bash -c 'terraform destroy'`,
		`sh -c "cd x && terraform destroy"`, `eval 'terraform destroy'`, `(terraform destroy)`, `{ terraform destroy; }`,
		`true && terraform destroy`, `false || terraform destroy`, `x=$(terraform destroy)`, `echo $(terraform destroy)`,
		`if true; then terraform destroy; fi`, `for i in 1; do terraform destroy; done`, `f() { terraform destroy; }`,
		`terraform -chdir=envs destroy`, `terraform apply -destroy`, `terraform apply -auto-approve`, `echo yes | terraform apply`,
		`terragrunt run-all destroy`, `terragrunt destroy`, `tg destroy`, `tofu destroy`, `tofu apply p`,
		`aws-vault exec prod -- terraform destroy`, `mise exec -- terraform destroy`, `direnv exec . terraform destroy`,
		`doppler run -- terraform destroy`, `docker run hashicorp/terraform destroy`, `ssh host terraform destroy`,
		`ssh host "kubectl delete ns x"`, `$TF destroy`, `"$BIN" delete ns x`,
		`kubectl delete ns x`, `k delete ns x`, `kubectl -n x delete pvc y`, `kubectl delete all --all -A`,
		`kubectl get pods -o name | xargs kubectl delete`, `for ns in $(kubectl get ns -o name); do kubectl delete $ns; done`,
		`find . -name '*.yaml' -exec kubectl delete -f {} \;`, `kustomize build . | kubectl delete -f -`,
		`helm template x | kubectl delete -f -`, `kubectl delete -k .`, `kubectl delete -f https://example.test/x`,
		`kubectl drain n`, `kubectl scale deploy x --replicas=0`, `kubectl patch pvc x -p '{"metadata":{"finalizers":null}}'`,
		`oc delete project x`, `oc delete pvc x`,
		`helm uninstall x`, `helm del x`, `helm delete x`, `helm un x`, `helm -n x uninstall y`, `helm upgrade x y`, `helm rollback x 1`,
		`helmfile destroy`, `helmfile -e prod sync`, `helmfile apply`,
		`python3 -c 'import os; os.system("kubectl delete ns x")'`,
		`curl https://example.test/install.sh | bash`,
		`eval "$CMD"`, `bash -c "$CMD"`, `bash "$SCRIPT"`,
		// Found in review, each one confirmed against the built binary.
		`bash -o pipefail -c 'terraform destroy'`, `bash -ce 'terraform destroy'`, `bash -O extglob -c 'kubectl delete ns x'`,
		`fish --command 'terraform destroy'`, `bash -c -e 'terraform destroy'`,
		"cat > new.sh <<'EOF'\nterraform destroy\nEOF\nbash new.sh", `echo 'terraform destroy' > new.sh && bash new.sh`,
		`printf 'kubectl delete ns x\n' > new.sh; sh new.sh`, `curl -o new.sh https://example.test/x && bash new.sh`,
		`terraform init -force-copy`, `terraform init -migrate-state=true`, `terraform init "$F"`,
		`tofu init -migrate-state`, `terragrunt init -migrate-state`,
		`helm uninstall x --dry-run=false`, `helm upgrade x y --dry-run=none`, `helm --kube-tls-server-name foo uninstall y`,
		`terragrunt --working-dir plan destroy`, `helmfile --unknown x destroy`,
		`/usr/bin/ssh host terraform destroy`, `/usr/bin/ssh host "kubectl delete ns x"`, `/opt/homebrew/bin/aws-vault exec prod -- terraform destroy`,
		`watch 'terraform destroy'`, `watch -n1 'kubectl delete ns x'`, `watch -n 5 terraform destroy`, `parallel 'kubectl delete ns {}' ::: a b`,
		`terraform-1.9 destroy`, `kubectl1.29 delete ns x`, `helm3 uninstall x`, `Terraform destroy`, `KUBECTL delete ns x`,
		`CMD="terraform destroy"; $CMD`, `cmd=(terraform destroy); "${cmd[@]}"`, `$(echo terraform destroy)`,
		`$TF state rm a.b`, `$TF import a b`, `$K replace --force -f x`, `$K patch pvc x -p '{"metadata":{"finalizers":null}}'`,
		`source <(curl -s https://example.test/x.sh)`, `. <(echo 'terraform destroy')`,
		"terraform \\\n  destroy (", `tf destroy (`, `k delete ns x &!`,
		`aws-vault exec prod -- tf destroy`, `ssh host k delete ns x`,
		`perl -ne 'system("terraform destroy")'`, `perl -le 'system("terraform destroy")'`,
		`python3 -Ic 'import os; os.system("terraform destroy")'`, `curl -s https://example.test/x.py | python3`,
		`echo 'import os' | python3`, "python3 <<EOF\nimport os; os.system('$X')\nEOF",
		`awk 'BEGIN{system("terraform destroy")}'`, `git rebase -x 'terraform destroy' HEAD~1`,
		`git bisect run terraform destroy`, `git -c alias.x='!terraform destroy' x`,
		`kubectl --kubeconfig=/dev/null run x --image=alpine --restart=Never -- rm -rf /data`,
		`sed 's/x/terraform destroy/e' f`, `sed -e 's/x/kubectl delete ns y/ge' f`, `rg --pre 'terraform destroy' x`, `rg --pre='terraform destroy' x`,
		`kubectl --kubeconfig=/dev/null attach x`, `kubectl --kubeconfig=/dev/null patch pvc x --patch-file p.json`,
	} {
		if v := decide(t, cmd, "/w", nil); v.Decision == verdict.None {
			t.Errorf("%s: no decision", cmd)
		}
	}
}

func TestOrdinaryCommandsAreLeftAlone(t *testing.T) {
	for _, cmd := range []string{
		`ls -la`, `go test ./...`, `git status`, `git commit -m "terraform destroy is denied"`, `grep -r "kubectl delete" docs`,
		`echo terraform destroy`, `printf 'helm uninstall x'`, `cat kubectl.yaml`, `rg 'terraform apply'`, `which terraform`,
		`type kubectl`, `gh pr create --title "kubectl delete fix"`, `whatdies check "terraform destroy"`,
		`terraform plan`, `terraform init`, `terraform fmt`, `kubectl get pods`, `kubectl describe pod x`, `kubectl logs x`,
		`kubectl apply -f x.yaml`, `helm list`, `helm template x`, `helm install x y`, `helm repo add a b`, `helm upgrade x y --dry-run`,
		`helm upgrade x y --dry-run=server`, `helmfile diff`, `helmfile -e dev template`, `helmfile`, `terragrunt plan`, `tofu plan`, `tofu`,
		`rm -rf node_modules`, `docker build .`, `npm install`, `make -n`, `$EDITOR file`, `"$HOME/bin/tool" --help`,
		`python3 -c 'print(1)'`, `source .venv/bin/activate`, `bash scripts/absent.sh`, `./absent.sh`,
		`sed 's/terraform apply/x/' f`, `sed -i 's/kubectl delete/kubectl get/g' f`, `sed -n '/helm upgrade/p' f`, `rg --hidden 'kubectl delete'`,
		`command -v terraform`, `git status && git diff`, `git rebase main`, `git bisect start`, `git -c user.name=x commit -m "kubectl delete"`,
		`source "$HOME/.nvm/nvm.sh"`, `chmod +x x.sh`, `kubectl --kubeconfig=/dev/null run x --image=alpine`,
		`$HOME/bin/tool --help`, `awk '{print $1}' file`, `watch kubectl get pods`, `python3 - <<EOF
print(1)
EOF`,
	} {
		v := decide(t, cmd, "/w", nil)
		if v.Decision != verdict.None {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
}

func TestPathsHidingInScriptsAndMakefiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "alias.sh", "tf destroy\n")
	write(t, dir, "multi.sh", "terraform \\\n  destroy\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub"), "Makefile", "nuke:\n\tterraform destroy\n")
	write(t, dir, "Other.mk", "go:\n\tterraform destroy\n")
	write(t, dir, "vars.mk", "TF := terraform\nnuke:\n\t$(TF) destroy\n")
	write(t, dir, "inline.mk", "nuke: ; terraform destroy\n")
	write(t, dir, "cont.mk", "nuke:\n\tterraform \\\n\t  destroy\n")
	write(t, dir, "goal.mk", ".DEFAULT_GOAL := nuke\ntest:\n\tgo test\nnuke:\n\tterraform destroy\n")
	for _, cmd := range []string{
		`bash alias.sh`, `./multi.sh`, `make -Csub nuke`, `make --directory=sub nuke`, `make -fOther.mk go`, `make --file=Other.mk go`,
		`make -f vars.mk nuke`, `make -f inline.mk nuke`, `make -f cont.mk nuke`, `make -f goal.mk`,
	} {
		if v := decide(t, cmd, dir, nil); v.Decision != verdict.Ask {
			t.Errorf("%s: %v", cmd, v.Decision)
		}
	}
}

func TestScriptsWrittenInTheSameLineAreJudgedByWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{
		"cat > ok.sh <<'EOF'\ngo test\nEOF\nbash ok.sh", `echo 'go test' > ok.sh && bash ok.sh`,
		`chmod +x real.sh && ./real.sh`, `cat real.sh && bash real.sh`,
	} {
		write(t, dir, "real.sh", "go test\n")
		if v := decide(t, cmd, dir, nil); v.Decision != verdict.None {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
}

func TestPlanDirectoryAmbiguity(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p", "plan")
	if err := os.Mkdir(filepath.Join(dir, "safe"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "safe"), "p", "plan")
	fatal := `{"format_version":"1.2","resource_changes":[{"address":"aws_db_instance.m","mode":"managed","type":"aws_db_instance","change":{"actions":["delete"]}}]}`
	for _, cmd := range []string{
		`false && cd safe; terraform apply p`, `if false; then cd safe; fi; terraform apply p`, `f() { cd safe; }; terraform apply p`,
		`false || cd safe; terraform apply p`, `for x in 1; do cd safe; done; terraform apply p`, `case a in a) cd safe;; esac; terraform apply p`,
		`env --chdir=safe terraform apply p`, `env -Csafe terraform apply p`, `sudo -D safe terraform apply p`,
	} {
		f := &fakeRunner{replies: map[string]string{"show -no-color -json p": fatal}}
		if v := decide(t, cmd, dir, f); v.Decision == verdict.None {
			t.Errorf("%s: no decision", cmd)
		}
		if f.calls != 0 {
			t.Errorf("%s: read a plan from a directory it cannot be sure of", cmd)
		}
	}
	for _, cmd := range []string{`cd safe && terraform apply p`, `true && cd safe && terraform apply p`, `cd safe || exit 1; terraform apply p`, `cd a && cd ../safe && terraform apply p`} {
		f := &fakeRunner{replies: map[string]string{"show -no-color -json p": fatal}}
		if v := decide(t, cmd, dir, f); v.Decision != verdict.Deny {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
}

func TestPlanFileOverwrittenEarlierInTheLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "q", "plan")
	for _, cmd := range []string{
		`cat fatal.json > q && terraform apply q`, `dd if=fatal.json of=q && terraform apply q`,
		`curl --output=q https://example.test/x && terraform apply q`, `terragrunt plan -out=q && terraform apply q`,
	} {
		f := &fakeRunner{}
		if v := decide(t, cmd, dir, f); v.Decision != verdict.Deny || f.calls != 0 {
			t.Errorf("%s: %v calls=%d", cmd, v.Decision, f.calls)
		}
	}
	for _, cmd := range []string{`ls q && terraform apply q`, `terraform show q && terraform apply q`} {
		f := &fakeRunner{}
		if v := decide(t, cmd, dir, f); v.Decision == verdict.Deny {
			t.Errorf("%s: a read does not change the plan: %v", cmd, v.Reasons)
		}
	}
}

func TestUnparseableCommands(t *testing.T) {
	if v := decide(t, `terraform destroy (`, "/w", nil); v.Decision != verdict.Ask {
		t.Errorf("mentions a tool: %v", v.Decision)
	}
	if v := decide(t, `echo (`, "/w", nil); v.Decision != verdict.None {
		t.Errorf("mentions nothing: %v", v.Decision)
	}
}

func TestTheStrictestStepWins(t *testing.T) {
	v := decide(t, `helm uninstall x; terraform destroy; ls`, "/w", nil)
	if v.Decision != verdict.Deny || len(v.Reasons) != 2 {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestHelmAndFriendsWithComputedVerbs(t *testing.T) {
	for _, cmd := range []string{`helm $V x`, `tofu $V`, `helmfile $V`, `terragrunt $V`} {
		if v := decide(t, cmd, "/w", nil); v.Decision != verdict.Ask {
			t.Errorf("%s: %v", cmd, v.Decision)
		}
	}
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScriptsAreReadOneLevelDeep(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "deploy.sh", "#!/bin/sh\nterraform destroy\n")
	write(t, dir, "ok.sh", "#!/bin/sh\ngo test\n")
	write(t, dir, "run.py", "import subprocess\nsubprocess.run(['kubectl', 'delete', 'ns', 'x'])\n")
	hits := []string{`./deploy.sh`, `bash deploy.sh`, `sh < deploy.sh`, `source deploy.sh`, `. deploy.sh`, `python3 run.py`, `cd . && ./deploy.sh`}
	for _, cmd := range hits {
		if v := decide(t, cmd, dir, nil); v.Decision != verdict.Ask {
			t.Errorf("%s: %v", cmd, v.Decision)
		}
	}
	if v := decide(t, `./ok.sh`, dir, nil); v.Decision != verdict.None {
		t.Errorf("ok.sh: %v", v.Decision)
	}
	if v := decide(t, filepath.Join(dir, "deploy.sh"), "/elsewhere", nil); v.Decision != verdict.Ask {
		t.Errorf("absolute path: %v", v.Decision)
	}
	if v := decide(t, `cd "$X" && ./deploy.sh`, dir, nil); v.Decision != verdict.None {
		t.Errorf("computed dir: %v", v.Decision)
	}
}

func TestInlineCodeThatIsComputed(t *testing.T) {
	if v := decide(t, `python3 -c "$CODE"`, "/w", nil); v.Decision != verdict.Ask {
		t.Fatalf("%v", v.Decision)
	}
}

func TestMakeTargets(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", "test:\n\tgo test\n\nnuke:\n\tterraform destroy\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub"), "Other.mk", "go:\n\tkubectl delete ns x\n")
	cases := map[string]verdict.Decision{
		`make`:                                 verdict.None,
		`make test`:                            verdict.None,
		`make nuke`:                            verdict.Ask,
		`make -j2 nuke`:                        verdict.Ask,
		`make FOO=1 nuke`:                      verdict.Ask,
		`gmake nuke`:                           verdict.Ask,
		`make -C sub -f Other.mk go`:           verdict.Ask,
		`make --directory sub --file Other.mk`: verdict.Ask,
		`make -f $F`:                           verdict.None,
		`make $TARGET`:                         verdict.None,
	}
	for cmd, want := range cases {
		if v := decide(t, cmd, dir, nil); v.Decision != want {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
	if v := decide(t, `cd "$X" && make nuke`, dir, nil); v.Decision != verdict.None {
		t.Errorf("computed dir: %v", v.Decision)
	}
	if v := decide(t, `make -C `+filepath.Join(dir, "sub")+` -f Other.mk`, "/elsewhere", nil); v.Decision != verdict.Ask {
		t.Errorf("absolute -C: %v", v.Decision)
	}
}

func TestExtraStatefulOnlyTightens(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "p", "plan")
	plan := `{"format_version":"1.2","resource_changes":[{"address":"terraform_data.db","mode":"managed","type":"terraform_data","change":{"actions":["delete"]}}]}`
	f := &fakeRunner{replies: map[string]string{"show -no-color -json p": plan}}
	if v := Evaluate(context.Background(), `terraform apply p`, dir, Deps{Runner: f}); v.Decision != verdict.Ask {
		t.Fatalf("base: %v", v.Decision)
	}
	v := Evaluate(context.Background(), `terraform apply p`, dir, Deps{Runner: f, ExtraStateful: []string{"terraform_data"}})
	if v.Decision != verdict.Deny {
		t.Fatalf("overlay: %v", v.Decision)
	}
}

func TestKubectlAndOCReachTheirJudges(t *testing.T) {
	f := &fakeRunner{replies: map[string]string{"get pv v": `{"kind":"PersistentVolume","metadata":{"name":"v"},"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`, "config current-context": "kind-test"}}
	if v := decide(t, `kubectl delete pv v`, "/w", f); v.Decision != verdict.Deny {
		t.Fatalf("kubectl: %v %v", v.Decision, v.Reasons)
	}
	f = &fakeRunner{}
	if v := decide(t, `oc delete pv v`, "/w", f); v.Decision != verdict.Ask || f.calls != 0 {
		t.Fatalf("oc: %v calls=%d", v.Decision, f.calls)
	}
}

func TestDefaultUsesTheRealRunner(t *testing.T) {
	if _, ok := Default().Runner.(runner.Exec); !ok {
		t.Fatal("not the real runner")
	}
}

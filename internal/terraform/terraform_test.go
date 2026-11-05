package terraform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

type fakeRunner struct {
	out   string
	err   error
	calls int
	dir   string
	opts  runner.Options
	args  []string
}

func (f *fakeRunner) Run(_ context.Context, opts runner.Options, _ string, args ...string) ([]byte, error) {
	f.calls++
	f.dir, f.opts, f.args = opts.Dir, opts, args
	return []byte(f.out), f.err
}

const destroyDB = `{"format_version":"1.2","applyable":true,"complete":true,"resource_changes":[
 {"address":"aws_db_instance.main","mode":"managed","type":"aws_db_instance","change":{"actions":["delete"]}}]}`

var stateful = map[string]bool{"aws_db_instance": true}

func eval(t *testing.T, cmd, cwd string, f *fakeRunner) verdict.Verdict {
	t.Helper()
	a := shell.Analyze(cmd, cwd)
	if a.ParseError != nil {
		t.Fatal(a.ParseError)
	}
	var out verdict.Verdict
	for i, inv := range a.Invocations {
		if inv.Tool == "terraform" {
			out = verdict.Join(out, Evaluate(context.Background(), inv, a.Invocations[:i], Deps{Runner: f, Stateful: stateful}))
		}
	}
	return out
}

func planDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadOnlyCommandsDecideNothing(t *testing.T) {
	for _, cmd := range []string{
		`terraform`, `terraform -version`, `terraform -help`, `terraform plan`, `terraform plan -destroy -out=p`,
		`terraform init -upgrade`, `terraform validate`, `terraform fmt`, `terraform show`, `terraform output`,
		`terraform state list`, `terraform state show x`, `terraform state pull`, `terraform workspace list`,
		`terraform workspace select dev`, `terraform -chdir=x plan`,
	} {
		f := &fakeRunner{}
		if v := eval(t, cmd, "/w", f); v.Decision != verdict.None {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
		if f.calls != 0 {
			t.Errorf("%s: ran a probe", cmd)
		}
	}
	if !ReadOnlySubcommand("plan") || ReadOnlySubcommand("apply") {
		t.Error("ReadOnlySubcommand is wrong")
	}
}

func TestNoSavedPlanIsDeniedWithInstructions(t *testing.T) {
	for _, cmd := range []string{
		`terraform destroy`, `terraform destroy -auto-approve`, `terraform apply`, `terraform apply -auto-approve`,
		`terraform apply --auto-approve=true`, `terraform apply -destroy`, `terraform apply -var a=b -target x`,
		`terraform apply -var-file=x.tfvars`, `echo yes | terraform apply`, `yes | terraform destroy`,
	} {
		f := &fakeRunner{}
		v := eval(t, cmd, "/w", f)
		if v.Decision != verdict.Deny {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
		if !strings.Contains(v.Advice, "terraform plan") || !strings.Contains(v.Advice, "separate call") {
			t.Errorf("%s: advice %q", cmd, v.Advice)
		}
		if f.calls != 0 {
			t.Errorf("%s: ran a probe", cmd)
		}
	}
	if v := eval(t, `terraform destroy`, "/w", &fakeRunner{}); !strings.Contains(v.Advice, "plan -destroy") {
		t.Errorf("destroy advice = %q", v.Advice)
	}
}

func TestAskWithoutProbing(t *testing.T) {
	cases := map[string]string{
		`terraform taint x`:                                 "taint",
		`terraform untaint x`:                               "untaint",
		`terraform force-unlock x`:                          "force-unlock",
		`terraform import a b`:                              "import",
		`terraform refresh`:                                 "refresh",
		`terraform test`:                                    "test",
		`terraform state rm x`:                              "state rm",
		`terraform state mv a b`:                            "state mv",
		`terraform state push x`:                            "state push",
		`terraform state`:                                   "state",
		`terraform workspace delete dev`:                    "workspace delete",
		`terraform workspace $X`:                            "workspace delete",
		`terraform init -migrate-state`:                     "migrate state",
		`terraform $X`:                                      "computed",
		`terraform -unknown plan`:                           "unknown global",
		`terraform apply $P`:                                "computed",
		`terraform apply -new-flag p`:                       "does not know",
		`terraform apply a b`:                               "more than one",
		`TF_CLI_ARGS_apply=-x terraform apply p`:            "TF_CLI_ARGS_apply",
		`TF_WORKSPACE=x terraform apply p`:                  "TF_WORKSPACE",
		`TF_DATA_DIR=x terraform apply p`:                   "TF_DATA_DIR",
		`PATH=/x terraform apply p`:                         "PATH",
		`terraform workspace select x && terraform apply p`: "changes the target",
		`./terraform apply p`:                               "plain terraform on PATH",
		`cd "$X" && terraform apply p`:                      "computed at run time",
		`terraform apply -`:                                 "looks like an option",
		`terraform apply missing`:                           "not found",
	}
	for cmd, want := range cases {
		f := &fakeRunner{}
		v := eval(t, cmd, "/w", f)
		if v.Decision != verdict.Ask {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
			continue
		}
		if !strings.Contains(strings.Join(v.Reasons, " "), want) {
			t.Errorf("%s: reasons %v, want %q", cmd, v.Reasons, want)
		}
		if f.calls != 0 {
			t.Errorf("%s: ran a probe", cmd)
		}
	}
}

func TestPlanWrittenInTheSameCommandIsDenied(t *testing.T) {
	dir := planDir(t, "p")
	for _, cmd := range []string{
		`terraform plan -out=p && terraform apply p`,
		`terraform plan -out=p; terraform apply p`,
		`cp other p && terraform apply p`,
		`cd . && curl -o ./p https://example.test/p && terraform apply p`,
	} {
		f := &fakeRunner{out: destroyDB}
		v := eval(t, cmd, dir, f)
		if v.Decision != verdict.Deny || !strings.Contains(v.Advice, "Split") {
			t.Errorf("%s: %v %v %q", cmd, v.Decision, v.Reasons, v.Advice)
		}
		if f.calls != 0 {
			t.Errorf("%s: read a plan that is about to change", cmd)
		}
	}
}

func TestApplyReadsThePlanFromTheRightPlace(t *testing.T) {
	dir := planDir(t, "p")
	f := &fakeRunner{out: destroyDB}
	v := eval(t, `terraform apply -auto-approve -parallelism 2 -lock=false p`, dir, f)
	if v.Decision != verdict.Deny {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
	if f.dir != dir || strings.Join(f.args, " ") != "show -no-color -json p" {
		t.Errorf("dir=%q args=%v", f.dir, f.args)
	}
	if !contains(f.opts.DropEnv, "TF_REATTACH_PROVIDERS") || !contains(f.opts.DropEnv, "TF_CLI_ARGS*") {
		t.Errorf("drop = %v", f.opts.DropEnv)
	}
	if len(f.opts.SetEnv) != 1 || !strings.HasPrefix(f.opts.SetEnv[0], "TF_CLI_CONFIG_FILE=") {
		t.Errorf("set = %v", f.opts.SetEnv)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestChdirIsResolved(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "envs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{`terraform -chdir=envs apply p`, `terraform -chdir=` + dir + ` apply p`, `cd envs && terraform apply p`} {
		f := &fakeRunner{out: destroyDB}
		v := eval(t, cmd, parent, f)
		if v.Decision != verdict.Deny || f.dir != dir {
			t.Errorf("%s: %v dir=%q reasons=%v", cmd, v.Decision, f.dir, v.Reasons)
		}
	}
}

func TestAbsolutePlanPath(t *testing.T) {
	dir := planDir(t, "p")
	f := &fakeRunner{out: destroyDB}
	if v := eval(t, `terraform apply `+filepath.Join(dir, "p"), "/somewhere", f); v.Decision != verdict.Deny {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestDirectoryIsNotAPlan(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	if v := eval(t, `terraform apply d`, dir, f); v.Decision != verdict.Ask || f.calls != 0 {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestProbeFailureIsAsk(t *testing.T) {
	dir := planDir(t, "p")
	f := &fakeRunner{err: errors.New("Failed to load plugin schemas")}
	v := eval(t, `terraform apply p`, dir, f)
	if v.Decision != verdict.Ask || !strings.Contains(v.Reasons[0], "plugin schemas") {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestAmbientTFCLIArgsDoNotBlockPlanReading(t *testing.T) {
	t.Setenv("TF_CLI_ARGS_apply", "-parallelism=2")
	dir := planDir(t, "p")
	f := &fakeRunner{out: destroyDB}
	if v := eval(t, `terraform apply p`, dir, f); v.Decision != verdict.Deny || f.calls != 1 {
		t.Fatalf("%v %v calls=%d", v.Decision, v.Reasons, f.calls)
	}
}

func TestAdviceNamesAPathTerraformCanWrite(t *testing.T) {
	bare := t.TempDir()
	v := eval(t, `terraform apply -auto-approve`, bare, &fakeRunner{})
	if !strings.Contains(v.Advice, "-out=whatdies.tfplan") || !strings.Contains(v.Advice, "do not commit") {
		t.Errorf("no .terraform: %q", v.Advice)
	}
	inited := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inited, "envs", ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	v = eval(t, `terraform -chdir=envs destroy`, inited, &fakeRunner{})
	if !strings.Contains(v.Advice, "plan -destroy -out=.terraform/whatdies.tfplan") || strings.Contains(v.Advice, "do not commit") {
		t.Errorf("with .terraform: %q", v.Advice)
	}
	v = eval(t, `cd "$X" && terraform destroy`, inited, &fakeRunner{})
	if !strings.Contains(v.Advice, "-out=whatdies.tfplan") {
		t.Errorf("unknown dir: %q", v.Advice)
	}
}

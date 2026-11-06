package terraform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

var stateful = map[string]bool{"aws_db_instance": true}

func eval(t *testing.T, cmd, cwd string) verdict.Verdict {
	t.Helper()
	a := shell.Analyze(cmd, cwd)
	if a.ParseError != nil {
		t.Fatal(a.ParseError)
	}
	var out verdict.Verdict
	for i, inv := range a.Invocations {
		if inv.Tool == "terraform" {
			out = verdict.Join(out, Evaluate(inv, a.Invocations[:i], Deps{Stateful: func() (map[string]bool, error) { return stateful, nil }}))
		}
	}
	return out
}

// fatalDir holds a plan that is denied once read, so a verdict without
// StopAdvice shows the plan was not read.
func fatalDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	writePlan(t, dir, name, fatalPlan)
	return dir
}

func readThePlan(v verdict.Verdict) bool {
	return v.Decision == verdict.Deny && v.Advice == verdict.StopAdvice
}

func TestReadOnlyCommandsDecideNothing(t *testing.T) {
	for _, cmd := range []string{
		`terraform`, `terraform -version`, `terraform -help`, `terraform plan`, `terraform plan -destroy -out=p`,
		`terraform init -upgrade`, `terraform validate`, `terraform fmt`, `terraform show`, `terraform output`,
		`terraform state list`, `terraform state show x`, `terraform state pull`, `terraform workspace list`,
		`terraform workspace select dev`, `terraform -chdir=x plan`,
	} {
		if v := eval(t, cmd, "/w"); v.Decision != verdict.None {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
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
		v := eval(t, cmd, "/w")
		if v.Decision != verdict.Deny {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
		if !strings.Contains(v.Advice, "terraform plan") || !strings.Contains(v.Advice, "separate call") {
			t.Errorf("%s: advice %q", cmd, v.Advice)
		}
	}
	if v := eval(t, `terraform destroy`, "/w"); !strings.Contains(v.Advice, "plan -destroy") {
		t.Errorf("destroy advice = %q", v.Advice)
	}
}

func TestAskWithoutReadingThePlan(t *testing.T) {
	dir := fatalDir(t, "p")
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
		v := eval(t, cmd, dir)
		if v.Decision != verdict.Ask {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
			continue
		}
		if !strings.Contains(strings.Join(v.Reasons, " "), want) {
			t.Errorf("%s: reasons %v, want %q", cmd, v.Reasons, want)
		}
	}
}

func TestPlanWrittenInTheSameCommandIsDenied(t *testing.T) {
	dir := fatalDir(t, "p")
	for _, cmd := range []string{
		`terraform plan -out=p && terraform apply p`,
		`terraform plan -out=p; terraform apply p`,
		`cp other p && terraform apply p`,
		`cd . && curl -o ./p https://example.test/p && terraform apply p`,
	} {
		v := eval(t, cmd, dir)
		if v.Decision != verdict.Deny || !strings.Contains(v.Advice, "Split") {
			t.Errorf("%s: %v %v %q", cmd, v.Decision, v.Reasons, v.Advice)
		}
	}
}

func TestApplyReadsThePlanFromTheRightPlace(t *testing.T) {
	dir := fatalDir(t, "p")
	v := eval(t, `terraform apply -auto-approve -parallelism 2 -lock=false p`, dir)
	if !readThePlan(v) || !strings.Contains(v.Reasons[0], "aws_db_instance.main") {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestChdirIsResolved(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "envs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePlan(t, dir, "p", fatalPlan)
	for _, cmd := range []string{`terraform -chdir=envs apply p`, `terraform -chdir=` + dir + ` apply p`, `cd envs && terraform apply p`} {
		if v := eval(t, cmd, parent); !readThePlan(v) {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
}

func TestAbsolutePlanPath(t *testing.T) {
	dir := fatalDir(t, "p")
	if v := eval(t, `terraform apply `+filepath.Join(dir, "p"), "/somewhere"); !readThePlan(v) {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestUnreadablePlansAreAsk(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "text"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]string{`terraform apply d`: "directory", `terraform apply text`: "not a zip"} {
		v := eval(t, cmd, dir)
		if v.Decision != verdict.Ask || !strings.Contains(v.Reasons[0], want) {
			t.Errorf("%s: %v %v", cmd, v.Decision, v.Reasons)
		}
	}
}

func TestStatefulLoadFailureIsAsk(t *testing.T) {
	dir := fatalDir(t, "p")
	a := shell.Analyze(`terraform apply p`, dir)
	v := Evaluate(a.Invocations[0], nil, Deps{Stateful: func() (map[string]bool, error) { return nil, errors.New("registry broken") }})
	if v.Decision != verdict.Ask || v.Reasons[0] != "registry broken" {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestAmbientTFCLIArgsDoNotBlockPlanReading(t *testing.T) {
	t.Setenv("TF_CLI_ARGS_apply", "-parallelism=2")
	if v := eval(t, `terraform apply p`, fatalDir(t, "p")); !readThePlan(v) {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func TestAdviceNamesAPathTerraformCanWrite(t *testing.T) {
	bare := t.TempDir()
	v := eval(t, `terraform apply -auto-approve`, bare)
	if !strings.Contains(v.Advice, "-out=whatdies.tfplan") || !strings.Contains(v.Advice, "do not commit") {
		t.Errorf("no .terraform: %q", v.Advice)
	}
	inited := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inited, "envs", ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	v = eval(t, `terraform -chdir=envs destroy`, inited)
	if !strings.Contains(v.Advice, "plan -destroy -out=.terraform/whatdies.tfplan") || strings.Contains(v.Advice, "do not commit") {
		t.Errorf("with .terraform: %q", v.Advice)
	}
	v = eval(t, `cd "$X" && terraform destroy`, inited)
	if !strings.Contains(v.Advice, "-out=whatdies.tfplan") {
		t.Errorf("unknown dir: %q", v.Advice)
	}
}

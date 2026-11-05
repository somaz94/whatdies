package shell

import (
	"strings"
	"testing"
)

func tools(a Analysis) string {
	names := make([]string, 0, len(a.Invocations))
	for _, i := range a.Invocations {
		names = append(names, i.Tool)
	}
	return strings.Join(names, ",")
}

func TestFindsEveryProgramAStatementStarts(t *testing.T) {
	cases := map[string]string{
		`ls`:                       "ls",
		`ls && terraform plan`:     "ls,terraform",
		`a | b || c ; d`:           "a,b,c,d",
		`(cd x; kubectl get pods)`: "kubectl",
		`{ helm list; }`:           "helm",
		`if a; then b; elif c; then d; else e; fi`: "a,b,c,d,e",
		`while a; do b; done`:                      "a,b",
		`for x in 1 2; do kubectl get $x; done`:    "kubectl",
		`for x in $(helm list); do echo $x; done`:  "helm,echo",
		`case x in y) kubectl get pods;; esac`:     "kubectl",
		`f() { terraform destroy; }; f`:            "terraform,f",
		`time terraform plan`:                      "terraform",
		`coproc kubectl get pods`:                  "kubectl",
		`echo $(terraform destroy)`:                "terraform,echo",
		`cat <(kubectl get pods)`:                  "kubectl,cat",
		`[[ $(helm list) ]]`:                       "helm",
		`x=$(kubectl get pods)`:                    "kubectl",
		`export X=$(helm list)`:                    "helm",
		`cat <<< $(helm list)`:                     "helm,cat",
		`/usr/local/bin/terraform destroy`:         "terraform",
		`\terraform destroy`:                       "terraform",
		`'terraform' destroy`:                      "terraform",
		`"terraform" destroy`:                      "terraform",
		`k delete ns x`:                            "kubectl",
		`tf destroy`:                               "terraform",
		`tg apply`:                                 "terragrunt",
		`kubecolor delete ns x`:                    "kubectl",
		`terraform.exe destroy`:                    "terraform",
	}
	for cmd, want := range cases {
		if got := tools(Analyze(cmd, "/w")); got != want {
			t.Errorf("%s: got %q, want %q", cmd, got, want)
		}
	}
}

func TestSeesThroughWrappers(t *testing.T) {
	for _, cmd := range []string{
		`env terraform destroy`,
		`env -i A=1 -u B terraform destroy`,
		`env -- terraform destroy`,
		`sudo terraform destroy`,
		`sudo -u root -E A=1 terraform destroy`,
		`doas -u root terraform destroy`,
		`command terraform destroy`,
		`command -p terraform destroy`,
		`builtin terraform destroy`,
		`exec -a name terraform destroy`,
		`nohup terraform destroy`,
		`nice -n 10 terraform destroy`,
		`ionice -c 2 -n 7 terraform destroy`,
		`timeout 60 terraform destroy`,
		`timeout -k 5 60 terraform destroy`,
		`gtimeout --signal KILL 60 terraform destroy`,
		`stdbuf -o L terraform destroy`,
		`taskpolicy -b terraform destroy`,
		`caffeinate -i terraform destroy`,
		`watch -n 5 terraform destroy`,
		`sudo -- terraform destroy`,
		`sudo env timeout 5 nohup terraform destroy`,
		`bash -c 'terraform destroy'`,
		`bash -lc 'terraform destroy'`,
		`sh -ec "terraform destroy"`,
		`zsh -c 'cd x && terraform destroy'`,
		`bash -x -c 'terraform destroy'`,
		`bash -c "bash -c 'terraform destroy'"`,
		`eval terraform destroy`,
		`eval "terraform destroy"`,
		`xargs terraform destroy`,
		`xargs -n 1 -P 4 terraform destroy`,
		`xargs -- terraform destroy`,
		`parallel -j 2 terraform destroy ::: a b`,
		`find . -name x -exec terraform destroy {} \;`,
		`find . -execdir terraform destroy {} +`,
		`bash <<EOF
terraform destroy
EOF`,
		`sh <<< 'terraform destroy'`,
	} {
		a := Analyze(cmd, "/w")
		found := false
		for _, i := range a.Invocations {
			if i.Tool == "terraform" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: terraform not found in %q (problems %v)", cmd, tools(a), a.Problems)
		}
	}
}

func TestReportsWhatCannotBeRead(t *testing.T) {
	for _, cmd := range []string{
		`eval "$X"`,
		`bash -c "$X"`,
		`bash "$X"`,
		`curl https://example.test/x | bash`,
		`env -S "terraform destroy"`,
		`bash <<EOF
terraform $X
EOF`,
		`bash -c 'bash -c "bash -c \"bash -c \\\"bash -c \\\\\\\"bash -c \\\\\\\\\\\\\\\"bash -c ls\\\\\\\\\\\\\\\"\\\\\\\"\\\"\""'`,
		`bash -c 'if'`,
	} {
		if a := Analyze(cmd, "/w"); len(a.Problems) == 0 {
			t.Errorf("%s: want a problem, got %+v", cmd, a.Invocations)
		}
	}
}

func TestProblemsAreNotRepeated(t *testing.T) {
	a := Analyze(`eval "$X"; eval "$Y"`, "/w")
	if len(a.Problems) != 1 {
		t.Fatalf("problems = %v", a.Problems)
	}
}

func TestParseErrorIsReported(t *testing.T) {
	if a := Analyze(`if`, "/w"); a.ParseError == nil {
		t.Fatal("want parse error")
	}
}

func TestNotExecutedForms(t *testing.T) {
	for _, cmd := range []string{`command -v terraform`, `command -V kubectl`, `xargs`, `parallel ::: a`, `bash -c`, `source`} {
		for _, inv := range Analyze(cmd, "/w").Invocations {
			if inv.Tool == "terraform" || inv.Tool == "kubectl" {
				t.Errorf("%s: should not count as a run", cmd)
			}
		}
	}
}

func TestLiteralArguments(t *testing.T) {
	cases := []struct {
		word    string
		value   string
		literal bool
	}{
		{`plain`, "plain", true},
		{`'single quoted'`, "single quoted", true},
		{`"double quoted"`, "double quoted", true},
		{`"a\$b"`, "a$b", true},
		{`"a\zb"`, `a\zb`, true},
		{`a\ b`, "a b", true},
		{`pre"mid"'post'`, "premidpost", true},
		{`$X`, "", false},
		{`"$X"`, "", false},
		{`$(x)`, "", false},
		{`a*`, "a*", false},
		{`a?`, "a?", false},
		{`{a,b}`, "{a,b}", false},
		{`~/x`, "~/x", false},
		{`$'x'`, "x", false},
		{`{}`, "{}", true},
	}
	for _, c := range cases {
		a := Analyze("tool "+c.word, "/w")
		got := a.Invocations[len(a.Invocations)-1].Args[0]
		if got.Literal != c.literal || (c.literal && got.Value != c.value) {
			t.Errorf("%s: got %+v", c.word, got)
		}
	}
}

func last(t *testing.T, cmd string) Invocation {
	t.Helper()
	a := Analyze(cmd, "/w")
	if len(a.Invocations) == 0 {
		t.Fatalf("%s: nothing found", cmd)
	}
	return a.Invocations[len(a.Invocations)-1]
}

func TestTracksTheWorkingDirectory(t *testing.T) {
	cases := []struct {
		cmd   string
		dir   string
		known bool
	}{
		{`terraform plan`, "/w", true},
		{`cd envs && terraform plan`, "/w/envs", true},
		{`cd envs; cd ../other; terraform plan`, "/w/other", true},
		{`cd /abs && terraform plan`, "/abs", true},
		{`cd -P envs && terraform plan`, "/w/envs", true},
		{`pushd envs && terraform plan`, "/w/envs", true},
		{`(cd envs) && terraform plan`, "/w", true},
		{`cd "$X" && terraform plan`, "", false},
		{`cd - && terraform plan`, "", false},
		{`cd && terraform plan`, "", false},
		{`popd && terraform plan`, "", false},
		{`env -C elsewhere ls && terraform plan`, "", false},
	}
	for _, c := range cases {
		inv := last(t, c.cmd)
		if inv.DirKnown != c.known || (c.known && inv.Dir != c.dir) {
			t.Errorf("%s: dir=%q known=%v", c.cmd, inv.Dir, inv.DirKnown)
		}
	}
	if inv := last(t, `cd x && cd y && terraform plan`); inv.Dir != "/w/x/y" {
		t.Errorf("dir = %q", inv.Dir)
	}
}

func TestEnvironmentIsTracked(t *testing.T) {
	cases := map[string]string{
		`KUBECONFIG=/k kubectl get pods`:                         "/k",
		`export KUBECONFIG=/k; kubectl get pods`:                 "/k",
		`KUBECONFIG=/k; kubectl get pods`:                        "/k",
		`env KUBECONFIG=/k kubectl get pods`:                     "/k",
		`sudo KUBECONFIG=/k kubectl get pods`:                    "/k",
		`(export KUBECONFIG=/k); KUBECONFIG=/j kubectl get pods`: "/j",
	}
	for cmd, want := range cases {
		if got := last(t, cmd).Env["KUBECONFIG"]; got.Value != want {
			t.Errorf("%s: got %+v", cmd, got)
		}
	}
	if _, ok := last(t, `(export KUBECONFIG=/k); kubectl get pods`).Env["KUBECONFIG"]; ok {
		t.Error("a subshell's export must not reach its parent")
	}
}

func TestTargetChangesAreNoticed(t *testing.T) {
	for _, cmd := range []string{
		`kubectl config use-context prod && kubectl get pods`,
		`kubectx prod && kubectl get pods`,
		`kubens prod; kubectl get pods`,
		`kctx prod; kubectl get pods`,
		`terraform workspace select prod && kubectl get pods`,
		`aws eks update-kubeconfig --name x && kubectl get pods`,
		`gcloud container clusters get-credentials x && kubectl get pods`,
		`az aks get-credentials -n x && kubectl get pods`,
	} {
		if last(t, cmd).TargetChange == "" {
			t.Errorf("%s: not noticed", cmd)
		}
	}
	for _, cmd := range []string{`kubectl get pods`, `kubectl config view; kubectl get pods`, `(kubectx prod); kubectl get pods`} {
		if got := last(t, cmd).TargetChange; got != "" {
			t.Errorf("%s: unexpected %q", cmd, got)
		}
	}
}

func TestComputedArgumentsFromXargsAndFind(t *testing.T) {
	for _, cmd := range []string{`xargs kubectl delete pod`, `find . -exec kubectl delete -f {} \;`} {
		var inv Invocation
		for _, i := range Analyze(cmd, "/w").Invocations {
			if i.Tool == "kubectl" {
				inv = i
			}
		}
		if inv.Tool != "kubectl" {
			t.Fatalf("%s: kubectl not found", cmd)
		}
		computedFound := false
		for _, a := range inv.Args {
			if !a.Literal {
				computedFound = true
			}
		}
		if !computedFound {
			t.Errorf("%s: no computed argument in %+v", cmd, inv.Args)
		}
	}
}

func TestStdin(t *testing.T) {
	inv := last(t, "kubectl apply -f - <<EOF\nkind: Pod\nEOF")
	if !inv.HasHeredoc || !inv.HeredocLiteral || !strings.Contains(inv.Heredoc, "kind: Pod") {
		t.Errorf("heredoc = %+v", inv)
	}
	inv = last(t, "kubectl apply -f - <<EOF\nname: $X\nEOF")
	if !inv.HasHeredoc || inv.HeredocLiteral {
		t.Errorf("expanding heredoc = %+v", inv)
	}
	if inv := last(t, `kubectl apply -f - <<< 'kind: Pod'`); !inv.HasHeredoc || inv.Heredoc != "kind: Pod" {
		t.Errorf("here-string = %+v", inv)
	}
}

func TestProgramFiles(t *testing.T) {
	cases := map[string]struct{ tool, arg string }{
		`bash deploy.sh`:               {ScriptTool, "deploy.sh"},
		`sh -x deploy.sh`:              {ScriptTool, "deploy.sh"},
		`bash -- deploy.sh`:            {ScriptTool, "deploy.sh"},
		`bash < deploy.sh`:             {ScriptTool, "deploy.sh"},
		`./deploy.sh now`:              {ScriptTool, "./deploy.sh"},
		`scripts/deploy`:               {ScriptTool, "scripts/deploy"},
		`source env.sh`:                {ScriptTool, "env.sh"},
		`. env.sh`:                     {ScriptTool, "env.sh"},
		`python3 run.py --now`:         {ScriptTool, "run.py"},
		`python3 -u run.py`:            {ScriptTool, "run.py"},
		`python3 -c 'print(1)'`:        {CodeTool, "print(1)"},
		`node -e 'x()'`:                {CodeTool, "x()"},
		"python3 <<EOF\nprint(2)\nEOF": {CodeTool, "print(2)\n"},
	}
	for cmd, want := range cases {
		inv := last(t, cmd)
		if inv.Tool != want.tool || inv.Args[0].Value != want.arg {
			t.Errorf("%s: got %s %+v", cmd, inv.Tool, inv.Args)
		}
	}
	for _, cmd := range []string{`python3 "$X"`, `python3`} {
		if a := Analyze(cmd, "/w"); len(a.Invocations) != 0 {
			t.Errorf("%s: unexpected %+v", cmd, a.Invocations)
		}
	}
	if inv := last(t, `python3 -m pytest -q`); inv.Tool != "python" || inv.Args[0].Value != "pytest" {
		t.Errorf("-m: %+v", inv)
	}
}

func TestPathToKnownToolStaysThatTool(t *testing.T) {
	inv := last(t, `./terraform destroy`)
	if inv.Tool != "terraform" || inv.Bare {
		t.Fatalf("%+v", inv)
	}
	if inv := last(t, `terraform destroy`); !inv.Bare {
		t.Fatal("bare not noticed")
	}
}

func TestComputedProgramName(t *testing.T) {
	inv := last(t, `$TF destroy`)
	if inv.Tool != "" || inv.Args[0].Value != "destroy" {
		t.Fatalf("%+v", inv)
	}
}

func TestLiterals(t *testing.T) {
	got := last(t, `tool a $b c`).Literals()
	if strings.Join(got, ",") != "a,c" {
		t.Fatalf("got %v", got)
	}
}

func TestInvocationBudget(t *testing.T) {
	a := Analyze(strings.Repeat("a;", maxInvocations+5), "/w")
	if len(a.Problems) == 0 || len(a.Invocations) != maxInvocations {
		t.Fatalf("invocations=%d problems=%v", len(a.Invocations), a.Problems)
	}
}

func TestFileNamesInWritesAndArguments(t *testing.T) {
	if !last(t, `echo x > d/q`).WritesFile("q") || last(t, `echo x > r`).WritesFile("q") || last(t, `echo q`).WritesFile("q") {
		t.Error("WritesFile is wrong")
	}
	for _, cmd := range []string{`curl -oq u`, `curl --output=q u`, `cp a d/q`} {
		if !last(t, cmd).ArgNamesFile("q") {
			t.Errorf("%s: not noticed", cmd)
		}
	}
	for _, cmd := range []string{`curl u`, `tool --q`, `tool -q`} {
		if last(t, cmd).ArgNamesFile("q") {
			t.Errorf("%s: noticed", cmd)
		}
	}
}

// Each of these once ended without recording anything, so nothing later could see it.
func TestProblemsForCommandsThatCannotBeRead(t *testing.T) {
	for _, cmd := range []string{
		`$(pick) destroy`, `parallel "$X"`, `parallel ::: "$X"`, `parallel -a cmds`, `parallel --arg-file=cmds`, `parallel :::: cmds`,
		`parallel < cmds`, `cat cmds | parallel`, "parallel <<E\ncmd\nE", `watch "$X"`, `bash -c -- "$X"`, `bash -- "$X"`, `bash "$X"`, `bash -c "$X"`,
		`cat x | bash -s "$X"`, `cat x | bash -s -- y`, `cat x | bash /dev/stdin`, `cat x | . /dev/stdin`, `cat x | python3 /dev/stdin`, `cat x | python3 -`,
		`source <(x)`, `python3 "$X"`, `perl -e"$X"`, "bash <<E\n$X\nE", "python3 <<E\n$X\nE", `cat x | python3`, `cat x | bash`, `env -S'a b'`, `env --split-string=x`,
	} {
		if a := Analyze(cmd, "/w"); len(a.Problems) == 0 {
			t.Errorf("%s: want a problem, got %+v", cmd, a.Invocations)
		}
	}
	for _, cmd := range []string{`parallel`, `parallel --version`, `watch`, `watch -n 1`, `bash -s`, `python3 -c`, `python3 -c x -`, `python3 -c x "$Y"`} {
		if a := Analyze(cmd, "/w"); len(a.Problems) != 0 {
			t.Errorf("%s: unexpected %v", cmd, a.Problems)
		}
	}
}

func TestTextHandedToAShell(t *testing.T) {
	cases := map[string]string{
		`bash --command='tool x'`:        "tool",
		`fish --command 'tool x'`:        "tool",
		`bash -o pipefail -c 'tool x'`:   "tool",
		`bash -euo pipefail -c 'tool x'`: "tool",
		`bash +o posix -c 'tool x'`:      "tool",
		`bash -c -- 'tool x'`:            "tool",
		`bash -s -c 'tool x'`:            "tool",
		"bash -s y <<E\ntool x\nE":       "tool",
		"bash /dev/stdin <<E\ntool\nE":   "tool",
		"source /dev/fd/0 <<E\ntool\nE":  "tool",
		`parallel ::: 'tool x'`:          "tool",
		`sudo -- tool x`:                 "tool",
		`command -p tool x`:              "tool",
	}
	for cmd, want := range cases {
		if inv := last(t, cmd); inv.Tool != want {
			t.Errorf("%s: got %q", cmd, inv.Tool)
		}
	}
	if inv := last(t, `bash < run.sh`); inv.Tool != ScriptTool || inv.Args[0].Value != "run.sh" {
		t.Errorf("redirected script: %+v", inv)
	}
	if inv := last(t, `bash -s < run.sh`); inv.Tool != ScriptTool || inv.Args[0].Value != "run.sh" {
		t.Errorf("redirected script after -s: %+v", inv)
	}
}

func TestInterpreterFlagsDifferByLanguage(t *testing.T) {
	cases := map[string]struct{ tool, arg string }{
		`ruby -r json -e 'x'`:          {CodeTool, "x"},
		`ruby -I lib run.rb`:           {ScriptTool, "run.rb"},
		`perl -p -e 'x' f`:             {CodeTool, "x"},
		`perl -I lib run.pl`:           {ScriptTool, "run.pl"},
		`perl -i.bak -pe 'x' f`:        {CodeTool, "x"},
		`perl -e 'a' -e 'b'`:           {CodeTool, "b"},
		`perl -e'x'`:                   {CodeTool, "-ex"},
		`node --print 'x'`:             {CodeTool, "x"},
		`node -r mod run.js`:           {ScriptTool, "run.js"},
		`node --eval=x`:                {CodeTool, "--eval=x"},
		`php -r 'x'`:                   {CodeTool, "x"},
		`php -d a=b run.php`:           {ScriptTool, "run.php"},
		`python3 -E run.py`:            {ScriptTool, "run.py"},
		`python3 -I run.py`:            {ScriptTool, "run.py"},
		`python3 -W ignore run.py`:     {ScriptTool, "run.py"},
		`python3.12 run.py`:            {ScriptTool, "run.py"},
		`python3 < run.py`:             {ScriptTool, "run.py"},
		"python3 /dev/stdin <<E\nx\nE": {CodeTool, "x\n"},
		"python3 - <<E\nx\nE":          {CodeTool, "x\n"},
	}
	for cmd, want := range cases {
		inv := last(t, cmd)
		if inv.Tool != want.tool || inv.Args[0].Value != want.arg {
			t.Errorf("%s: got %s %+v", cmd, inv.Tool, inv.Args)
		}
	}
}

func TestEnvOptions(t *testing.T) {
	for _, cmd := range []string{`env -u K tool`, `env --unset K tool`, `env --unset=K tool`, `env -uK tool`} {
		inv := last(t, cmd)
		if _, ok := inv.Env["K"]; !ok || inv.Tool != "tool" {
			t.Errorf("%s: %+v", cmd, inv)
		}
	}
	if _, ok := last(t, `env -i tool`).Env["KUBECONFIG"]; !ok {
		t.Error("env -i drops KUBECONFIG")
	}
	for _, cmd := range []string{`env -C d tool`, `env --chdir d tool`, `env -Cd tool`, `env --chdir=d tool`} {
		if inv := last(t, cmd); inv.DirKnown || inv.Tool != "tool" {
			t.Errorf("%s: %+v", cmd, inv)
		}
	}
	if inv := last(t, `env "$X" tool`); inv.Tool != "" {
		t.Errorf("a computed word may be the program: %+v", inv)
	}
	if a := Analyze(`env -u`, "/w"); len(a.Invocations) != 0 {
		t.Errorf("nothing runs: %+v", a.Invocations)
	}
}

func TestTheRestOfWhatEmitRecognises(t *testing.T) {
	if inv := last(t, `terraform-1.9 destroy`); inv.Tool != "terraform" || inv.Bare {
		t.Errorf("versioned: %+v", inv)
	}
	if got := tools(Analyze(`/usr/bin/make test`, "/w")); got != ScriptTool+",make" {
		t.Errorf("make by path: %q", got)
	}
	if got := tools(Analyze(`./gmake test`, "/w")); got != ScriptTool+",gmake" {
		t.Errorf("gmake by path: %q", got)
	}
	for _, cmd := range []string{`oc project x; kubectl get pods`, `oc login u; kubectl get pods`, `source env.sh; kubectl get pods`, `source "$X"; kubectl get pods`} {
		if last(t, cmd).TargetChange == "" {
			t.Errorf("%s: not noticed", cmd)
		}
	}
	if got := last(t, `oc; kubectl get pods`).TargetChange; got != "" {
		t.Errorf("oc alone: %q", got)
	}
	if inv := last(t, `sudo -D d tool`); inv.DirKnown {
		t.Errorf("sudo -D: %+v", inv)
	}
	if inv := last(t, `sudo A=1 tool`); inv.Env["A"].Value != "1" {
		t.Errorf("sudo assignment: %+v", inv)
	}
	if a := Analyze(`command -v tool`, "/w"); len(a.Invocations) != 0 {
		t.Errorf("command -v runs nothing: %+v", a.Invocations)
	}
	if inv := last(t, `command -p tool`); inv.Tool != "tool" {
		t.Errorf("command -p: %+v", inv)
	}
	if a := Analyze(`time`, "/w"); len(a.Invocations) != 0 {
		t.Errorf("time alone: %+v", a.Invocations)
	}
}

func TestWordsAndAssignments(t *testing.T) {
	cases := map[string]Arg{
		"a\\\nb":     {Value: "ab", Literal: true},
		"\"a\\\nb\"": {Value: "ab", Literal: true},
		"-I{}":       {Value: "-I{}", Literal: true},
		"{x}":        {Value: "{x}", Literal: true},
		"{1..3}":     {Value: "{1..3}"},
		"~/x":        {Value: "~/x", Home: true},
		"~/*":        {Value: "~/*"},
		"~/{a,b}":    {Value: "~/{a,b}"},
	}
	for word, want := range cases {
		if got := last(t, "tool "+word).Args[0]; got != want {
			t.Errorf("%q: got %+v", word, got)
		}
	}
	if got := last(t, `cmd=(a b); $cmd`).Env["cmd"]; !got.Literal || got.Value != "a b" {
		t.Errorf("array: %+v", got)
	}
	if got := last(t, `cmd=(a "$b"); $cmd`).Env["cmd"]; got.Literal {
		t.Errorf("computed array: %+v", got)
	}
	if got := last(t, `A=1; (tool)`).Env["A"]; got.Value != "1" {
		t.Errorf("a subshell sees the parent's variables: %+v", got)
	}
	if got := last(t, `echo x > "$OUT"`).Writes; len(got) != 0 {
		t.Errorf("a computed target is not a known write: %v", got)
	}
}

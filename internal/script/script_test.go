package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMention(t *testing.T) {
	hits := []string{
		"terraform destroy -auto-approve",
		"tofu apply",
		"terragrunt run-all destroy",
		"terraform state rm x",
		"kubectl delete ns x",
		"kubectl -n x delete pod y",
		`subprocess.run(["kubectl", "delete", "ns", name])`,
		"oc delete project x",
		"helm uninstall x",
		"helm upgrade x y",
		"helmfile sync",
		"  terraform apply",
	}
	for _, text := range hits {
		if Mention(text) == "" {
			t.Errorf("%q: no mention", text)
		}
	}
	misses := []string{
		"terraform plan",
		"kubectl get pods",
		"helm list",
		"# terraform destroy",
		"// kubectl delete ns x",
		"delete the terraform folder", // verb before the tool
		"go test ./...",
	}
	for _, text := range misses {
		if m := Mention(text); m != "" {
			t.Errorf("%q: unexpected %q", text, m)
		}
	}
}

func TestMentionHasNoLineLimit(t *testing.T) {
	text := strings.Repeat("x", maxBytes+10) + " terraform destroy\n"
	if Mention(text) == "" {
		t.Error("a line past the scanner's old limit stopped the search")
	}
}

func TestMentionContinuations(t *testing.T) {
	for _, text := range []string{"terraform \\\n  destroy", "terraform \\\r\n  destroy", "# note \\\nterraform destroy", "terraform \\\n"} {
		got := Mention(text)
		if (got == "") != (text == "terraform \\\n") {
			t.Errorf("%q: got %q", text, got)
		}
	}
}

func write(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	if File(write(t, dir, "bad.sh", "#!/bin/sh\nterraform destroy\n")) == "" {
		t.Error("missed")
	}
	if m := File(write(t, dir, "ok.sh", "#!/bin/sh\ngo test\n")); m != "" {
		t.Errorf("unexpected %q", m)
	}
	if File(filepath.Join(dir, "absent.sh")) != "" {
		t.Error("absent file must be empty")
	}
	if File(dir) != "" {
		t.Error("a directory must be empty")
	}
}

func TestFileReadsAtMostTheLimit(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x\n", maxBytes) + "terraform destroy\n"
	if m := File(write(t, dir, "big.sh", body)); m != "" {
		t.Errorf("read past the limit: %q", m)
	}
}

const makefileText = `.PHONY: test deploy
VERSION := 1
all: test

test:
	go test ./...

deploy: prepare
	echo deploying

prepare:
	terraform apply -auto-approve

clean nuke: ; rm -rf bin

drop:
	kubectl delete ns x
	# kubectl comment

# helm uninstall x
`

func TestMakeFollowsTargetsAndPrerequisites(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", makefileText)
	cases := []struct {
		targets []string
		hit     bool
	}{
		{nil, false},
		{[]string{"test"}, false},
		{[]string{"deploy"}, true},
		{[]string{"prepare"}, true},
		{[]string{"drop"}, true},
		{[]string{"test", "drop"}, true},
		{[]string{"clean"}, false},
		{[]string{"from-an-include"}, true},
	}
	for _, c := range cases {
		if got := Make(dir, "", c.targets, nil, nil) != ""; got != c.hit {
			t.Errorf("%v: hit=%v", c.targets, got)
		}
	}
}

func TestMakeFinds(t *testing.T) {
	dir := t.TempDir()
	if Make(dir, "", nil, nil, nil) != "" {
		t.Error("no makefile must be empty")
	}
	write(t, dir, "Custom.mk", "go:\n\tterraform destroy\n")
	if Make(dir, "Custom.mk", nil, nil, nil) == "" {
		t.Error("-f file missed")
	}
	if Make(dir, filepath.Join(dir, "Custom.mk"), []string{"go"}, nil, nil) == "" {
		t.Error("absolute -f file missed")
	}
	write(t, dir, "GNUmakefile", "VERSION := 1\n")
	if Make(dir, "", nil, nil, nil) != "" {
		t.Error("a file without rules must be empty")
	}
}

func TestMakeSurvivesCycles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", "a: b\nb: a\n\tgo test\n")
	if Make(dir, "", []string{"a"}, nil, nil) != "" {
		t.Error("unexpected mention")
	}
}

func TestMentionSkipsLinesThatOnlyPrintOrDefine(t *testing.T) {
	for _, text := range []string{`echo "usage: terraform destroy"`, `@echo terraform destroy`, `printf 'kubectl delete\n'`, `alias kd='kubectl delete'`} {
		if m := Mention(text); m != "" {
			t.Errorf("%q: %q", text, m)
		}
	}
	for _, text := range []string{`echo x | terraform destroy`, `echo "$(terraform destroy)"`, `echo 'terraform destroy' > y.sh`, `echo x; terraform destroy`} {
		if Mention(text) == "" {
			t.Errorf("%q: missed", text)
		}
	}
}

func TestMentionReportsTheCommandNotWhatPrecedesIt(t *testing.T) {
	cases := map[string]string{"run k delete pod": "k delete", `"tf destroy"`: "tf destroy", "tf destroy": "tf destroy"}
	for text, want := range cases {
		if got := Mention(text); got != want {
			t.Errorf("%q: got %q", text, got)
		}
	}
}

func TestMakeVariablesAndRules(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, text, target string
		hit                bool
	}{
		{"weak assignment keeps the first", "A = terraform\nA ?= echo\nx:\n\t$A destroy\n", "x", true},
		{"appended value", "A = terraform\nA += destroy\nx:\n\t$(A)\n", "x", true},
		{"braces", "A = terraform\nx:\n\t${A} destroy\n", "x", true},
		{"a cycle ends", "A = $(B)\nB = $(A)\nx:\n\t$(A) destroy\n", "x", false},
		{"doubling stops at the limit", "A = $(A)$(A)x\nx:\n\t$(A)\n", "x", false},
		{"target variable with spaces", "x: A = terraform\nx:\n\t$(A) destroy\n", "x", true},
		{"target variable stays with its target", "x: A = terraform\ny:\n\t$(A) destroy\n", "y", false},
		{"define with an equals sign", "define A =\nterraform destroy\nendef\nx:\n\t$(A)\n", "x", true},
		{"unterminated define defines nothing", "define A\nterraform destroy\nx:\n\t$(A)\n", "x", true},
		{"pattern with a suffix", "%.tf:\n\tterraform $*\n", "destroy.tf", true},
		{"pattern prerequisite", "go-%: run-%\nrun-%:\n\tterraform $*\n", "go-destroy", true},
		{"automatic in a prerequisite", "x: $@-y\nx-y:\n\tterraform destroy\n", "x", true},
		{"recipe ended by other text", "x:\n\techo a\nnot a rule\n\tterraform destroy\n", "x", false},
	}
	for _, c := range cases {
		write(t, dir, "Makefile", c.text)
		if got := Make(dir, "", []string{c.target}, nil, nil) != ""; got != c.hit {
			t.Errorf("%s: hit=%v", c.name, got)
		}
	}
}

func TestMakeEnvironmentAndCommandLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Makefile", "A := echo\nx:\n\t$(A) destroy\n")
	if Make(dir, "", []string{"x"}, map[string]string{"A": "terraform"}, nil) != "" {
		t.Error("a plain assignment replaces the environment")
	}
	if Make(dir, "", []string{"x"}, nil, map[string]string{"A": "terraform"}) == "" {
		t.Error("the command line outranks the file")
	}
	write(t, dir, "Makefile", "A ?= echo\nx:\n\t$(A) destroy\n")
	if Make(dir, "", []string{"x"}, map[string]string{"A": "terraform"}, nil) == "" {
		t.Error("?= keeps the environment")
	}
}

func TestMakeReadsIncludes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "one.mk", "include two.mk\nA = terraform\n")
	write(t, dir, "two.mk", "nuke:\n\t$(A) destroy\nlint:\n\tgo vet\n")
	for text, want := range map[string]bool{
		"include one.mk\nall:\n\tgo test\n":       true,
		"-include one.mk absent.mk\nall:\n\tls\n": true,
		"sinclude one.mk\nall:\n\tls\n":           true,
		"include $(F)\nall:\n\tls\n":              false,
		"include one.mk one.mk\nall:\n\tls\n":     true,
	} {
		write(t, dir, "Makefile", text)
		if got := Make(dir, "", []string{"nuke"}, nil, nil) != ""; got != want {
			t.Errorf("%q: hit=%v", text, got)
		}
	}
	write(t, dir, "Makefile", "include one.mk\nall:\n\tls\n")
	if m := Make(dir, "", []string{"lint"}, nil, nil); m != "" {
		t.Errorf("a target found in an include needs no whole-file search: %q", m)
	}
}

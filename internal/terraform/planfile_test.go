package terraform

import (
	"archive/zip"
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/verdict"
)

// managed drops data sources, which the verdict skips: in a destroy plan the
// JSON leaves them out while the saved plan lists them as deletes.
func managed(f planFacts) planFacts {
	f.Changes = slices.DeleteFunc(slices.Clone(f.Changes), func(c plannedChange) bool { return c.Data })
	slices.SortFunc(f.Changes, func(a, b plannedChange) int {
		return strings.Compare(a.Address+fmtBool(a.Deposed), b.Address+fmtBool(b.Deposed))
	})
	return f
}

func fmtBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Plans written by real terraform must decode to what terraform show -json says.
// Regenerate them with testdata/plans/generate.sh.
func TestSavedPlansMatchTerraformShow(t *testing.T) {
	paths, err := filepath.Glob("testdata/plans/*.tfplan")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".tfplan")
		got, problem := readPlanFile(path, testGuards)
		if problem != "" {
			t.Errorf("%s: %s", name, problem)
			continue
		}
		raw, err := os.ReadFile(strings.TrimSuffix(path, ".tfplan") + ".json")
		if err != nil {
			t.Fatal(err)
		}
		want, problem := factsFromJSON(raw, testGuards)
		if problem != "" {
			t.Fatalf("%s: %s", name, problem)
		}
		if g, w := managed(got), managed(want); !reflect.DeepEqual(g, w) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, g, w)
		}
	}
}

func TestSavedPlanFixturesCoverEveryShape(t *testing.T) {
	read := func(name string) planFacts {
		f, problem := readPlanFile("testdata/plans/"+name+".tfplan", nil)
		if problem != "" {
			t.Fatalf("%s: %s", name, problem)
		}
		return f
	}
	actions := map[string]bool{}
	for _, name := range []string{"changes", "destroy", "forget", "deposed-targeted", "provisioner"} {
		for _, c := range read(name).Changes {
			actions[strings.Join(c.Actions, ",")] = true
		}
	}
	for _, want := range []string{"no-op", "create", "read", "update", "delete", "delete,create", "create,delete", "forget"} {
		if !actions[want] {
			t.Errorf("no fixture has actions %s", want)
		}
	}
	if f := read("errored"); !f.Errored || !f.Partial {
		t.Errorf("errored: %+v", f)
	}
	if f := read("deposed-targeted"); !f.Partial {
		t.Errorf("targeted plan is not partial: %+v", f)
	}
	if f := read("action"); f.ProviderActions != 1 {
		t.Errorf("action: %+v", f)
	}
	if f := read("provisioner"); !f.Provisioners {
		t.Errorf("provisioner: %+v", f)
	}
	if v := judgeFacts(read("rich"), nil); v.Decision != verdict.None {
		t.Errorf("drift, moved, import, checks and outputs changed the verdict: %v %v", v.Decision, v.Reasons)
	}
	guards, problem := readPlanFile("testdata/plans/guards.tfplan", testGuards)
	if problem != "" {
		t.Fatal(problem)
	}
	v := judgeFacts(guards, nil)
	want := []string{
		"aws_s3_bucket.weakened sets force_destroy to true, so a later destroy can lose data it would have kept",
		"aws_s3_bucket.unknown may set force_destroy to true when applied, so a later destroy can lose data it would have kept",
		"aws_db_instance.unprotected sets deletion_protection to false, so a later destroy can lose data it would have kept",
	}
	if v.Decision != verdict.Ask || !slices.Equal(slices.Sorted(slices.Values(v.Reasons)), slices.Sorted(slices.Values(want))) {
		t.Errorf("guards: %v %q", v.Decision, v.Reasons)
	}
	if v := judgeFacts(read("no-change"), nil); v.Decision != verdict.None {
		t.Errorf("no-change: %v %v", v.Decision, v.Reasons)
	}
	if v := judgeFacts(read("destroy"), map[string]bool{"terraform_data": true}); v.Decision != verdict.Deny {
		t.Errorf("destroy: %v %v", v.Decision, v.Reasons)
	}
}

func TestAnythingNotUnderstoodIsAProblem(t *testing.T) {
	change := func(addr string, action uint64) testPlan {
		return testPlan{changes: []testChange{{address: addr, action: action}}}
	}
	cases := map[string]struct {
		plan testPlan
		want string
	}{
		"unknown top-level field": {testPlan{extra: pbBytes(31, []byte("x"))}, "field 31"},
		"future version":          {testPlan{version: 4}, "format 4"},
		"unknown action":          {change("a.b", 9), "action 9"},
		"unreadable address":      {change("ephemeral.a.b", actDelete), "could not read"},
		"empty address":           {change("", actDelete), "could not read"},
		"unknown mode":            {testPlan{extra: pbVarint(fieldMode, 3)}, "mode 3"},
		"version as bytes":        {testPlan{version: planVersion, extra: pbBytes(fieldVersion, []byte("3"))}, "encoded"},
		"truncated":               {testPlan{extra: []byte{0x1a, 0x05, 0x01}}, "encoded"},
		"group wire type":         {testPlan{extra: []byte{0x1b}}, "encoded"},
		"field zero":              {testPlan{extra: []byte{0x00, 0x00}}, "encoded"},
		"short fixed64":           {testPlan{extra: []byte{0x11, 0x01}}, "encoded"},
		"bad varint":              {testPlan{extra: []byte{0x08, 0xff}}, "encoded"},
		"bad key":                 {testPlan{extra: []byte{0xff}}, "encoded"},
		"no tfplan":               {testPlan{noTfplan: true}, "no tfplan"},
		"change with no body":     {testPlan{extra: pbBytes(fieldResourceChange, pbBytes(changeAddress, []byte("a.b")))}, "without a change"},
		"change body not message": {testPlan{extra: pbBytes(fieldResourceChange, append(pbBytes(changeAddress, []byte("a.b")), pbVarint(changeBody, 1)...))}, "encoded"},
		"action as bytes":         {testPlan{extra: pbBytes(fieldResourceChange, append(pbBytes(changeAddress, []byte("a.b")), pbBytes(changeBody, pbBytes(bodyAction, nil))...))}, "encoded"},
		"actions as varint":       {testPlan{extra: pbVarint(fieldActionInvocations, 1)}, "encoded"},
		"errored as bytes":        {testPlan{extra: pbBytes(fieldErrored, nil)}, "encoded"},
		"complete as bytes":       {testPlan{extra: pbBytes(fieldComplete, nil)}, "encoded"},
		"address as varint":       {testPlan{extra: pbBytes(fieldResourceChange, pbVarint(changeAddress, 1))}, "encoded"},
		"changes as varint":       {testPlan{extra: pbVarint(fieldResourceChange, 1)}, "encoded"},
		"unknown fixed32 field":   {testPlan{extra: []byte{0xfd, 0x01, 0, 0, 0, 0}}, "field 31"},
	}
	for name, c := range cases {
		path := writePlan(t, t.TempDir(), "p", c.plan)
		if _, problem := readPlanFile(path, nil); !strings.Contains(problem, c.want) {
			t.Errorf("%s: problem %q, want %q", name, problem, c.want)
		}
	}
}

func TestDecodedFacts(t *testing.T) {
	p := testPlan{
		changes: []testChange{
			{address: "module.a[0].module.b[\"k\"].aws_db_instance.m", action: actReplace},
			{address: "data.aws_db_instance.d", action: actNoOp},
			{address: "x.y", action: actDelete, deposed: "abcd"},
		},
		partial: true,
		errored: true,
		actions: 2,
		// A fixed64 field at a known number is skipped like any other.
		extra: append([]byte{0x29}, make([]byte, 8)...),
	}
	got, problem := readPlanFile(writePlan(t, t.TempDir(), "p", p), nil)
	if problem != "" {
		t.Fatal(problem)
	}
	want := planFacts{
		Errored: true, Partial: true, ProviderActions: 2,
		Changes: []plannedChange{
			{Address: "module.a[0].module.b[\"k\"].aws_db_instance.m", Type: "aws_db_instance", Actions: []string{"delete", "create"}},
			{Address: "data.aws_db_instance.d", Type: "aws_db_instance", Data: true, Actions: []string{"no-op"}},
			{Address: "x.y", Type: "x", Deposed: true, Actions: []string{"delete"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %+v\nwant %+v", got, want)
	}
}

func TestProvisionersAreFoundInTheConfigurationText(t *testing.T) {
	for name, config := range map[string]map[string]string{
		"block":   {"tfconfig/m-/main.tf": "resource \"x\" \"y\" {\n  provisioner \"local-exec\" {}\n}\n"},
		"json":    {"tfconfig/m-/main.tf.json": `{"resource":{"x":{"y":{"provisioner":[]}}}}`},
		"module":  {"tfconfig/m-/main.tf": "", "tfconfig/m-net/main.tf": "provisioner \"remote-exec\" {}"},
		"comment": {"tfconfig/m-/main.tf": "# provisioner \"local-exec\" was removed"},
		// terraform decodes the escape, so this is a real provisioner.
		"json escape": {"tfconfig/m-/main.tf.json": `{"resource":{"x":{"y":{"\u0070rovisioner":[]}}}}`},
		"broken json": {"tfconfig/m-/main.tf.json": `{"resource":`},
	} {
		got, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{config: config}), nil)
		if problem != "" || !got.Provisioners {
			t.Errorf("%s: %+v %q", name, got, problem)
		}
	}
	got, _ := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{config: map[string]string{"tfconfig/m-/main.tf": "resource \"x\" \"y\" {}"}}), nil)
	if got.Provisioners {
		t.Error("found a provisioner in a configuration without one")
	}
	// Outside tfconfig/ the text is state or a lock file, not configuration.
	got, _ = readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{config: map[string]string{"tfstate": "provisioner"}}), nil)
	if got.Provisioners {
		t.Error("searched outside tfconfig/")
	}
}

func TestPlanFilesThatCannotBeReadAsPlans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "text"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	var twice bytes.Buffer
	w := zip.NewWriter(&twice)
	for range 2 {
		f, _ := w.Create("tfplan")
		_, _ = f.Write(testPlan{}.tfplan())
	}
	_ = w.Close()
	if err := os.WriteFile(filepath.Join(dir, "twice"), twice.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "unreadable")
	writePlan(t, dir, "unreadable", fatalPlan)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing": "not found", "text": "not a zip", "twice": "more than one",
		".": "directory",
	}
	if os.Geteuid() != 0 {
		cases["unreadable"] = "could not be opened"
	}
	cases["text/x"] = "could not be inspected"
	for name, want := range cases {
		if _, problem := readPlanFile(filepath.Join(dir, name), nil); !strings.Contains(problem, want) {
			t.Errorf("%s: problem %q, want %q", name, problem, want)
		}
	}
}

// zipWith writes stored entries and marks those named in broken with a
// compression method no reader knows.
func zipWith(t *testing.T, entries map[string][]byte, broken ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(entries[name])
	}
	_ = w.Close()
	raw := buf.Bytes()
	for _, name := range broken {
		for _, sig := range []struct {
			magic  string
			method int
			name   int
		}{{"PK\x03\x04", 8, 30}, {"PK\x01\x02", 10, 46}} {
			for i := 0; i+sig.name+len(name) <= len(raw); i++ {
				if string(raw[i:i+4]) == sig.magic && string(raw[i+sig.name:i+sig.name+len(name)]) == name {
					raw[i+sig.method] = 99
				}
			}
		}
	}
	return raw
}

func TestEntriesAreReadWithALimit(t *testing.T) {
	raw := zipWith(t, map[string][]byte{"a": bytes.Repeat([]byte{0}, 100), "b": []byte("x")}, "b")
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readEntry(z.File[0], 99); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("over the limit: %v", err)
	}
	if data, err := readEntry(z.File[0], 100); err != nil || len(data) != 100 {
		t.Errorf("at the limit: %d %v", len(data), err)
	}
	if _, err := readEntry(z.File[1], 100); err == nil {
		t.Error("read an entry with an unknown compression method")
	}
}

func TestEntriesThatCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	write := func(raw []byte) string {
		path := filepath.Join(dir, "p")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tfplan := testPlan{}.tfplan()
	path := write(zipWith(t, map[string][]byte{"tfplan": tfplan, "tfconfig/m-/main.tf": []byte("x")}, "tfconfig/m-/main.tf"))
	if _, problem := readPlanFile(path, nil); !strings.Contains(problem, "configuration could not be searched") {
		t.Errorf("config: %q", problem)
	}
	path = write(zipWith(t, map[string][]byte{"tfplan": tfplan}, "tfplan"))
	if _, problem := readPlanFile(path, nil); !strings.Contains(problem, "tfplan entry could not be read") {
		t.Errorf("tfplan: %q", problem)
	}
}

func TestParseAddress(t *testing.T) {
	cases := map[string]struct {
		typ  string
		data bool
	}{
		"aws_db_instance.main":                    {"aws_db_instance", false},
		"aws_db_instance.main[0]":                 {"aws_db_instance", false},
		`aws_db_instance.main["a.b"]`:             {"aws_db_instance", false},
		`aws_db_instance.main["c\"d] e"]`:         {"aws_db_instance", false},
		`aws_db_instance.main["back\\"]`:          {"aws_db_instance", false},
		"data.aws_ami.x":                          {"aws_ami", true},
		"module.db.aws_db_instance.main":          {"aws_db_instance", false},
		`module.a["x"].module.b[2].data.t.n["k"]`: {"t", true},
		"module.data.x.y":                         {"x", false},
		"x.data":                                  {"x", false},
		"data_thing.y":                            {"data_thing", false},
		"my-type_2.name-3":                        {"my-type_2", false},
		"module.my-mod.a.b":                       {"a", false},
	}
	for addr, want := range cases {
		typ, data, ok := parseAddress(addr)
		if !ok || typ != want.typ || data != want.data {
			t.Errorf("%s: %q %v %v", addr, typ, data, ok)
		}
	}
	for _, addr := range []string{
		"", "a", "a.", ".b", "a.b.c", "ephemeral.a.b", "a.b[", "a.b[]", "a.b[x]", `a.b["x]`, `a.b["x"`, "a.b[0]x",
		"module.a", "module.a.", "module.a[0]", "module.a[0]b.c.d", "module..a.b", "1a.b", "a.1b", "data.a", "a.b[0][1]",
		`a.b["x\`, "module.a[].b.c",
	} {
		if typ, _, ok := parseAddress(addr); ok {
			t.Errorf("%q parsed as type %q", addr, typ)
		}
	}
}

func TestSecretsInAPlanNeverReachTheReason(t *testing.T) {
	p := testPlan{changes: []testChange{{address: "aws_db_instance.main", action: actDelete}}}
	p.extra = pbBytes(2, pbBytes(1, []byte("hunter2")))
	got, problem := readPlanFile(writePlan(t, t.TempDir(), "p", p), nil)
	if problem != "" {
		t.Fatal(problem)
	}
	if m := judgeFacts(got, stateful).Message(); strings.Contains(m, "hunter2") || strings.Contains(m, "values") {
		t.Fatalf("leaked: %s", m)
	}
}

func TestTooManyChangesIsAProblem(t *testing.T) {
	p := testPlan{changes: make([]testChange, maxChanges+1)}
	for i := range p.changes {
		p.changes[i] = testChange{address: "a.b", action: actNoOp}
	}
	if _, problem := readPlanFile(writePlan(t, t.TempDir(), "p", p), nil); !strings.Contains(problem, "more than") {
		t.Fatalf("problem %q", problem)
	}
	p.changes = p.changes[:maxChanges]
	if _, problem := readPlanFile(writePlan(t, t.TempDir(), "p", p), nil); problem != "" {
		t.Fatalf("at the cap: %q", problem)
	}
}

// Repeated fields are read the way terraform reads them, checked by rewriting a
// real plan and running terraform show on it: the last scalar wins, and a second
// body without an action keeps the first one's.
func TestRepeatedFieldsMatchTerraform(t *testing.T) {
	change := func(bodies ...[]byte) []byte {
		rc := pbBytes(changeAddress, []byte("x.y"))
		for _, b := range bodies {
			rc = append(rc, pbBytes(changeBody, b)...)
		}
		return pbBytes(fieldResourceChange, rc)
	}
	cases := map[string]struct {
		extra   []byte
		actions []string
		partial bool
	}{
		"second body is a no-op":   {change(pbVarint(bodyAction, actDelete), pbVarint(bodyAction, actNoOp)), []string{"no-op"}, false},
		"second body is empty":     {change(pbVarint(bodyAction, actDelete), nil), []string{"delete"}, false},
		"complete then incomplete": {append(change(pbVarint(bodyAction, actDelete)), pbVarint(fieldComplete, 0)...), []string{"delete"}, true},
	}
	for name, c := range cases {
		got, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{extra: c.extra}), nil)
		if problem != "" || len(got.Changes) != 1 || !slices.Equal(got.Changes[0].Actions, c.actions) || got.Partial != c.partial {
			t.Errorf("%s: %+v %q", name, got, problem)
		}
	}
}

func FuzzFactsFromProto(f *testing.F) {
	f.Add(testPlan{}.tfplan())
	f.Add(testPlan{changes: []testChange{{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpTrue)}}}}.tfplan())
	f.Add(fatalPlan.tfplan())
	f.Add(testPlan{changes: []testChange{{address: `module.a["k"].data.t.n[0]`, action: actReplace, deposed: "d"}}, actions: 1, errored: true}.tfplan())
	f.Fuzz(func(t *testing.T, raw []byte) {
		facts, problem := factsFromProto(raw, testGuards)
		if problem != "" && !reflect.DeepEqual(facts, planFacts{}) {
			t.Fatalf("facts returned with a problem: %+v", facts)
		}
	})
}

func FuzzParseAddress(f *testing.F) {
	for _, s := range []string{"a.b", `module.a["x"].data.t.n[0]`, `a.b["c\\"d] e"]`, "ephemeral.a.b", "a.b[0][1]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, addr string) {
		if typ, _, ok := parseAddress(addr); ok && (typ == "" || !strings.Contains(addr, typ+".")) {
			t.Fatalf("%q parsed as type %q", addr, typ)
		}
	})
}

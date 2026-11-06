package terraform

import (
	"bytes"
	"strings"
	"testing"

	"github.com/somaz94/whatdies/internal/verdict"
)

var testGuards = map[string]map[string]bool{
	"aws_s3_bucket":   {"force_destroy": true},
	"aws_db_instance": {"deletion_protection": false, "skip_final_snapshot": true},
}

var (
	mpTrue    = []byte{0xc3}
	mpFalse   = []byte{0xc2}
	mpNil     = []byte{0xc0}
	mpUnknown = []byte{0xd4, 0x00, 0x00}
)

func mpStr(s string) []byte {
	if len(s) < 32 {
		return append([]byte{0xa0 | byte(len(s))}, s...)
	}
	return append([]byte{0xd9, byte(len(s))}, s...)
}

// mpMap encodes alternating key and value pairs as a msgpack map.
func mpMap(kv ...[]byte) []byte {
	out := []byte{0xde, 0, byte(len(kv) / 2)}
	for _, b := range kv {
		out = append(out, b...)
	}
	return out
}

func bucket(force []byte) []byte {
	return mpMap(mpStr("bucket"), mpStr("example"), mpStr("force_destroy"), force, mpStr("tags"), mpMap(mpStr("k"), mpStr("hunter2")))
}

func dbValues(protection []byte) []byte {
	return mpMap(mpStr("deletion_protection"), protection, mpStr("skip_final_snapshot"), mpFalse)
}

func guardVerdict(t *testing.T, c testChange) (verdict.Verdict, string) {
	t.Helper()
	facts, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{changes: []testChange{c}}), testGuards)
	if problem != "" {
		return verdict.Verdict{}, problem
	}
	return judgeFacts(facts, stateful), ""
}

func TestUpdatesThatTurnAGuardOffAreAsk(t *testing.T) {
	cases := map[string]struct {
		change testChange
		want   string
	}{
		"false to true": {testChange{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpTrue)}}, "sets force_destroy to true"},
		"null to true":  {testChange{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpNil), bucket(mpTrue)}}, "sets force_destroy to true"},
		"unknown":       {testChange{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpUnknown)}}, "may set force_destroy to true"},
		"unset":         {testChange{address: "aws_db_instance.m", action: actUpdate, values: [][]byte{dbValues(mpTrue), mpMap(mpStr("skip_final_snapshot"), mpFalse)}}, "unsets deletion_protection"},
		"set to null":   {testChange{address: "aws_db_instance.m", action: actUpdate, values: [][]byte{dbValues(mpTrue), dbValues(mpNil)}}, "unsets deletion_protection"},
		"off is false":  {testChange{address: "module.db.aws_db_instance.m", action: actUpdate, values: [][]byte{dbValues(mpTrue), dbValues(mpFalse)}}, "module.db.aws_db_instance.m sets deletion_protection to false"},
	}
	for name, c := range cases {
		v, problem := guardVerdict(t, c.change)
		if problem != "" || v.Decision != verdict.Ask || !strings.Contains(strings.Join(v.Reasons, " "), c.want) {
			t.Errorf("%s: %v %v %q", name, v.Decision, v.Reasons, problem)
		}
		if strings.Contains(v.Message(), "hunter2") || strings.Contains(v.Message(), "example") {
			t.Errorf("%s: a value reached the message: %s", name, v.Message())
		}
	}
}

func TestGuardsLeftOnOrAlreadyOffDecideNothing(t *testing.T) {
	for name, c := range map[string]testChange{
		"stays true":       {address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpTrue), bucket(mpTrue)}},
		"turned back on":   {address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpTrue), bucket(mpFalse)}},
		"unknown when off": {address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpTrue), bucket(mpUnknown)}},
		"unset when off":   {address: "aws_db_instance.m", action: actUpdate, values: [][]byte{dbValues(mpFalse), mpMap(mpStr("skip_final_snapshot"), mpFalse)}},
		// Unset means the default false, which keeps force_destroy's guard on.
		"unset when off is true": {address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpNil)}},
		"created off":            {address: "aws_s3_bucket.b", action: actCreate, values: [][]byte{bucket(mpTrue)}},
		"type without guards":    {address: "aws_other.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpTrue)}},
		"data source":            {address: "data.aws_s3_bucket.b", action: actUpdate, values: [][]byte{[]byte("not read")}},
		"no-op is not opened":    {address: "aws_s3_bucket.b", action: actNoOp, values: [][]byte{[]byte("not read")}},
		"other keys any shape":   {address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{everyType(), everyType()}},
	} {
		if v, problem := guardVerdict(t, c); problem != "" || v.Decision != verdict.None {
			t.Errorf("%s: %v %v %q", name, v.Decision, v.Reasons, problem)
		}
	}
}

// everyType holds one value of each msgpack type outside the guard keys.
func everyType() []byte {
	var big bytes.Buffer
	big.WriteString(strings.Repeat("x", 300))
	values := [][]byte{
		{0x05}, {0xe0}, {0xcc, 1}, {0xcd, 0, 1}, {0xce, 0, 0, 0, 1}, {0xcf, 0, 0, 0, 0, 0, 0, 0, 1},
		{0xd0, 1}, {0xd1, 0, 1}, {0xd2, 0, 0, 0, 1}, {0xd3, 0, 0, 0, 0, 0, 0, 0, 1},
		{0xca, 0, 0, 0, 0}, {0xcb, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xc4, 1, 'b'}, {0xc5, 0, 1, 'b'}, {0xc6, 0, 0, 0, 1, 'b'},
		{0xda, 0, 1, 's'}, {0xdb, 0, 0, 0, 1, 's'}, append([]byte{0xda, 1, 44}, big.Bytes()...),
		{0xd4, 0, 0}, {0xd5, 0, 0, 0}, {0xd6, 0, 0, 0, 0, 0}, {0xd7, 1, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xd8, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{0xc7, 1, 0, 0}, {0xc8, 0, 1, 0, 0}, {0xc9, 0, 0, 0, 1, 0, 0},
		{0x92, 0xc0, 0xc3}, {0xdc, 0, 1, 0xc2}, {0xdd, 0, 0, 0, 1, 0xc2},
		{0x81, 0xa1, 'k', 0xc0}, {0xdf, 0, 0, 0, 1, 0xa1, 'k', 0xc0},
	}
	kv := [][]byte{mpStr("force_destroy"), mpFalse}
	for i, v := range values {
		kv = append(kv, mpStr(strings.Repeat("k", i+1)), v)
	}
	return mpMap(kv...)
}

func TestGuardValuesThatCannotBeReadAreAProblem(t *testing.T) {
	update := func(values ...[]byte) testChange {
		return testChange{address: "aws_s3_bucket.b", action: actUpdate, values: values}
	}
	ok := bucket(mpFalse)
	cases := map[string]struct {
		change testChange
		want   string
	}{
		"one value":         {update(ok), "1 values"},
		"three values":      {update(ok, ok, ok), "3 values"},
		"string guard":      {update(ok, bucket(mpStr("true"))), "not a boolean"},
		"array guard":       {update(bucket([]byte{0x91, 0xc3}), ok), "not a boolean"},
		"not a map":         {update(mpTrue, ok), "not encoded"},
		"key not a string":  {update([]byte{0x81, 0x01, 0xc3}, ok), "not encoded"},
		"truncated value":   {update(mpMap(mpStr("tags"), []byte{0xda, 0, 9, 'x'}), ok), "not encoded"},
		"trailing bytes":    {update(append(ok, 0xc0), ok), "not encoded"},
		"count too large":   {update([]byte{0xdf, 0xff, 0xff, 0xff, 0xff}, ok), "not encoded"},
		"duplicate guard":   {update(mpMap(mpStr("force_destroy"), mpFalse, mpStr("force_destroy"), mpTrue), ok), "not encoded"},
		"reserved code":     {update(mpMap(mpStr("tags"), []byte{0xc1}), ok), "not encoded"},
		"guard missing":     {update(mpMap(mpStr("force_destroy")), ok), "not encoded"},
		"deep nesting":      {update(mpMap(mpStr("tags"), append(bytes.Repeat([]byte{0x91}, maxMsgpackDepth+2), 0xc0)), ok), "not encoded"},
		"array too long":    {update(mpMap(mpStr("tags"), []byte{0xdd, 0xff, 0xff, 0xff, 0xff}), ok), "not encoded"},
		"short header":      {update([]byte{0xde, 0}, ok), "not encoded"},
		"empty":             {update([]byte{}, ok), "not encoded"},
		"short key length":  {update([]byte{0x81, 0xd9}, ok), "not encoded"},
		"str16 key":         {update([]byte{0x81, 0xda, 0, 1}, ok), "not encoded"},
		"short array count": {update(mpMap(mpStr("tags"), []byte{0xdc, 0}), ok), "not encoded"},
		"short map count":   {update(mpMap(mpStr("tags"), []byte{0xde}), ok), "not encoded"},
		"short ext length":  {update(mpMap(mpStr("tags"), []byte{0xc7}), ok), "not encoded"},
		"after unreadable":  {update(ok, mpTrue), "not encoded"},
		"guard at the end":  {update(append([]byte{0x81}, mpStr("force_destroy")...), ok), "not encoded"},
		"short str32 key":   {update([]byte{0x81, 0xdb, 0, 0}, ok), "not encoded"},
		"short fixed int":   {update(mpMap(mpStr("tags"), []byte{0xcf, 0}), ok), "not encoded"},
		"short array32":     {update(mpMap(mpStr("tags"), []byte{0xdd, 0}), ok), "not encoded"},
		"short map32":       {update(mpMap(mpStr("tags"), []byte{0xdf, 0}), ok), "not encoded"},
		"guard in neither":  {update(mpMap(mpStr("force_destroX"), mpFalse), mpMap(mpStr("force_destroX"), mpTrue)), "no force_destroy attribute"},
		"binary key":        {update(append([]byte{0x81, 0xc4, 13}, append([]byte("force_destroy"), 0xc3)...), ok), "not encoded"},
		"nil key":           {update([]byte{0x82, 0xc0, 0xc3, 0xad, 'f', 'o', 'r', 'c', 'e', '_', 'd', 'e', 's', 't', 'r', 'o', 'y', 0xc2}, ok), "not encoded"},
	}
	for name, c := range cases {
		if _, problem := guardVerdict(t, c.change); !strings.Contains(problem, c.want) || !strings.Contains(problem, "aws_s3_bucket.b") {
			t.Errorf("%s: problem %q, want %q", name, problem, c.want)
		}
	}
}

func TestGuardValuesMustBeDynamicValues(t *testing.T) {
	body := append(pbVarint(bodyAction, actUpdate), pbBytes(bodyValues, pbBytes(5, nil))...)
	body = append(body, pbBytes(bodyValues, pbBytes(5, nil))...)
	rc := append(pbBytes(changeAddress, []byte("aws_s3_bucket.b")), pbBytes(changeBody, body)...)
	if _, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{extra: pbBytes(fieldResourceChange, rc)}), testGuards); !strings.Contains(problem, "not in a form") {
		t.Errorf("problem %q", problem)
	}
	body = append(pbVarint(bodyAction, actUpdate), pbBytes(bodyValues, pbVarint(dynamicMsgpack, 1))...)
	body = append(body, pbBytes(bodyValues, pbVarint(dynamicMsgpack, 1))...)
	rc = append(pbBytes(changeAddress, []byte("aws_s3_bucket.b")), pbBytes(changeBody, body)...)
	if _, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{extra: pbBytes(fieldResourceChange, rc)}), testGuards); !strings.Contains(problem, "encoded") {
		t.Errorf("problem %q", problem)
	}
	body = append(pbVarint(bodyAction, actUpdate), pbVarint(bodyValues, 1)...)
	rc = append(pbBytes(changeAddress, []byte("aws_s3_bucket.b")), pbBytes(changeBody, body)...)
	if _, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{extra: pbBytes(fieldResourceChange, rc)}), testGuards); !strings.Contains(problem, "encoded") {
		t.Errorf("problem %q", problem)
	}
}

func TestDenyStillListsWeakenedGuards(t *testing.T) {
	p := testPlan{changes: []testChange{
		{address: "aws_db_instance.main", action: actDelete},
		{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(mpTrue)}},
	}}
	facts, problem := readPlanFile(writePlan(t, t.TempDir(), "p", p), testGuards)
	if problem != "" {
		t.Fatal(problem)
	}
	v := judgeFacts(facts, stateful)
	if v.Decision != verdict.Deny || !strings.Contains(strings.Join(v.Reasons, " "), "sets force_destroy") {
		t.Fatalf("%v %v", v.Decision, v.Reasons)
	}
}

func FuzzGuardValues(f *testing.F) {
	f.Add(bucket(mpTrue))
	f.Add(everyType())
	f.Add(dbValues(mpUnknown))
	f.Fuzz(func(t *testing.T, packed []byte) {
		dv := pbBytes(dynamicMsgpack, packed)
		if got, err := guardValues(dv, testGuards["aws_s3_bucket"]); err == nil && len(got) > 1 {
			t.Fatalf("read more than the guard keys: %v", got)
		}
	})
}

// cty marks a refined unknown with another extension type than a plain one.
func TestEveryExtensionTypeIsUnknown(t *testing.T) {
	for _, ext := range [][]byte{{0xd4, 0x00, 0x00}, {0xd4, 0x0c, 0x00}, {0xc7, 2, 0x0c, 0, 0}} {
		v, problem := guardVerdict(t, testChange{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), bucket(ext)}})
		if problem != "" || v.Decision != verdict.Ask || !strings.Contains(v.Reasons[0], "may set") {
			t.Errorf("% x: %v %v %q", ext, v.Decision, v.Reasons, problem)
		}
	}
}

// Protobuf concatenates a repeated field across two copies of its message, and
// terraform reads values split over two bodies that way too.
func TestValuesSplitAcrossBodiesAreJoined(t *testing.T) {
	first := append(pbVarint(bodyAction, actUpdate), pbBytes(bodyValues, pbBytes(dynamicMsgpack, bucket(mpFalse)))...)
	second := pbBytes(bodyValues, pbBytes(dynamicMsgpack, bucket(mpTrue)))
	rc := append(pbBytes(changeAddress, []byte("aws_s3_bucket.b")), pbBytes(changeBody, first)...)
	rc = append(rc, pbBytes(changeBody, second)...)
	facts, problem := readPlanFile(writePlan(t, t.TempDir(), "p", testPlan{extra: pbBytes(fieldResourceChange, rc)}), testGuards)
	if problem != "" || len(facts.Changes) != 1 || len(facts.Changes[0].Weakens) != 1 {
		t.Fatalf("%+v %q", facts, problem)
	}
}

func TestKeysInEveryStringEncodingAreRead(t *testing.T) {
	key := "force_destroy"
	for name, encoded := range map[string][]byte{
		"str8":  append([]byte{0xd9, byte(len(key))}, key...),
		"str16": append([]byte{0xda, 0, byte(len(key))}, key...),
		"str32": append([]byte{0xdb, 0, 0, 0, byte(len(key))}, key...),
	} {
		after := append([]byte{0x81}, append(encoded, 0xc3)...)
		v, problem := guardVerdict(t, testChange{address: "aws_s3_bucket.b", action: actUpdate, values: [][]byte{bucket(mpFalse), after}})
		if problem != "" || v.Decision != verdict.Ask {
			t.Errorf("%s: %v %q", name, v.Decision, problem)
		}
	}
}

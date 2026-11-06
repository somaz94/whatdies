package terraform

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Wire values of a change's action, as observed in saved plans.
const (
	actNoOp    = 0
	actCreate  = 1
	actUpdate  = 3
	actDelete  = 5
	actReplace = 6
)

type testChange struct {
	address string
	action  uint64
	deposed string
	// Each is a msgpack document wrapped as a DynamicValue; nil means one empty map.
	values [][]byte
}

// testPlan builds a saved plan. The zero value is an empty, complete plan.
type testPlan struct {
	changes  []testChange
	partial  bool
	errored  bool
	actions  int
	version  uint64
	extra    []byte
	config   map[string]string
	noTfplan bool
}

func pbVarint(num, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, num<<3), v)
}

func pbBytes(num uint64, b []byte) []byte {
	out := binary.AppendUvarint(binary.AppendUvarint(nil, num<<3|2), uint64(len(b)))
	return append(out, b...)
}

func (p testPlan) tfplan() []byte {
	version := p.version
	if version == 0 {
		version = planVersion
	}
	out := pbVarint(fieldVersion, version)
	for _, c := range p.changes {
		rc := pbBytes(changeAddress, []byte(c.address))
		body := pbVarint(bodyAction, c.action)
		if c.values == nil {
			body = append(body, pbBytes(bodyValues, pbBytes(dynamicMsgpack, mpMap()))...)
		}
		for _, v := range c.values {
			body = append(body, pbBytes(bodyValues, pbBytes(dynamicMsgpack, v))...)
		}
		rc = append(rc, pbBytes(changeBody, body)...)
		if c.deposed != "" {
			rc = append(rc, pbBytes(changeDeposed, []byte(c.deposed))...)
		}
		out = append(out, pbBytes(fieldResourceChange, rc)...)
	}
	for range p.actions {
		out = append(out, pbBytes(fieldActionInvocations, pbBytes(1, []byte("action.x.y")))...)
	}
	if p.errored {
		out = append(out, pbVarint(fieldErrored, 1)...)
	}
	if !p.partial {
		out = append(out, pbVarint(fieldComplete, 1)...)
	}
	return append(out, p.extra...)
}

func (p testPlan) zip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if !p.noTfplan {
		add("tfplan", p.tfplan())
	}
	for _, name := range slices.Sorted(maps.Keys(p.config)) {
		add(name, []byte(p.config[name]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writePlan(t *testing.T, dir, name string, p testPlan) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, p.zip(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fatalPlan deletes a resource that stateful marks as holding data.
var fatalPlan = testPlan{changes: []testChange{{address: "aws_db_instance.main", action: actDelete}}}

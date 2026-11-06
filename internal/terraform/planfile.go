package terraform

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// A saved plan is a zip whose tfplan entry is protobuf. The field numbers below
// were observed in plans terraform wrote and checked against terraform show -json,
// not taken from terraform's source. Anything not listed is a reason to ask.
const (
	planVersion = 3

	fieldVersion           = 1
	fieldResourceChange    = 3
	fieldMode              = 17
	fieldErrored           = 20
	fieldComplete          = 26
	fieldActionInvocations = 30

	changeDeposed = 7
	changeBody    = 9
	changeAddress = 13
	bodyAction    = 1
)

// knownTopFields are the fields factsFromProto decodes, plus those seen to carry
// nothing an apply destroys: variables, outputs, targets, backend, drift, checks, timestamps.
var knownTopFields = map[uint64]bool{
	1: true, 2: true, 3: true, 4: true, 5: true, 13: true, 14: true, 15: true, 17: true,
	18: true, 19: true, 20: true, 21: true, 25: true, 26: true, 30: true,
}

var actionNames = map[uint64][]string{
	0: {"no-op"}, 1: {"create"}, 2: {"read"}, 3: {"update"}, 5: {"delete"},
	6: {"delete", "create"}, 7: {"create", "delete"}, 8: {"forget"},
}

// Limits keep a hostile zip from exhausting memory before the deadline. A change
// is a few bytes on the wire but far more once decoded, hence its own cap.
const (
	maxPlanEntry  = 128 << 20
	maxConfigRead = 64 << 20
	maxChanges    = 100_000
)

// readPlanFile decodes a saved plan without running terraform. A problem is
// returned instead of facts whenever the file is not fully understood.
func readPlanFile(path string) (planFacts, string) {
	f, size, problem := openRegular(path)
	if problem != "" {
		return planFacts{}, problem
	}
	defer func() { _ = f.Close() }()
	z, err := zip.NewReader(f, size)
	if err != nil {
		return planFacts{}, "it is not a saved plan (not a zip archive)"
	}
	var tfplan *zip.File
	configBudget := int64(maxConfigRead)
	provisioners := false
	for _, entry := range z.File {
		switch {
		case entry.Name == "tfplan":
			if tfplan != nil {
				return planFacts{}, "it holds more than one tfplan entry"
			}
			tfplan = entry
		case strings.HasPrefix(entry.Name, "tfconfig/") && !provisioners:
			text, err := readEntry(entry, configBudget)
			if err != nil {
				return planFacts{}, "its configuration could not be searched: " + err.Error()
			}
			configBudget -= int64(len(text))
			provisioners = mentionsProvisioner(entry.Name, text)
		}
	}
	if tfplan == nil {
		return planFacts{}, "it is not a saved plan (no tfplan entry)"
	}
	raw, err := readEntry(tfplan, maxPlanEntry)
	if err != nil {
		return planFacts{}, "its tfplan entry could not be read: " + err.Error()
	}
	facts, problem := factsFromProto(raw)
	facts.Provisioners = provisioners
	return facts, problem
}

// mentionsProvisioner searches configuration text. A comment that mentions one also
// counts, which only makes the verdict stricter. JSON can spell a key with \u
// escapes that terraform decodes, so a .json file is also searched token by token.
func mentionsProvisioner(name string, text []byte) bool {
	if bytes.Contains(text, []byte("provisioner")) {
		return true
	}
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	if !json.Valid(text) {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	for {
		tok, err := dec.Token()
		if err != nil {
			return err != io.EOF
		}
		if s, ok := tok.(string); ok && strings.Contains(s, "provisioner") {
			return true
		}
	}
}

func openRegular(path string) (*os.File, int64, string) {
	// Stat first, and open without blocking where the system allows: opening a FIFO
	// blocks until a writer appears, and the path can be swapped for one in between.
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, "the file was not found"
	case err != nil:
		return nil, 0, "it could not be inspected"
	case info.IsDir():
		return nil, 0, "it is a directory, not a plan file"
	case !info.Mode().IsRegular():
		return nil, 0, "it is not a regular file"
	}
	f, err := os.OpenFile(path, openFlags, 0)
	if err != nil {
		return nil, 0, "it could not be opened"
	}
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, 0, "it changed while being opened"
	}
	return f, info.Size(), ""
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("it is larger than whatdies will read")
	}
	return data, nil
}

func factsFromProto(raw []byte) (planFacts, string) {
	var facts planFacts
	version, complete := uint64(0), false
	err := eachField(raw, func(num uint64, f field) error {
		if !knownTopFields[num] {
			return fmt.Errorf("the plan holds field %d, which whatdies does not know, so it cannot rule out a destructive change", num)
		}
		var err error
		switch num {
		case fieldVersion:
			version, err = f.varint()
		case fieldMode:
			var mode uint64
			if mode, err = f.varint(); err == nil && mode > 2 {
				err = fmt.Errorf("the plan has mode %d, which whatdies does not know", mode)
			}
		case fieldErrored:
			var v uint64
			v, err = f.varint()
			facts.Errored = facts.Errored || v != 0
		case fieldComplete:
			var v uint64
			v, err = f.varint()
			complete = v != 0
		case fieldActionInvocations:
			_, err = f.bytes()
			facts.ProviderActions++
		case fieldResourceChange:
			if len(facts.Changes) >= maxChanges {
				return fmt.Errorf("the plan lists more than %d changes, more than whatdies will read", maxChanges)
			}
			var body []byte
			if body, err = f.bytes(); err == nil {
				var c plannedChange
				if c, err = decodeChange(body); err == nil {
					facts.Changes = append(facts.Changes, c)
				}
			}
		}
		return err
	})
	if err != nil {
		return planFacts{}, err.Error()
	}
	if version != planVersion {
		return planFacts{}, fmt.Sprintf("plan format %d is not one whatdies knows", version)
	}
	facts.Partial = !complete
	return facts, ""
}

func decodeChange(raw []byte) (plannedChange, error) {
	var c plannedChange
	var action uint64
	hasBody := false
	err := eachField(raw, func(num uint64, f field) error {
		var err error
		switch num {
		case changeAddress:
			c.Address, err = f.text()
		case changeDeposed:
			var key string
			key, err = f.text()
			c.Deposed = key != ""
		case changeBody:
			var body []byte
			if body, err = f.bytes(); err == nil {
				hasBody = true
				err = eachField(body, func(num uint64, f field) error {
					var err error
					if num == bodyAction {
						action, err = f.varint()
					}
					return err
				})
			}
		}
		return err
	})
	if err != nil {
		return c, err
	}
	typ, data, ok := parseAddress(c.Address)
	if !ok {
		return c, fmt.Errorf("the plan names a resource as %q, which whatdies could not read", c.Address)
	}
	if !hasBody {
		return c, fmt.Errorf("the plan lists %s without a change", c.Address)
	}
	actions, ok := actionNames[action]
	if !ok {
		return c, fmt.Errorf("%s has action %d, which whatdies does not know", c.Address, action)
	}
	c.Type, c.Data, c.Actions = typ, data, actions
	return c, nil
}

// field is one protobuf field as it sits on the wire.
type field struct {
	wire  uint64
	value uint64
	data  []byte
}

var errEncoding = errors.New("the plan is not encoded the way whatdies expects")

func (f field) varint() (uint64, error) {
	if f.wire != 0 {
		return 0, errEncoding
	}
	return f.value, nil
}

func (f field) bytes() ([]byte, error) {
	if f.wire != 2 {
		return nil, errEncoding
	}
	return f.data, nil
}

func (f field) text() (string, error) {
	b, err := f.bytes()
	return string(b), err
}

func eachField(buf []byte, visit func(num uint64, f field) error) error {
	for len(buf) > 0 {
		key, n := binary.Uvarint(buf)
		if n <= 0 {
			return errEncoding
		}
		buf = buf[n:]
		f := field{wire: key & 7}
		switch f.wire {
		case 0:
			if f.value, n = binary.Uvarint(buf); n <= 0 {
				return errEncoding
			}
			buf = buf[n:]
		case 1, 5:
			size := 8
			if f.wire == 5 {
				size = 4
			}
			if len(buf) < size {
				return errEncoding
			}
			buf = buf[size:]
		case 2:
			length, n := binary.Uvarint(buf)
			if n <= 0 || length > uint64(len(buf)-n) {
				return errEncoding
			}
			f.data, buf = buf[n:n+int(length)], buf[n+int(length):]
		default:
			return errEncoding
		}
		if num := key >> 3; num == 0 {
			return errEncoding
		} else if err := visit(num, f); err != nil {
			return err
		}
	}
	return nil
}

// parseAddress reads module.a["k"].data.aws_x.y[0] into its type and mode. Plans
// carry no separate type field, and anything it cannot read is a reason to ask.
func parseAddress(addr string) (typ string, data bool, ok bool) {
	rest := addr
	for {
		after, found := strings.CutPrefix(rest, "module.")
		if !found {
			break
		}
		if _, after, ok = identifier(after); !ok {
			return "", false, false
		}
		if after, ok = instanceKey(after); !ok || !strings.HasPrefix(after, ".") {
			return "", false, false
		}
		rest = after[1:]
	}
	rest, data = strings.CutPrefix(rest, "data.")
	typ, rest, ok = identifier(rest)
	if !ok || !strings.HasPrefix(rest, ".") {
		return "", false, false
	}
	if _, rest, ok = identifier(rest[1:]); !ok {
		return "", false, false
	}
	if rest, ok = instanceKey(rest); !ok || rest != "" {
		return "", false, false
	}
	return typ, data, true
}

func identifier(s string) (name, rest string, ok bool) {
	i := 0
	for i < len(s) {
		c := s[i]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || !(c == '-' || (c >= '0' && c <= '9'))) {
			break
		}
		i++
	}
	return s[:i], s[i:], i > 0
}

// instanceKey consumes an optional [0] or ["key"].
func instanceKey(s string) (string, bool) {
	if !strings.HasPrefix(s, "[") {
		return s, true
	}
	i := 1
	if strings.HasPrefix(s[i:], `"`) {
		for i++; i < len(s) && s[i] != '"'; i++ {
			if s[i] == '\\' {
				i++
			}
		}
		i++
	} else {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return "", false
		}
	}
	if i >= len(s) || s[i] != ']' {
		return "", false
	}
	return s[i+1:], true
}

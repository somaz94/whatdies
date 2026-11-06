package terraform

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// A change's values are before and after as cty msgpack. Plans hold secrets in
// clear text, so only guard keys are decoded and every other value is skipped by length.

const (
	actionUpdate    = 3
	dynamicMsgpack  = 1
	maxMsgpackDepth = 32
)

type guardKind int

const (
	guardAbsent guardKind = iota
	guardNull
	guardBool
	guardUnknown
	guardOther
)

type guardValue struct {
	kind guardKind
	b    bool
}

func (v guardValue) is(b bool) bool { return v.kind == guardBool && v.b == b }

// weakenedGuards compares before and after for each guard attribute of the type,
// where off is the value that turns the guard off.
func weakenedGuards(values [][]byte, attrs map[string]bool) ([]string, error) {
	if len(values) != 2 {
		return nil, fmt.Errorf("an update holds %d values instead of before and after", len(values))
	}
	before, err := guardValues(values[0], attrs)
	if err != nil {
		return nil, err
	}
	after, err := guardValues(values[1], attrs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		off, b, a := attrs[name], before[name], after[name]
		switch {
		case a.kind == guardOther || b.kind == guardOther:
			return nil, fmt.Errorf("%s is not a boolean in the plan", name)
		// A value lists every attribute, so a guard in neither means guards.yaml and
		// the provider disagree, which must not pass silently.
		case a.kind == guardAbsent && b.kind == guardAbsent:
			return nil, fmt.Errorf("the plan has no %s attribute, so guards.yaml may not match this provider", name)
		case a.kind == guardUnknown && !b.is(off):
			out = append(out, fmt.Sprintf("may set %s to %t when applied", name, off))
		case a.is(off) && !b.is(off):
			out = append(out, fmt.Sprintf("sets %s to %t", name, off))
		// Unset means the provider default, which is false: only a guard whose off
		// value is false is turned off by it.
		case !off && (a.kind == guardAbsent || a.kind == guardNull) && b.is(true):
			out = append(out, fmt.Sprintf("unsets %s", name))
		}
	}
	return out, nil
}

// guardValues reads the guard attributes out of one DynamicValue message.
func guardValues(dynamic []byte, attrs map[string]bool) (map[string]guardValue, error) {
	var packed []byte
	found := false
	err := eachField(dynamic, func(num uint64, f field) error {
		var err error
		if num == dynamicMsgpack {
			packed, err = f.bytes()
			found = true
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("a resource's values are not in a form whatdies reads")
	}
	r := msgpackReader{b: packed}
	n, err := r.mapHeader()
	if err != nil {
		return nil, err
	}
	out := map[string]guardValue{}
	for range n {
		key, err := r.str()
		if err != nil {
			return nil, err
		}
		if _, guarded := attrs[string(key)]; !guarded {
			if err := r.skip(0); err != nil {
				return nil, err
			}
			continue
		}
		if _, seen := out[string(key)]; seen {
			return nil, errMsgpack
		}
		v, err := r.guard()
		if err != nil {
			return nil, err
		}
		out[string(key)] = v
	}
	if r.i != len(r.b) {
		return nil, errMsgpack
	}
	return out, nil
}

var errMsgpack = errors.New("a resource's values are not encoded the way whatdies expects")

// msgpackReader steps through msgpack, checking every length against what is left.
type msgpackReader struct {
	b []byte
	i int
}

func (r *msgpackReader) take(n uint64) ([]byte, error) {
	if n > uint64(len(r.b)-r.i) {
		return nil, errMsgpack
	}
	out := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return out, nil
}

func (r *msgpackReader) bigEndian(size int) (uint64, error) {
	b, err := r.take(uint64(size))
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (r *msgpackReader) code() (byte, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *msgpackReader) mapHeader() (uint64, error) {
	c, err := r.code()
	if err != nil {
		return 0, err
	}
	var n uint64
	switch {
	case c >= 0x80 && c <= 0x8f:
		n = uint64(c & 0x0f)
	case c == 0xde:
		n, err = r.bigEndian(2)
	case c == 0xdf:
		n, err = r.bigEndian(4)
	default:
		return 0, errMsgpack
	}
	// Each entry takes at least two bytes, so a larger count cannot be real.
	if err != nil || n > uint64(len(r.b)-r.i)/2 {
		return 0, errMsgpack
	}
	return n, nil
}

func (r *msgpackReader) str() ([]byte, error) {
	c, err := r.code()
	if err != nil {
		return nil, err
	}
	var n uint64
	switch {
	case c >= 0xa0 && c <= 0xbf:
		n = uint64(c & 0x1f)
	case c == 0xd9:
		n, err = r.bigEndian(1)
	case c == 0xda:
		n, err = r.bigEndian(2)
	case c == 0xdb:
		n, err = r.bigEndian(4)
	default:
		return nil, errMsgpack
	}
	if err != nil {
		return nil, err
	}
	return r.take(n)
}

func (r *msgpackReader) guard() (guardValue, error) {
	if r.i >= len(r.b) {
		return guardValue{}, errMsgpack
	}
	switch c := r.b[r.i]; {
	case c == 0xc0:
		r.i++
		return guardValue{kind: guardNull}, nil
	case c == 0xc2 || c == 0xc3:
		r.i++
		return guardValue{kind: guardBool, b: c == 0xc3}, nil
	case isExt(c):
		// cty marks a value not known until apply as an extension type.
		return guardValue{kind: guardUnknown}, r.skip(0)
	}
	return guardValue{kind: guardOther}, r.skip(0)
}

func isExt(c byte) bool { return c == 0xc7 || c == 0xc8 || c == 0xc9 || (c >= 0xd4 && c <= 0xd8) }

func (r *msgpackReader) skip(depth int) error {
	if depth > maxMsgpackDepth {
		return errMsgpack
	}
	c, err := r.code()
	if err != nil {
		return err
	}
	var n uint64
	switch {
	case c <= 0x7f, c >= 0xe0, c == 0xc0, c == 0xc2, c == 0xc3:
		return nil
	case c >= 0x80 && c <= 0x8f:
		return r.skipN(uint64(c&0x0f)*2, depth)
	case c >= 0x90 && c <= 0x9f:
		return r.skipN(uint64(c&0x0f), depth)
	case c >= 0xa0 && c <= 0xbf:
		_, err = r.take(uint64(c & 0x1f))
		return err
	case c == 0xc4, c == 0xd9:
		n, err = r.bigEndian(1)
	case c == 0xc5, c == 0xda:
		n, err = r.bigEndian(2)
	case c == 0xc6, c == 0xdb:
		n, err = r.bigEndian(4)
	case c == 0xc7:
		n, err = r.bigEndian(1)
		n++
	case c == 0xc8:
		n, err = r.bigEndian(2)
		n++
	case c == 0xc9:
		n, err = r.bigEndian(4)
		n++
	case c == 0xcc, c == 0xd0:
		n = 1
	case c == 0xcd, c == 0xd1:
		n = 2
	case c == 0xca, c == 0xce, c == 0xd2:
		n = 4
	case c == 0xcb, c == 0xcf, c == 0xd3:
		n = 8
	case c >= 0xd4 && c <= 0xd8:
		n = 1 + 1<<(c-0xd4)
	case c == 0xdc:
		n, err = r.bigEndian(2)
		if err == nil {
			return r.skipN(n, depth)
		}
	case c == 0xdd:
		n, err = r.bigEndian(4)
		if err == nil {
			return r.skipN(n, depth)
		}
	case c == 0xde:
		n, err = r.bigEndian(2)
		if err == nil {
			return r.skipN(n*2, depth)
		}
	case c == 0xdf:
		n, err = r.bigEndian(4)
		if err == nil {
			return r.skipN(n*2, depth)
		}
	default:
		return errMsgpack
	}
	if err != nil {
		return err
	}
	_, err = r.take(n)
	return err
}

func (r *msgpackReader) skipN(n uint64, depth int) error {
	// Each element takes at least one byte.
	if n > uint64(len(r.b)-r.i) {
		return errMsgpack
	}
	for range n {
		if err := r.skip(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

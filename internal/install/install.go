// Package install adds and removes the hook in a Claude Code settings file.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// marker identifies an entry as ours; matching on the binary path would miss
	// one left behind by a moved or removed binary.
	marker  = "# managed by whatdies"
	matcher = "Bash|Monitor|PowerShell"
	timeout = 30
)

// HookCommand returns the shell command to install. A hook that crashes or whose
// binary is gone would otherwise let the command run, so failure exits 2.
func HookCommand(binary string) string {
	return fmt.Sprintf(`%s hook || { echo 'whatdies hook failed. Reinstall it, or remove its PreToolUse entry from your Claude Code settings.' >&2; exit 2; } %s`,
		quote(binary), marker)
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// member is one key of a JSON object. Settings files are edited by people, so
// keys keep their order and a rewrite stays a small diff.
type member struct {
	key   string
	value json.RawMessage
}

type object []member

func parseObject(raw []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("expected a JSON object")
	}
	var out object
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, member{key.(string), value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	// Rewriting the file would silently drop whatever follows the object.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return out, nil
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

func (o object) set(key string, value json.RawMessage) object {
	for i, m := range o {
		if m.key == key {
			o[i].value = value
			return o
		}
	}
	return append(o, member{key, value})
}

func (o object) without(key string) object {
	out := make(object, 0, len(o))
	for _, m := range o {
		if m.key != key {
			out = append(out, m)
		}
	}
	return out
}

// marshal does not escape <, > and &, which appear in the hook command and
// would make it unreadable in the settings file.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// withoutOurs drops our hooks from a PreToolUse list, keeping anything else that
// shares an entry with them.
func withoutOurs(entries []json.RawMessage) ([]json.RawMessage, bool, error) {
	out := make([]json.RawMessage, 0, len(entries))
	removed := false
	for _, raw := range entries {
		entry, err := parseObject(raw)
		if err != nil {
			return nil, false, fmt.Errorf("a PreToolUse entry is not an object: %w", err)
		}
		list, ok := entry.get("hooks")
		if !ok {
			out = append(out, raw)
			continue
		}
		var hooks []json.RawMessage
		if err := json.Unmarshal(list, &hooks); err != nil {
			return nil, false, fmt.Errorf("a PreToolUse entry has invalid hooks: %w", err)
		}
		kept := hooks[:0:0]
		for _, h := range hooks {
			if bytes.Contains(h, []byte(marker)) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		switch {
		case len(kept) == len(hooks):
			out = append(out, raw)
		case len(kept) > 0:
			value, err := marshal(kept)
			if err != nil {
				return nil, false, err
			}
			rewritten, err := marshal(entry.set("hooks", value))
			if err != nil {
				return nil, false, err
			}
			out = append(out, rewritten)
		}
	}
	return out, removed, nil
}

func load(path string) (object, []json.RawMessage, object, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return object{}, nil, object{}, nil
	}
	top, err := parseObject(raw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	hooks := object{}
	if value, ok := top.get("hooks"); ok {
		if hooks, err = parseObject(value); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: hooks: %w", path, err)
		}
	}
	var entries []json.RawMessage
	if value, ok := hooks.get("PreToolUse"); ok {
		if err := json.Unmarshal(value, &entries); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: hooks.PreToolUse: %w", path, err)
		}
	}
	return top, entries, hooks, nil
}

func save(path string, top, hooks object, entries []json.RawMessage) error {
	if len(entries) > 0 {
		value, err := marshal(entries)
		if err != nil {
			return err
		}
		hooks = hooks.set("PreToolUse", value)
	} else {
		hooks = hooks.without("PreToolUse")
	}
	if len(hooks) > 0 {
		value, err := marshal(hooks)
		if err != nil {
			return err
		}
		top = top.set("hooks", value)
	} else {
		top = top.without("hooks")
	}
	compact, err := marshal(top)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	return writeAtomic(path, pretty.Bytes())
}

// writeAtomic follows a symlink first: settings kept in a dotfiles repository
// are usually linked, and a rename would replace the link with a plain file.
func writeAtomic(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if err := backup(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".whatdies-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// backup keeps the file as it was before whatdies first touched it.
func backup(path string) error {
	dst := path + ".whatdies.bak"
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Install adds the hook to the settings file at path, replacing any earlier one.
func Install(path, binary string) error {
	top, entries, hooks, err := load(path)
	if err != nil {
		return err
	}
	entries, _, err = withoutOurs(entries)
	if err != nil {
		return err
	}
	entry, err := marshal(map[string]any{
		"matcher": matcher,
		"hooks": []map[string]any{{
			"type":      "command",
			"command":   HookCommand(binary),
			"timeout":   timeout,
			"onFailure": "block",
		}},
	})
	if err != nil {
		return err
	}
	return save(path, top, hooks, append(entries, entry))
}

// Uninstall removes the hook and reports whether one was present.
func Uninstall(path string) (bool, error) {
	top, entries, hooks, err := load(path)
	if err != nil {
		return false, err
	}
	entries, removed, err := withoutOurs(entries)
	if err != nil || !removed {
		return false, err
	}
	return true, save(path, top, hooks, entries)
}

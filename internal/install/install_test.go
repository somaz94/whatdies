package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bin = "/opt/tools/whatdies"

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func settings(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	if err := Install(path, bin); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type      string `json:"type"`
					Command   string `json:"command"`
					Timeout   int    `json:"timeout"`
					OnFailure string `json:"onFailure"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc.Hooks.PreToolUse[0]
	h := entry.Hooks[0]
	if entry.Matcher != "Bash|Monitor|PowerShell" || h.Type != "command" || h.Timeout != 30 || h.OnFailure != "block" {
		t.Fatalf("%+v", entry)
	}
	if !strings.Contains(h.Command, "'"+bin+"' hook") || !strings.Contains(h.Command, "exit 2") {
		t.Fatalf("command = %q", h.Command)
	}
}

func TestInstallKeepsOrderAndOtherSettings(t *testing.T) {
	path := settings(t, `{
  "zeta": 1,
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "b"}]}],
    "PreToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "a"}]}]
  },
  "alpha": {"nested": "<&>"}
}`)
	if err := Install(path, bin); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if strings.Index(got, `"zeta"`) > strings.Index(got, `"hooks"`) || strings.Index(got, `"hooks"`) > strings.Index(got, `"alpha"`) {
		t.Errorf("top-level order changed:\n%s", got)
	}
	if strings.Index(got, `"Stop"`) > strings.Index(got, `"PreToolUse"`) {
		t.Errorf("hook order changed:\n%s", got)
	}
	for _, want := range []string{`"matcher": "Edit"`, `"command": "b"`, `"nested": "<&>"`, bin} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s:\n%s", want, got)
		}
	}
	if !strings.Contains(got, ">&2") {
		t.Errorf("command was escaped:\n%s", got)
	}
	if strings.Index(got, `"matcher": "Edit"`) > strings.Index(got, bin) {
		t.Errorf("an existing hook must run first:\n%s", got)
	}
}

func TestInstallTwiceLeavesOneEntry(t *testing.T) {
	path := settings(t, `{}`)
	for _, b := range []string{bin, "/new/place/whatdies"} {
		if err := Install(path, b); err != nil {
			t.Fatal(err)
		}
	}
	got := read(t, path)
	if strings.Count(got, marker) != 1 || strings.Contains(got, bin) || !strings.Contains(got, "/new/place/whatdies") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUninstallRestoresWhatWasThere(t *testing.T) {
	path := settings(t, `{"a": 1}`)
	if err := Install(path, bin); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(path)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	got := read(t, path)
	if strings.Contains(got, "hooks") || !strings.Contains(got, `"a": 1`) {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUninstallKeepsHooksSharingAnEntry(t *testing.T) {
	path := settings(t, `{"hooks": {"PreToolUse": [
	 {"matcher": "Bash", "note": "x", "hooks": [
	   {"type": "command", "command": "keep"},
	   {"type": "command", "command": "'/x/whatdies' hook # managed by whatdies"}]},
	 {"matcher": "Edit"}
	]}}`)
	removed, err := Uninstall(path)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	got := read(t, path)
	for _, want := range []string{`"keep"`, `"note": "x"`, `"matcher": "Edit"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, marker) {
		t.Errorf("not removed:\n%s", got)
	}
}

func TestUninstallWithNothingThere(t *testing.T) {
	path := settings(t, `{"a":1}`)
	removed, err := Uninstall(path)
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if read(t, path) != `{"a":1}` {
		t.Fatal("file was rewritten")
	}
	if removed, err := Uninstall(filepath.Join(t.TempDir(), "absent.json")); err != nil || removed {
		t.Fatalf("absent: removed=%v err=%v", removed, err)
	}
}

func TestBrokenSettingsAreRefusedAndUntouched(t *testing.T) {
	for _, text := range []string{
		`not json`, `[]`, `{"a":`, `{"hooks": []}`, `{"hooks": {"PreToolUse": {}}}`,
		`{"hooks": {"PreToolUse": [1]}}`, `{"hooks": {"PreToolUse": [{"hooks": 1}]}}`,
	} {
		path := settings(t, text)
		if err := Install(path, bin); err == nil {
			t.Errorf("%s: want error", text)
		}
		if read(t, path) != text {
			t.Errorf("%s: file changed", text)
		}
		if _, err := Uninstall(path); err == nil {
			t.Errorf("%s: uninstall wants error", text)
		}
	}
}

func TestEmptyFileCountsAsNew(t *testing.T) {
	path := settings(t, "  \n")
	if err := Install(path, bin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), marker) {
		t.Fatal("not installed")
	}
}

func TestModeAndBackup(t *testing.T) {
	const original = `{"a":1}`
	path := settings(t, original)
	for i := 0; i < 2; i++ {
		if err := Install(path, bin); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if got := read(t, path+".whatdies.bak"); got != original {
		t.Errorf("backup = %q, want the first version", got)
	}
}

func TestSymlinkIsFollowed(t *testing.T) {
	real := settings(t, `{}`)
	link := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if err := Install(link, bin); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link was replaced")
	}
	if !strings.Contains(read(t, real), marker) {
		t.Fatal("target not updated")
	}
}

func TestQuotingSurvivesAnApostrophe(t *testing.T) {
	cmd := HookCommand("/home/o'neil/whatdies")
	if !strings.HasPrefix(cmd, `'/home/o'\''neil/whatdies' hook`) {
		t.Fatalf("cmd = %s", cmd)
	}
}

func TestUnreadablePathFails(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir, bin); err == nil {
		t.Fatal("reading a directory should fail")
	}
}

func TestSettingsThatAreNotOneJSONObject(t *testing.T) {
	for _, raw := range []string{`{1:2}`, `{"a":}`, `{"a":1`, `{"a":1} trailing`, `{} {}`, `[]`, ``} {
		if _, err := parseObject([]byte(raw)); err == nil {
			t.Errorf("%q: want an error", raw)
		}
	}
	if _, err := parseObject([]byte("{\"a\":1}\n")); err != nil {
		t.Errorf("a trailing newline is fine: %v", err)
	}
}

// Rewriting a file with data after its JSON would silently drop that data.
func TestInstallRefusesToDropWhatItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	const body = "{\"a\":1}\n{\"b\":2}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(path, "/bin/whatdies"); err == nil {
		t.Fatal("want an error")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("file changed: %q %v", got, err)
	}
}

func TestWriteAtomicFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(file, "sub", "settings.json"), []byte("{}")); err == nil {
		t.Error("a parent that is a file: want an error")
	}

	readOnly := t.TempDir()
	existing := filepath.Join(readOnly, "settings.json")
	if err := os.WriteFile(existing, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	if err := writeAtomic(filepath.Join(readOnly, "new.json"), []byte("{}")); err == nil {
		t.Error("an unwritable directory: want an error")
	}
	if err := writeAtomic(existing, []byte("{}")); err == nil {
		t.Error("nowhere to put the backup: want an error")
	}

	dir := t.TempDir()
	locked := filepath.Join(dir, "locked.json")
	if err := os.WriteFile(locked, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(locked, []byte("{}")); err == nil {
		t.Error("an unreadable original cannot be backed up: want an error")
	}
}

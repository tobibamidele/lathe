package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	good := map[string]string{"Add Users": "add_users", "add-users": "add_users", "  init ": "init", "v2_changes": "v2_changes"}
	for in, want := range good {
		if got, err := sanitizeName(in); err != nil || got != want {
			t.Errorf("sanitizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "a/b", "drop;table", "naïve"} {
		if _, err := sanitizeName(bad); err == nil {
			t.Errorf("sanitizeName(%q) should fail", bad)
		}
	}
}

func TestAbsoluteSQLitePath(t *testing.T) {
	wd, _ := os.Getwd()
	cases := map[string]string{
		":memory:":                       ":memory:",
		"file::memory:?mode=memory":      "file::memory:?mode=memory",
		"dev.db":                         filepath.Join(wd, "dev.db"),
		"file:dev.db?cache=shared":       "file:" + filepath.Join(wd, "dev.db") + "?cache=shared",
		"/var/data/app.db":               "/var/data/app.db",
		"file:/var/data/app.db?mode=rwc": "file:/var/data/app.db?mode=rwc",
	}
	for in, want := range cases {
		if got := absoluteSQLitePath(in); got != want {
			t.Errorf("absoluteSQLitePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAllowsFlagsAfterPositionals(t *testing.T) {
	a := &app{out: &bytes.Buffer{}, err: &bytes.Buffer{}}
	fs := a.flags("x")
	dry := fs.Bool("dry-run", false, "")
	dir := fs.String("schema", "schema", "")
	pos, err := parse(fs, []string{"add_users", "--dry-run", "--schema", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 1 || pos[0] != "add_users" || !*dry || *dir != "s2" {
		t.Errorf("pos=%v dry=%v dir=%s", pos, *dry, *dir)
	}
}

func TestInitScaffoldsSchemaAndIgnoresScratchDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := Main([]string{"init", "--dialect", "sqlite"}, &out, &errb); code != 0 {
		t.Fatalf("init failed: %s", errb.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, "schema", "schema.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"package schema", "s.SQLite", "var Schema = s.New("} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffold lacks %q:\n%s", want, body)
		}
	}
	ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(ignore), "_lathe/") {
		t.Errorf(".gitignore = %q", ignore)
	}

	// refuses to overwrite, rejects unknown dialects, is idempotent for .gitignore
	if code := Main([]string{"init"}, &out, &errb); code == 0 {
		t.Error("second init must not overwrite the schema")
	}
	if code := Main([]string{"init", "--dialect", "oracle", "--schema", "other"}, &out, &errb); code == 0 {
		t.Error("unknown dialect must fail")
	}
	if err := ensureIgnored(dir, "_lathe/"); err != nil {
		t.Fatal(err)
	}
	ignore, _ = os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(ignore), "_lathe/") != 1 {
		t.Errorf("_lathe/ duplicated in .gitignore: %q", ignore)
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if Main(nil, &out, &errb) == 0 || Main([]string{"nope"}, &out, &errb) == 0 || Main([]string{"migrate"}, &out, &errb) == 0 {
		t.Error("bad invocations must fail")
	}
	out.Reset()
	if Main([]string{"version"}, &out, &errb) != 0 || !strings.Contains(out.String(), "lathe") {
		t.Errorf("version: %q", out.String())
	}
}

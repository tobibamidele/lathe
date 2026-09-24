// Package cli implements the lathe command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/tobibamidele/lathe/internal/loader"
	"github.com/tobibamidele/lathe/schema"
)

// Version is set at build time with -ldflags "-X .../internal/cli.Version=v1.2.3".
// See version.go for how it combines with the go toolchain's embedded build
// info; stderr stays separate for the migrate/driver errors below.
const usage = `lathe - a schema-first ORM toolchain for Go

Usage:
  lathe init [--dialect postgres|mysql|sqlite] [--schema DIR]
        Scaffold a schema package.
  lathe generate [--schema DIR]
        Generate the Go package (models, typed columns, client) from the schema.
  lathe migrate diff [NAME] [--schema DIR] [--dry-run]
        Write a migration for the difference between the schema and the last
        snapshot, plus its down migration.
  lathe migrate new NAME
        Create an empty migration pair for hand written SQL.
  lathe migrate snapshot [--schema DIR]
        Record the current schema as the snapshot without writing a migration
        (use after a hand written migration).
  lathe migrate up [--steps N] [--url URL] [--schema DIR]
  lathe migrate down [--steps N] [--url URL] [--schema DIR]
  lathe migrate status [--url URL] [--schema DIR]
        Apply, revert or list migrations. The URL defaults to $DATABASE_URL.
  lathe version

The schema lives in a Go package (default ./schema) that declares
'var Schema = schema.New(...)'. Run lathe from inside your Go module.
`

type app struct {
	out, err io.Writer
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	a := &app{out: stdout, err: stderr}
	if err := a.run(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "lathe: "+err.Error())
		return 1
	}
	return 0
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.err, usage)
		return errors.New("no command given")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return a.initCmd(rest)
	case "generate":
		return a.generateCmd(rest)
	case "migrate":
		return a.migrateCmd(rest)
	case "version", "--version", "-v":
		var info buildInfo
		if bi, ok := debug.ReadBuildInfo(); ok {
			info = runtimeBuildInfo{bi}
		}
		fmt.Fprintln(a.out, versionLine(Version, Revision, info))
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(a.out, usage)
		return nil
	}
	fmt.Fprint(a.err, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("lathe "+name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

// parse parses flags that may appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// load finds the module and executes the schema package.
func (a *app) load(schemaDir string) (*loader.Project, *schema.Export, error) {
	proj, err := loader.Find(".")
	if err != nil {
		return nil, nil, err
	}
	if st, err := os.Stat(schemaDir); err != nil || !st.IsDir() {
		return nil, nil, fmt.Errorf("no schema package at %s; run `lathe init` or pass --schema", schemaDir)
	}
	exp, err := proj.LoadExport(schemaDir)
	if err != nil {
		return nil, nil, err
	}
	return proj, exp, nil
}

func writeIfChanged(path string, content []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, content, 0o644)
}

func sanitizeName(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "_", "-", "_").Replace(s)
	if s == "" {
		return "", errors.New("the migration name is empty")
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "", fmt.Errorf("invalid migration name %q: use letters, digits and underscores", s)
		}
	}
	return s, nil
}

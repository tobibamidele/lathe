package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/tobibamidele/lathe/internal/ddl"
	"github.com/tobibamidele/lathe/internal/plan"
	"github.com/tobibamidele/lathe/schema"
)

const snapshotFile = "snapshot.json"

func (a *app) migrateCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.err, usage)
		return errors.New("migrate needs a subcommand: diff, new, snapshot, up, down or status")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "diff":
		return a.diffCmd(rest)
	case "new":
		return a.newCmd(rest)
	case "snapshot":
		return a.snapshotCmd(rest)
	case "up", "down", "status":
		return a.runCmd(sub, rest)
	}
	return fmt.Errorf("unknown migrate subcommand %q", sub)
}

func readSnapshot(dir string) (*schema.Snapshot, error) {
	body, err := os.ReadFile(filepath.Join(dir, snapshotFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s schema.Snapshot
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", filepath.Join(dir, snapshotFile), err)
	}
	if s.Version > schema.SnapshotVersion {
		return nil, fmt.Errorf("%s was written by a newer lathe (snapshot version %d); upgrade lathe", snapshotFile, s.Version)
	}
	return &s, nil
}

func writeSnapshot(dir string, s *schema.Snapshot) error {
	body, err := json.MarshalIndent(s.StripHints(), "", "  ")
	if err != nil {
		return err
	}
	_, err = writeIfChanged(filepath.Join(dir, snapshotFile), append(body, '\n'))
	return err
}

func sqlFile(title string, stmts, notes []string) []byte {
	var b strings.Builder
	b.WriteString("-- " + title + "\n")
	for _, n := range notes {
		b.WriteString("-- WARNING: " + n + "\n")
	}
	for i, s := range stmts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s + ";\n")
	}
	return []byte(b.String())
}

func (a *app) diffCmd(args []string) error {
	fs := a.flags("migrate diff")
	schemaDir := fs.String("schema", "schema", "schema package directory")
	dry := fs.Bool("dry-run", false, "print the SQL instead of writing files")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name := "changes"
	if len(pos) > 1 {
		return errors.New("migrate diff takes at most one name")
	}
	if len(pos) == 1 {
		name = pos[0]
	}
	name, err = sanitizeName(name)
	if err != nil {
		return err
	}

	proj, exp, err := a.load(*schemaDir)
	if err != nil {
		return err
	}
	dir := proj.Abs(exp.Config.Migrations)
	old, err := readSnapshot(dir)
	if err != nil {
		return err
	}
	d, err := ddl.For(exp.Snapshot.Dialect)
	if err != nil {
		return err
	}
	p, err := plan.Diff(old, exp.Snapshot, d)
	if err != nil {
		return err
	}
	if p.Empty() {
		fmt.Fprintln(a.out, "no changes: the schema matches the last snapshot")
		return nil
	}
	up, err := p.Up(d)
	if err != nil {
		return err
	}
	down, err := p.Down(d)
	if err != nil {
		return err
	}
	warnings := p.Warnings()

	if *dry {
		fmt.Fprintln(a.out, "-- up")
		a.out.Write(sqlFile("dry run", up, warnings))
		fmt.Fprintln(a.out, "\n-- down")
		a.out.Write(sqlFile("dry run", down, nil))
		return nil
	}

	version := time.Now().UTC().Format("20060102150405")
	base := filepath.Join(dir, version+"_"+name)
	if _, err := os.Stat(base + ".up.sql"); err == nil {
		return fmt.Errorf("%s.up.sql already exists; try again in a second", filepath.Base(base))
	}
	if _, err := writeIfChanged(base+".up.sql", sqlFile(name+" (up)", up, warnings)); err != nil {
		return err
	}
	if _, err := writeIfChanged(base+".down.sql", sqlFile(name+" (down)", down, nil)); err != nil {
		return err
	}
	if err := writeSnapshot(dir, exp.Snapshot); err != nil {
		return err
	}

	rel, _ := filepath.Rel(proj.Root, base)
	fmt.Fprintf(a.out, "created %s.up.sql\ncreated %s.down.sql\n\n", rel, rel)
	for _, line := range p.Describe() {
		fmt.Fprintln(a.out, "  "+line)
	}
	for _, w := range warnings {
		fmt.Fprintln(a.err, "warning: "+w)
	}
	fmt.Fprintln(a.out, "\nReview the SQL, then apply it with: lathe migrate up")
	return nil
}

func (a *app) newCmd(args []string) error {
	fs := a.flags("migrate new")
	schemaDir := fs.String("schema", "schema", "schema package directory")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: lathe migrate new NAME")
	}
	name, err := sanitizeName(pos[0])
	if err != nil {
		return err
	}
	proj, exp, err := a.load(*schemaDir)
	if err != nil {
		return err
	}
	dir := proj.Abs(exp.Config.Migrations)
	base := filepath.Join(dir, time.Now().UTC().Format("20060102150405")+"_"+name)
	for _, kind := range []string{"up", "down"} {
		body := []byte("-- " + name + " (" + kind + ")\n-- Write SQL here. Separate statements with semicolons.\n")
		if _, err := writeIfChanged(base+"."+kind+".sql", body); err != nil {
			return err
		}
	}
	rel, _ := filepath.Rel(proj.Root, base)
	fmt.Fprintf(a.out, "created %s.up.sql\ncreated %s.down.sql\n", rel, rel)
	fmt.Fprintln(a.out, "\nAfter writing the SQL, run `lathe migrate snapshot` if it brings the database in line with the schema.")
	return nil
}

func (a *app) snapshotCmd(args []string) error {
	fs := a.flags("migrate snapshot")
	schemaDir := fs.String("schema", "schema", "schema package directory")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	proj, exp, err := a.load(*schemaDir)
	if err != nil {
		return err
	}
	dir := proj.Abs(exp.Config.Migrations)
	if err := writeSnapshot(dir, exp.Snapshot); err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Root, filepath.Join(dir, snapshotFile))
	fmt.Fprintf(a.out, "wrote %s\n", rel)
	return nil
}

// ---- up / down / status ----

var defaultDrivers = map[schema.Dialect]string{
	schema.Postgres: "github.com/jackc/pgx/v5/stdlib",
	schema.MySQL:    "github.com/go-sql-driver/mysql",
	schema.SQLite:   "modernc.org/sqlite",
}

var migrateSrc = template.Must(template.New("migrate").Parse(`package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	_ "{{.Driver}}"

	"github.com/tobibamidele/lathe/migrate"
	dialect "github.com/tobibamidele/lathe/{{.Dialect}}"
)

func main() {
	ctx := context.Background()
	db, err := dialect.Open(os.Getenv("LATHE_DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "lathe: "+err.Error())
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "lathe: cannot connect to the database: "+err.Error())
		os.Exit(1)
	}
	steps, _ := strconv.Atoi(os.Getenv("LATHE_STEPS"))
	if err := migrate.Run(ctx, db, os.DirFS(os.Getenv("LATHE_MIGRATIONS")), os.Getenv("LATHE_ACTION"), steps, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lathe: "+err.Error())
		os.Exit(1)
	}
}
`))

func (a *app) runCmd(action string, args []string) error {
	fs := a.flags("migrate " + action)
	schemaDir := fs.String("schema", "schema", "schema package directory")
	url := fs.String("url", "", "database URL (default $DATABASE_URL)")
	steps := fs.Int("steps", 0, "number of migrations to apply or revert")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dsn := *url
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("no database URL: pass --url or set DATABASE_URL")
	}

	proj, exp, err := a.load(*schemaDir)
	if err != nil {
		return err
	}
	dialect := exp.Snapshot.Dialect
	if dialect == schema.SQLite {
		dsn = absoluteSQLitePath(dsn)
	}
	driver := exp.Config.Driver
	if driver == "" {
		driver = defaultDrivers[dialect]
	}
	var src bytes.Buffer
	if err := migrateSrc.Execute(&src, struct{ Driver, Dialect string }{driver, string(dialect)}); err != nil {
		return err
	}
	env := []string{
		"LATHE_DATABASE_URL=" + dsn,
		"LATHE_MIGRATIONS=" + proj.Abs(exp.Config.Migrations),
		"LATHE_ACTION=" + action,
		"LATHE_STEPS=" + strconv.Itoa(*steps),
	}
	if err := proj.Run(src.String(), env, a.out, a.err); err != nil {
		return fmt.Errorf("migrate %s failed (the driver package %s must be in your go.mod: go get %s)", action, driver, driver)
	}
	return nil
}

// absoluteSQLitePath makes a relative database path absolute, because the
// migration program runs from the module root rather than the caller's
// directory.
func absoluteSQLitePath(dsn string) string {
	if dsn == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return dsn
	}
	prefix := ""
	if strings.HasPrefix(dsn, "file:") {
		prefix, dsn = "file:", strings.TrimPrefix(dsn, "file:")
	}
	path, query := dsn, ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		path, query = dsn[:i], dsn[i:]
	}
	if strings.HasPrefix(path, "//") || path == "" {
		return prefix + dsn
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	return prefix + path + query
}

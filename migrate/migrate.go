// Package migrate applies SQL migrations.
//
// Migrations are pairs of files in a directory:
//
//	20260919120000_create_users.up.sql
//	20260919120000_create_users.down.sql
//
// The lathe CLI writes them for you (lathe migrate diff), but hand written
// files work the same. Applied versions are recorded in a lathe_migrations
// table together with a checksum, so an edited migration is detected.
//
// Embed the directory to migrate at start-up without the CLI:
//
//	//go:embed migrations/*.sql
//	var files embed.FS
//
//	sub, _ := fs.Sub(files, "migrations")
//	_, err := migrate.Up(ctx, db, sub)
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tobibamidele/lathe"
)

// Migration is one versioned change.
type Migration struct {
	Version  string // digits, usually a timestamp
	Name     string
	Up       string
	Down     string // empty when there is no down file
	Checksum string // of Up
}

// State is a migration together with what the database knows about it.
type State struct {
	Migration
	Applied   bool
	AppliedAt time.Time
	// Modified is set when the file changed after it was applied.
	Modified bool
	// Missing is set for applied versions that have no file any more.
	Missing bool
}

// Option configures a run.
type Option func(*settings)

type settings struct {
	table string
	steps int
	log   func(string)
}

// WithTable overrides the bookkeeping table name (default "lathe_migrations").
func WithTable(name string) Option { return func(s *settings) { s.table = name } }

// WithSteps limits how many migrations Up applies (0 means all).
func WithSteps(n int) Option { return func(s *settings) { s.steps = n } }

// WithLog receives one line per applied or reverted migration.
func WithLog(fn func(string)) Option { return func(s *settings) { s.log = fn } }

func newSettings(opts []Option) *settings {
	s := &settings{table: "lathe_migrations", log: func(string) {}}
	for _, o := range opts {
		o(s)
	}
	return s
}

var fileRE = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_\-]+)\.(up|down)\.sql$`)

// Load reads the migrations in the root of fsys, ordered by version.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	byVersion := map[string]*Migration{}
	downs := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		version, name, kind := m[1], m[2], m[3]
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if kind == "down" {
			downs[version] = string(body)
			continue
		}
		if prev, dup := byVersion[version]; dup {
			return nil, fmt.Errorf("migrate: version %s is used by both %q and %q", version, prev.Name, name)
		}
		sum := sha256.Sum256(body)
		byVersion[version] = &Migration{Version: version, Name: name, Up: string(body), Checksum: hex.EncodeToString(sum[:])}
	}
	for v := range downs {
		if _, ok := byVersion[v]; !ok {
			return nil, fmt.Errorf("migrate: %s.down.sql has no matching up file", v)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for v, m := range byVersion {
		m.Down = downs[v]
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[i].Version, out[j].Version) })
	return out, nil
}

func versionLess(a, b string) bool {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// Up applies every pending migration in order and returns the ones it applied.
func Up(ctx context.Context, db *lathe.DB, fsys fs.FS, opts ...Option) ([]Migration, error) {
	set := newSettings(opts)
	migs, err := Load(fsys)
	if err != nil {
		return nil, err
	}
	var applied []Migration
	err = withSession(ctx, db, set, func(s *session) error {
		states, err := s.states(ctx, migs)
		if err != nil {
			return err
		}
		for _, st := range states {
			if st.Modified {
				return fmt.Errorf("migrate: %s_%s was changed after it was applied (checksum mismatch); write a new migration instead", st.Version, st.Name)
			}
		}
		for _, st := range states {
			if st.Applied || st.Missing {
				continue
			}
			if set.steps > 0 && len(applied) >= set.steps {
				break
			}
			if err := s.apply(ctx, st.Migration, true); err != nil {
				return fmt.Errorf("migrate: applying %s_%s: %w", st.Version, st.Name, err)
			}
			set.log(fmt.Sprintf("applied  %s_%s", st.Version, st.Name))
			applied = append(applied, st.Migration)
		}
		return nil
	})
	return applied, err
}

// Down reverts the most recently applied migrations, newest first. steps must
// be positive.
func Down(ctx context.Context, db *lathe.DB, fsys fs.FS, steps int, opts ...Option) ([]Migration, error) {
	if steps < 1 {
		return nil, errors.New("migrate: Down needs steps >= 1")
	}
	set := newSettings(opts)
	migs, err := Load(fsys)
	if err != nil {
		return nil, err
	}
	var reverted []Migration
	err = withSession(ctx, db, set, func(s *session) error {
		states, err := s.states(ctx, migs)
		if err != nil {
			return err
		}
		for i := len(states) - 1; i >= 0 && len(reverted) < steps; i-- {
			st := states[i]
			if !st.Applied {
				continue
			}
			if st.Missing {
				return fmt.Errorf("migrate: %s is applied but its files are missing, cannot revert it", st.Version)
			}
			if strings.TrimSpace(st.Down) == "" {
				return fmt.Errorf("migrate: %s_%s has no down migration", st.Version, st.Name)
			}
			if err := s.apply(ctx, st.Migration, false); err != nil {
				return fmt.Errorf("migrate: reverting %s_%s: %w", st.Version, st.Name, err)
			}
			set.log(fmt.Sprintf("reverted %s_%s", st.Version, st.Name))
			reverted = append(reverted, st.Migration)
		}
		return nil
	})
	return reverted, err
}

// Status reports every known migration and whether it is applied.
func Status(ctx context.Context, db *lathe.DB, fsys fs.FS, opts ...Option) ([]State, error) {
	set := newSettings(opts)
	migs, err := Load(fsys)
	if err != nil {
		return nil, err
	}
	var out []State
	err = withSession(ctx, db, set, func(s *session) error {
		var err error
		out, err = s.states(ctx, migs)
		return err
	})
	return out, err
}

// Run executes "up", "down" or "status" and prints a human readable report.
// The lathe CLI uses it; it is exported so custom tooling can as well.
func Run(ctx context.Context, db *lathe.DB, fsys fs.FS, action string, steps int, out io.Writer, opts ...Option) error {
	opts = append([]Option{WithLog(func(s string) { fmt.Fprintln(out, s) })}, opts...)
	switch action {
	case "up":
		if steps > 0 {
			opts = append(opts, WithSteps(steps))
		}
		applied, err := Up(ctx, db, fsys, opts...)
		if err == nil && len(applied) == 0 {
			fmt.Fprintln(out, "nothing to apply, the database is up to date")
		}
		return err
	case "down":
		if steps < 1 {
			steps = 1
		}
		reverted, err := Down(ctx, db, fsys, steps, opts...)
		if err == nil && len(reverted) == 0 {
			fmt.Fprintln(out, "nothing to revert")
		}
		return err
	case "status":
		states, err := Status(ctx, db, fsys, opts...)
		if err != nil {
			return err
		}
		for _, st := range states {
			mark := "pending "
			switch {
			case st.Missing:
				mark = "MISSING "
			case st.Modified:
				mark = "MODIFIED"
			case st.Applied:
				mark = "applied "
			}
			line := fmt.Sprintf("%s  %s_%s", mark, st.Version, st.Name)
			if st.Applied && !st.AppliedAt.IsZero() {
				line += "  (" + st.AppliedAt.UTC().Format("2006-01-02 15:04:05") + " UTC)"
			}
			fmt.Fprintln(out, line)
		}
		if len(states) == 0 {
			fmt.Fprintln(out, "no migrations found")
		}
		return nil
	}
	return fmt.Errorf("migrate: unknown action %q (want up, down or status)", action)
}

// ---- session ----

type session struct {
	conn    *sql.Conn
	dialect string
	table   string
	q       func(string) string
	rebind  func(string) string
}

func withSession(ctx context.Context, db *lathe.DB, set *settings, fn func(*session) error) (err error) {
	sqlDB := db.SQL()
	if sqlDB == nil {
		return errors.New("migrate: needs a *lathe.DB that is not inside a transaction")
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	d := db.Dialect()
	s := &session{
		conn:    conn,
		dialect: d.Name(),
		table:   set.table,
		q:       d.Quote,
		rebind: func(q string) string {
			n := 0
			var b strings.Builder
			for _, r := range q {
				if r == '?' {
					n++
					b.WriteString(d.Placeholder(n))
					continue
				}
				b.WriteRune(r)
			}
			return b.String()
		},
	}

	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if uerr := unlock(); uerr != nil && err == nil {
			err = uerr
		}
	}()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s VARCHAR(64) PRIMARY KEY, %s VARCHAR(255) NOT NULL, %s VARCHAR(64) NOT NULL, %s BIGINT NOT NULL)",
		s.q(s.table), s.q("version"), s.q("name"), s.q("checksum"), s.q("applied_at"))); err != nil {
		return fmt.Errorf("migrate: creating %s: %w", s.table, err)
	}
	return fn(s)
}

// lock serialises concurrent migrators (several app instances starting at
// once). SQLite has a single writer, so it needs none.
func (s *session) lock(ctx context.Context) (func() error, error) {
	const key = 727274 // arbitrary, shared by every lathe process
	switch s.dialect {
	case "postgres":
		if _, err := s.conn.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_lock(%d)", key)); err != nil {
			return nil, fmt.Errorf("migrate: acquiring lock: %w", err)
		}
		return func() error {
			_, err := s.conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("SELECT pg_advisory_unlock(%d)", key))
			return err
		}, nil
	case "mysql":
		var got sql.NullInt64
		if err := s.conn.QueryRowContext(ctx, "SELECT GET_LOCK('lathe_migrate', 60)").Scan(&got); err != nil || got.Int64 != 1 {
			if err == nil {
				err = errors.New("timed out waiting for another migration to finish")
			}
			return nil, fmt.Errorf("migrate: acquiring lock: %w", err)
		}
		return func() error {
			_, err := s.conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('lathe_migrate')")
			return err
		}, nil
	}
	return func() error { return nil }, nil
}

type appliedRow struct {
	name     string
	checksum string
	at       int64
}

func (s *session) states(ctx context.Context, migs []Migration) ([]State, error) {
	rows, err := s.conn.QueryContext(ctx, fmt.Sprintf("SELECT %s, %s, %s, %s FROM %s",
		s.q("version"), s.q("name"), s.q("checksum"), s.q("applied_at"), s.q(s.table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[string]appliedRow{}
	for rows.Next() {
		var v string
		var r appliedRow
		if err := rows.Scan(&v, &r.name, &r.checksum, &r.at); err != nil {
			return nil, err
		}
		applied[v] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	known := map[string]bool{}
	var out []State
	for _, m := range migs {
		known[m.Version] = true
		st := State{Migration: m}
		if r, ok := applied[m.Version]; ok {
			st.Applied = true
			st.AppliedAt = time.Unix(r.at, 0)
			st.Modified = r.checksum != m.Checksum
		}
		out = append(out, st)
	}
	for v, r := range applied {
		if !known[v] {
			out = append(out, State{
				Migration: Migration{Version: v, Name: r.name, Checksum: r.checksum},
				Applied:   true, AppliedAt: time.Unix(r.at, 0), Missing: true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[i].Version, out[j].Version) })
	return out, nil
}

// apply runs one migration in one direction and records it.
func (s *session) apply(ctx context.Context, m Migration, up bool) (err error) {
	script := m.Up
	if !up {
		script = m.Down
	}
	stmts := SplitStatements(script, s.dialect == "mysql")

	record := func(exec func(string, ...any) error) error {
		if up {
			return exec(s.rebind(fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, ?)",
				s.q(s.table), s.q("version"), s.q("name"), s.q("checksum"), s.q("applied_at"))),
				m.Version, m.Name, m.Checksum, time.Now().Unix())
		}
		return exec(s.rebind(fmt.Sprintf("DELETE FROM %s WHERE %s = ?", s.q(s.table), s.q("version"))), m.Version)
	}

	if s.dialect == "mysql" {
		// MySQL commits DDL implicitly, so a transaction would not protect us.
		exec := func(q string, args ...any) error { _, err := s.conn.ExecContext(ctx, q, args...); return err }
		for i, st := range stmts {
			if err := exec(st); err != nil {
				return fmt.Errorf("statement %d failed (earlier statements of this migration are already applied, MySQL cannot roll back DDL): %w\n%s", i+1, err, snippet(st))
			}
		}
		return record(exec)
	}

	restoreFK := func() {}
	if s.dialect == "sqlite" {
		// Table rebuilds need foreign key enforcement off; the pragma is a
		// no-op inside a transaction, so it is switched around it.
		var was int
		if err := s.conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&was); err == nil && was == 1 {
			if _, err := s.conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
				return err
			}
			restoreFK = func() { _, _ = s.conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON") }
		}
	}
	defer restoreFK()

	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	exec := func(q string, args ...any) error { _, err := tx.ExecContext(ctx, q, args...); return err }
	for i, st := range stmts {
		if err := exec(st); err != nil {
			return fmt.Errorf("statement %d failed: %w\n%s", i+1, err, snippet(st))
		}
	}
	if err := record(exec); err != nil {
		return err
	}
	if s.dialect == "sqlite" {
		if err := checkForeignKeys(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// checkForeignKeys fails when a migration left dangling references behind.
// It only runs when the connection normally enforces foreign keys.
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent sql.NullString
		var rowid sql.NullInt64
		var fkid sql.NullInt64
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		return fmt.Errorf("the migration leaves rows in %q that reference missing rows in %q; nothing was applied", table.String, parent.String)
	}
	return rows.Err()
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

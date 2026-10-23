// Package importer reconstructs a lathe schema snapshot from a directory of
// SQL migration files.
//
// Migrations are applied in file-name order, so a table created in one
// migration and dropped in a later one disappears from the result. The parser
// understands the DDL that migration tools actually emit (Prisma, Drizzle and
// hand-written scripts); constructs lathe cannot express are reported as
// warnings and skipped.
package importer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tobibamidele/lathe/migrate"
	"github.com/tobibamidele/lathe/schema"
)

// Result is the outcome of importing a migration directory.
type Result struct {
	Snapshot *schema.Snapshot
	Warnings []string
	Files    []string
}

type enumDef struct {
	name   string
	values []string
}

type state struct {
	dialect  schema.Dialect
	tables   map[string]*schema.TableDef
	order    []string
	enums    map[string]*enumDef
	enumOf   map[*schema.ColumnDef]string
	warnings []string
	file     string
	stmt     int
}

func newState(d schema.Dialect) *state {
	return &state{
		dialect: d,
		tables:  map[string]*schema.TableDef{},
		enums:   map[string]*enumDef{},
		enumOf:  map[*schema.ColumnDef]string{},
	}
}

// ImportDir reads every .sql file under dir (recursively, skipping down
// migrations) and rebuilds the schema they describe.
func ImportDir(dir string, dialect schema.Dialect) (*Result, error) {
	if !dialect.Valid() {
		return nil, fmt.Errorf("unsupported dialect %q (want postgres, mysql or sqlite)", dialect)
	}
	files, err := collectFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .sql migration files found in %s", dir)
	}

	st := newState(dialect)
	for _, f := range files {
		body, err := os.ReadFile(f.path)
		if err != nil {
			return nil, err
		}
		st.file = f.rel
		scripts := migrate.SplitStatements(string(body), dialect == schema.MySQL)
		for i, script := range scripts {
			st.stmt = i + 1
			st.exec(script)
		}
	}
	st.file, st.stmt = "", 0
	st.finalize()

	snap := &schema.Snapshot{Version: schema.SnapshotVersion, Dialect: dialect, Tables: st.tablesInOrder()}
	return &Result{Snapshot: snap, Warnings: st.warnings, Files: relNames(files)}, nil
}

type sourceFile struct {
	path string
	rel  string
	key  sortKey
}

func collectFiles(dir string) ([]sourceFile, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	var files []sourceFile
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "meta") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".sql") || isDownMigration(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		files = append(files, sourceFile{path: path, rel: filepath.ToSlash(rel), key: makeSortKey(rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		if c := files[i].key.compare(files[j].key); c != 0 {
			return c < 0
		}
		return files[i].rel < files[j].rel
	})
	return files, nil
}

func isDownMigration(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".down.sql", "_down.sql", "-down.sql", ".rollback.sql", ".revert.sql"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// sortKey compares migration names by their leading number when they have one
// (001_, 0001_, 20260101123000_) and lexically otherwise. Nesting tools such
// as Prisma put the number on a directory, so every path segment is examined.
type sortKey struct {
	number string // zero-padded digits, empty when the path has no number
	text   string
}

func makeSortKey(rel string) sortKey {
	rel = filepath.ToSlash(rel)
	for _, seg := range strings.Split(rel, "/") {
		base := strings.TrimSuffix(seg, filepath.Ext(seg))
		digits := leadingDigits(base)
		if digits != "" {
			return sortKey{number: padNumber(digits), text: rel}
		}
	}
	return sortKey{text: rel}
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func padNumber(digits string) string {
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}
	if len(digits) < 20 {
		digits = strings.Repeat("0", 20-len(digits)) + digits
	}
	return digits
}

func (k sortKey) compare(o sortKey) int {
	switch {
	case k.number != "" && o.number != "":
		if c := strings.Compare(k.number, o.number); c != 0 {
			return c
		}
	case k.number != "":
		return -1
	case o.number != "":
		return 1
	}
	return strings.Compare(k.text, o.text)
}

func relNames(files []sourceFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.rel
	}
	return out
}

func (s *state) tablesInOrder() []*schema.TableDef {
	out := make([]*schema.TableDef, 0, len(s.order))
	for _, name := range s.order {
		if t := s.tables[name]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (s *state) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if s.file != "" {
		prefix := s.file
		if s.stmt > 0 {
			prefix += fmt.Sprintf(" (statement %d)", s.stmt)
		}
		msg = prefix + ": " + msg
	}
	s.warnings = append(s.warnings, msg)
}

// ---- lookups ----

func (s *state) table(name string) *schema.TableDef {
	if t := s.tables[name]; t != nil {
		return t
	}
	for k, t := range s.tables {
		if strings.EqualFold(k, name) {
			return t
		}
	}
	return nil
}

func (s *state) enum(name string) *enumDef {
	if e := s.enums[name]; e != nil {
		return e
	}
	for k, e := range s.enums {
		if strings.EqualFold(k, name) {
			return e
		}
	}
	return nil
}

// ---- statement dispatch ----

func (s *state) exec(script string) {
	toks, err := lex(script, s.dialect == schema.MySQL)
	if err != nil {
		s.warn("could not tokenize statement: %v", err)
		return
	}
	if len(toks) == 0 {
		return
	}
	p := &parser{toks: toks}
	switch {
	case p.acceptWord("CREATE"):
		s.execCreate(p)
	case p.acceptWord("ALTER"):
		s.execAlter(p)
	case p.acceptWord("DROP"):
		s.execDrop(p)
	case p.acceptWord("RENAME"):
		s.execRename(p)
	case p.acceptWord("COMMENT"), p.acceptWord("GRANT"), p.acceptWord("REVOKE"),
		p.acceptWord("INSERT"), p.acceptWord("UPDATE"), p.acceptWord("DELETE"),
		p.acceptWord("SET"), p.acceptWord("BEGIN"), p.acceptWord("COMMIT"),
		p.acceptWord("ROLLBACK"), p.acceptWord("PRAGMA"), p.acceptWord("VACUUM"),
		p.acceptWord("ANALYZE"), p.acceptWord("TRUNCATE"):
	case p.acceptWord("DO"):
		// A DO block often wraps CREATE TYPE in an exception handler so the
		// migration is idempotent. Pull the type definitions out; everything
		// else in a procedural block is outside lathe's model.
		s.execDo(p)
	case p.acceptWord("SELECT"), p.acceptWord("WITH"), p.acceptWord("START"),
		p.acceptWord("SAVEPOINT"), p.acceptWord("RELEASE"), p.acceptWord("COPY"):
		// data, session control and maintenance statements do not shape the schema
	default:
		// Views, functions, triggers, extensions and unknown statements are
		// not part of lathe's model; ignore them without noise.
	}
}

// execDo extracts enum definitions from a procedural DO block. The common
// idiom guards CREATE TYPE with an exception handler, so the body is a
// dollar-quoted string rather than ordinary statements.
func (s *state) execDo(p *parser) {
	for !p.done() {
		tok := p.next()
		if tok.kind != tokString || !strings.Contains(strings.ToUpper(tok.text), "CREATE") {
			continue
		}
		body, err := lex(tok.text, false)
		if err != nil {
			s.warn("DO block: could not tokenize body: %v", err)
			continue
		}
		for i := 0; i+1 < len(body); i++ {
			if body[i].isWord("CREATE") && body[i+1].isWord("TYPE") {
				s.createType(&parser{toks: body[i+2:]})
			}
		}
	}
}

func (s *state) execCreate(p *parser) {
	p.acceptWord("OR")
	p.acceptWord("REPLACE")
	temporary := p.acceptWord("TEMP") || p.acceptWord("TEMPORARY")
	p.acceptWord("UNLOGGED")
	unique := p.acceptWord("UNIQUE")
	switch {
	case p.acceptWord("TABLE"):
		if temporary {
			return
		}
		s.createTable(p)
	case p.acceptWord("INDEX"):
		s.createIndex(p, unique)
	case p.acceptWord("TYPE"):
		s.createType(p)
	}
}

func (s *state) execAlter(p *parser) {
	switch {
	case p.acceptWord("TABLE"):
		s.alterTable(p)
	case p.acceptWord("TYPE"):
		s.alterType(p)
	}
}

func (s *state) execDrop(p *parser) {
	switch {
	case p.acceptWord("TABLE"):
		s.dropTables(p)
	case p.acceptWord("INDEX"):
		s.dropIndex(p)
	case p.acceptWord("TYPE"):
		s.dropType(p)
	}
}

func (s *state) execRename(p *parser) {
	if p.acceptWord("TABLE") {
		for {
			from, ok := p.qualifiedIdent()
			if !ok {
				return
			}
			if !p.acceptWord("TO") {
				s.warn("RENAME TABLE %q: expected TO", from)
				return
			}
			to, ok := p.qualifiedIdent()
			if !ok {
				s.warn("RENAME TABLE %q: expected a new name", from)
				return
			}
			s.renameTable(from, to)
			if !p.acceptPunct(",") {
				return
			}
		}
	}
}

// ---- helpers shared by the parsers ----

func (s *state) addColumn(t *schema.TableDef, col *schema.ColumnDef) bool {
	if t.Column(col.Name) != nil {
		s.warn("table %q: column %q is declared more than once", t.Name, col.Name)
		return false
	}
	t.Columns = append(t.Columns, col)
	return true
}

func (s *state) addIndex(t *schema.TableDef, ix *schema.IndexDef) {
	if ix.Name == "" {
		prefix := "idx"
		if ix.Unique {
			prefix = "uq"
		}
		ix.Name = schema.AutoName(prefix, t.Name, ix.Columns...)
	}
	for _, old := range t.Indexes {
		if old.Name == ix.Name {
			s.warn("table %q: index %q is defined more than once; replacing it", t.Name, ix.Name)
			*old = *ix
			return
		}
	}
	t.Indexes = append(t.Indexes, ix)
}

func (s *state) addForeignKey(t *schema.TableDef, fk *schema.ForeignKeyDef) {
	if fk.Name == "" {
		fk.Name = schema.AutoName("fk", t.Name, fk.Columns...)
	}
	for _, old := range t.ForeignKeys {
		if old.Name == fk.Name {
			s.warn("table %q: foreign key %q is defined more than once; replacing it", t.Name, fk.Name)
			*old = *fk
			return
		}
	}
	t.ForeignKeys = append(t.ForeignKeys, fk)
}

func (s *state) removeTable(name string) {
	t := s.table(name)
	if t == nil {
		return
	}
	delete(s.tables, t.Name)
	for i, n := range s.order {
		if n == t.Name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	for _, c := range t.Columns {
		delete(s.enumOf, c)
	}
	for _, other := range s.tables {
		kept := other.ForeignKeys[:0]
		for _, fk := range other.ForeignKeys {
			if strings.EqualFold(fk.RefTable, t.Name) {
				s.warn("table %q: dropping foreign key %q because it references dropped table %q", other.Name, fk.Name, t.Name)
				continue
			}
			kept = append(kept, fk)
		}
		other.ForeignKeys = kept
	}
}

func (s *state) setPrimaryKey(t *schema.TableDef, cols []string) {
	if len(cols) == 0 {
		s.warn("table %q: primary key has no columns", t.Name)
		return
	}
	if len(t.PrimaryKey) > 0 && !sameNames(t.PrimaryKey, cols) {
		s.warn("table %q: primary key is declared more than once; using (%s)", t.Name, strings.Join(cols, ", "))
	}
	t.PrimaryKey = append([]string(nil), cols...)
	for _, name := range cols {
		if c := t.Column(name); c != nil {
			c.Nullable = false
		}
	}
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

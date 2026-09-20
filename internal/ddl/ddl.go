// Package ddl renders schema definitions and schema changes as SQL for each
// supported dialect.
package ddl

import (
	"fmt"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

// Dialect renders DDL for one database engine.
type Dialect interface {
	Name() schema.Dialect
	Quote(ident string) string

	// CanAlterForeignKeys reports whether constraints can be added to and
	// dropped from an existing table. When false (SQLite) foreign key changes
	// are folded into a table rebuild.
	CanAlterForeignKeys() bool

	// CreateTable returns the statements that create t, including its
	// indexes. Only inlineFKs are declared in the CREATE TABLE statement.
	CreateTable(t *schema.TableDef, inlineFKs []*schema.ForeignKeyDef) []string
	DropTable(name string) []string
	RenameTable(from, to string) []string
	AddForeignKey(table string, fk *schema.ForeignKeyDef) []string
	DropForeignKey(table string, fk *schema.ForeignKeyDef) []string
	AlterTable(c *TableChange) ([]string, error)
}

// For returns the DDL dialect for d.
func For(d schema.Dialect) (Dialect, error) {
	switch d {
	case schema.Postgres:
		return postgres{}, nil
	case schema.MySQL:
		return mysql{}, nil
	case schema.SQLite:
		return sqlite{}, nil
	}
	return nil, fmt.Errorf("ddl: unsupported dialect %q", d)
}

// ---- shared helpers ----

func quoteWith(q, ident string) string {
	return q + strings.ReplaceAll(ident, q, q+q) + q
}

func quoteList(d Dialect, cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = d.Quote(c)
	}
	return strings.Join(out, ", ")
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func enumList(values []string, quote func(string) string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = quote(v)
	}
	return strings.Join(out, ", ")
}

func checkName(table, col string) string { return schema.AutoName("ck", table, col) }

func foreignKeyClause(d Dialect, fk *schema.ForeignKeyDef) string {
	s := fmt.Sprintf("CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
		d.Quote(fk.Name), quoteList(d, fk.Columns), d.Quote(fk.RefTable), quoteList(d, fk.RefColumns))
	if fk.OnDelete != schema.NoAction {
		s += " ON DELETE " + string(fk.OnDelete)
	}
	if fk.OnUpdate != schema.NoAction {
		s += " ON UPDATE " + string(fk.OnUpdate)
	}
	return s
}

func createIndex(d Dialect, table string, ix *schema.IndexDef) string {
	kind := "INDEX"
	if ix.Unique {
		kind = "UNIQUE INDEX"
	}
	return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, d.Quote(ix.Name), d.Quote(table), quoteList(d, ix.Columns))
}

// literal renders a literal default for column c.
func literal(c *schema.ColumnDef, quote func(string) string, t, f string) string {
	d := c.Default
	switch {
	case c.Type.Kind == schema.KindBool:
		if d.Value == "true" {
			return t
		}
		return f
	case c.Type.Kind.IsNumeric():
		return d.Value
	}
	return quote(d.Value)
}

// createTableSQL assembles a CREATE TABLE statement from rendered parts.
func createTableSQL(d Dialect, name string, parts []string) string {
	return "CREATE TABLE " + d.Quote(name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n)"
}

func sameStrings(a, b []string) bool {
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

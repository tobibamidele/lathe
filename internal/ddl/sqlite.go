package ddl

import (
	"fmt"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

type sqlite struct{}

func (sqlite) Name() schema.Dialect      { return schema.SQLite }
func (sqlite) Quote(s string) string     { return quoteWith(`"`, s) }
func (sqlite) CanAlterForeignKeys() bool { return false }
func (s sqlite) q(x string) string       { return s.Quote(x) }

func (s sqlite) DropTable(n string) []string { return []string{"DROP TABLE " + s.q(n)} }

func (s sqlite) RenameTable(from, to string) []string {
	return []string{"ALTER TABLE " + s.q(from) + " RENAME TO " + s.q(to)}
}

// SQLite cannot add or drop constraints on an existing table. Foreign keys are
// always declared inline and changed through a table rebuild.
func (s sqlite) AddForeignKey(string, *schema.ForeignKeyDef) []string  { return nil }
func (s sqlite) DropForeignKey(string, *schema.ForeignKeyDef) []string { return nil }

func (s sqlite) typeSQL(c *schema.ColumnDef) string {
	t := c.Type
	switch t.Kind {
	case schema.KindSmallInt, schema.KindInt, schema.KindBigInt:
		return "INTEGER"
	case schema.KindReal, schema.KindDouble:
		return "REAL"
	case schema.KindDecimal:
		// NUMERIC affinity keeps ordering and aggregates numeric.
		return fmt.Sprintf("DECIMAL(%d, %d)", t.Precision, t.Scale)
	case schema.KindBool:
		return "BOOLEAN"
	case schema.KindVarChar, schema.KindText, schema.KindEnum, schema.KindUUID, schema.KindJSON:
		return "TEXT"
	case schema.KindTimestamp:
		return "DATETIME"
	case schema.KindDate:
		return "DATE"
	case schema.KindBytes:
		return "BLOB"
	}
	panic("ddl: unknown kind " + string(t.Kind))
}

// uuidExpr generates a random version 4 UUID.
const sqliteUUID = "lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))), 2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))), 2) || '-' || lower(hex(randomblob(6)))"

func (s sqlite) defaultSQL(c *schema.ColumnDef) string {
	d := c.Default
	switch d.Kind {
	case schema.DefaultNow:
		if c.Type.Kind == schema.KindDate {
			return "CURRENT_DATE"
		}
		return "CURRENT_TIMESTAMP"
	case schema.DefaultUUID:
		return "(" + sqliteUUID + ")"
	case schema.DefaultExpr:
		return "(" + d.Value + ")"
	}
	return literal(c, sqlString, "1", "0")
}

func (s sqlite) columnDef(table string, c *schema.ColumnDef, inlinePK bool) string {
	var b strings.Builder
	b.WriteString(s.q(c.Name) + " " + s.typeSQL(c))
	if inlinePK {
		b.WriteString(" PRIMARY KEY AUTOINCREMENT")
	}
	if !c.Nullable {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil {
		b.WriteString(" DEFAULT " + s.defaultSQL(c))
	}
	if c.Type.Kind == schema.KindEnum {
		fmt.Fprintf(&b, " CONSTRAINT %s CHECK (%s IN (%s))", s.q(checkName(table, c.Name)), s.q(c.Name), enumList(c.Type.Values, sqlString))
	}
	return b.String()
}

func (s sqlite) createTableStmt(name string, t *schema.TableDef, fks []*schema.ForeignKeyDef) string {
	var parts []string
	inlinePK := false
	for _, c := range t.Columns {
		inline := c.AutoIncrement
		inlinePK = inlinePK || inline
		parts = append(parts, s.columnDef(t.Name, c, inline))
	}
	if !inlinePK {
		parts = append(parts, "PRIMARY KEY ("+quoteList(s, t.PrimaryKey)+")")
	}
	for _, fk := range fks {
		parts = append(parts, foreignKeyClause(s, fk))
	}
	return createTableSQL(s, name, parts)
}

func (s sqlite) CreateTable(t *schema.TableDef, inline []*schema.ForeignKeyDef) []string {
	out := []string{s.createTableStmt(t.Name, t, inline)}
	for _, ix := range t.Indexes {
		out = append(out, createIndex(s, t.Name, ix))
	}
	return out
}

// needsRebuild reports whether c cannot be expressed with ALTER TABLE.
func (s sqlite) needsRebuild(c *TableChange) bool {
	if len(c.AlterColumns) > 0 || len(c.AddForeignKeys) > 0 || len(c.DropForeignKeys) > 0 {
		return true
	}
	for _, col := range c.AddColumns {
		// ADD COLUMN cannot add NOT NULL without a constant default, or a
		// default that is computed.
		if !col.Nullable && col.Default == nil {
			return true
		}
		if col.Default != nil && col.Default.Kind != schema.DefaultLiteral {
			return true
		}
		if col.AutoIncrement {
			return true
		}
	}
	return false
}

func (s sqlite) AlterTable(c *TableChange) ([]string, error) {
	table := c.New.Name
	var out []string
	if !s.needsRebuild(c) {
		for _, ix := range c.DropIndexes {
			out = append(out, "DROP INDEX "+s.q(ix.Name))
		}
		for _, col := range c.DropColumns {
			out = append(out, "ALTER TABLE "+s.q(table)+" DROP COLUMN "+s.q(col.Name))
		}
		for _, r := range c.RenameColumns {
			out = append(out, "ALTER TABLE "+s.q(table)+" RENAME COLUMN "+s.q(r.From)+" TO "+s.q(r.To))
		}
		for _, col := range c.AddColumns {
			out = append(out, "ALTER TABLE "+s.q(table)+" ADD COLUMN "+s.columnDef(table, col, false))
		}
		for _, ix := range c.AddIndexes {
			out = append(out, createIndex(s, table, ix))
		}
		return out, nil
	}
	return s.rebuild(c), nil
}

// rebuild implements the table rebuild procedure from the SQLite docs: create
// the new shape, copy the rows, drop the old table, rename, recreate indexes.
// The migration runner disables foreign key enforcement around it.
func (s sqlite) rebuild(c *TableChange) []string {
	table := c.New.Name
	tmp := "__lathe_new_" + table

	var out []string
	out = append(out, s.createTableStmt(tmp, c.New, c.New.ForeignKeys))

	renamedFrom := map[string]string{}
	for _, r := range c.RenameColumns {
		renamedFrom[r.To] = r.From
	}
	var dst, src []string
	for _, col := range c.New.Columns {
		from := col.Name
		if r, ok := renamedFrom[col.Name]; ok {
			from = r
		}
		oldCol := c.Old.Column(from)
		if oldCol == nil {
			continue // new column: takes its default
		}
		expr := s.q(from)
		if oldCol.Nullable && !col.Nullable && col.Default != nil {
			expr = "COALESCE(" + expr + ", " + s.defaultSQL(col) + ")"
		}
		dst = append(dst, s.q(col.Name))
		src = append(src, expr)
	}
	if len(dst) > 0 {
		out = append(out, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			s.q(tmp), strings.Join(dst, ", "), strings.Join(src, ", "), s.q(table)))
	}
	out = append(out, "DROP TABLE "+s.q(table))
	out = append(out, "ALTER TABLE "+s.q(tmp)+" RENAME TO "+s.q(table))
	for _, ix := range c.New.Indexes {
		out = append(out, createIndex(s, table, ix))
	}
	return out
}

package ddl

import (
	"fmt"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

type mysql struct{}

func (mysql) Name() schema.Dialect      { return schema.MySQL }
func (mysql) Quote(s string) string     { return quoteWith("`", s) }
func (mysql) CanAlterForeignKeys() bool { return true }
func (m mysql) q(s string) string       { return m.Quote(s) }
func (m mysql) alter(t string) string   { return "ALTER TABLE " + m.q(t) + " " }

func (m mysql) DropTable(n string) []string { return []string{"DROP TABLE " + m.q(n)} }

func (m mysql) RenameTable(from, to string) []string {
	return []string{"RENAME TABLE " + m.q(from) + " TO " + m.q(to)}
}

// mysqlString escapes a string literal. MySQL treats backslash as an escape
// character by default.
func mysqlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (m mysql) typeSQL(c *schema.ColumnDef) string {
	t := c.Type
	switch t.Kind {
	case schema.KindSmallInt:
		return "SMALLINT"
	case schema.KindInt:
		return "INT"
	case schema.KindBigInt:
		return "BIGINT"
	case schema.KindReal:
		return "FLOAT"
	case schema.KindDouble:
		return "DOUBLE"
	case schema.KindDecimal:
		return fmt.Sprintf("DECIMAL(%d, %d)", t.Precision, t.Scale)
	case schema.KindBool:
		return "BOOLEAN"
	case schema.KindVarChar:
		return fmt.Sprintf("VARCHAR(%d)", t.Length)
	case schema.KindText:
		return "TEXT"
	case schema.KindEnum:
		return "ENUM(" + enumList(t.Values, mysqlString) + ")"
	case schema.KindUUID:
		return "CHAR(36)"
	case schema.KindTimestamp:
		return "DATETIME(6)"
	case schema.KindDate:
		return "DATE"
	case schema.KindJSON:
		return "JSON"
	case schema.KindBytes:
		return "LONGBLOB"
	}
	panic("ddl: unknown kind " + string(t.Kind))
}

func (m mysql) defaultSQL(c *schema.ColumnDef) string {
	d := c.Default
	switch d.Kind {
	case schema.DefaultNow:
		if c.Type.Kind == schema.KindDate {
			return "(CURRENT_DATE)"
		}
		return "CURRENT_TIMESTAMP(6)"
	case schema.DefaultUUID:
		return "(UUID())"
	case schema.DefaultExpr:
		return "(" + d.Value + ")"
	}
	lit := literal(c, mysqlString, "TRUE", "FALSE")
	switch c.Type.Kind {
	case schema.KindText, schema.KindJSON, schema.KindBytes:
		// these types only accept expression defaults
		return "(" + lit + ")"
	}
	return lit
}

func (m mysql) columnDef(c *schema.ColumnDef) string {
	var b strings.Builder
	b.WriteString(m.q(c.Name) + " " + m.typeSQL(c))
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.Default != nil {
		b.WriteString(" DEFAULT " + m.defaultSQL(c))
	}
	return b.String()
}

func (m mysql) CreateTable(t *schema.TableDef, inline []*schema.ForeignKeyDef) []string {
	var parts []string
	for _, c := range t.Columns {
		parts = append(parts, m.columnDef(c))
	}
	parts = append(parts, "PRIMARY KEY ("+quoteList(m, t.PrimaryKey)+")")
	for _, fk := range inline {
		parts = append(parts, foreignKeyClause(m, fk))
	}
	out := []string{createTableSQL(m, t.Name, parts)}
	for _, ix := range t.Indexes {
		out = append(out, createIndex(m, t.Name, ix))
	}
	return out
}

func (m mysql) AddForeignKey(table string, fk *schema.ForeignKeyDef) []string {
	return []string{m.alter(table) + "ADD " + foreignKeyClause(m, fk)}
}

func (m mysql) DropForeignKey(table string, fk *schema.ForeignKeyDef) []string {
	return []string{m.alter(table) + "DROP FOREIGN KEY " + m.q(fk.Name)}
}

func (m mysql) AlterTable(c *TableChange) ([]string, error) {
	table := c.New.Name
	var out []string
	for _, ix := range c.DropIndexes {
		out = append(out, "DROP INDEX "+m.q(ix.Name)+" ON "+m.q(table))
	}
	for _, col := range c.DropColumns {
		out = append(out, m.alter(table)+"DROP COLUMN "+m.q(col.Name))
	}
	for _, r := range c.RenameColumns {
		out = append(out, m.alter(table)+"RENAME COLUMN "+m.q(r.From)+" TO "+m.q(r.To))
	}
	for _, a := range c.AlterColumns {
		out = append(out, m.alter(table)+"MODIFY COLUMN "+m.columnDef(a.New))
	}
	for _, col := range c.AddColumns {
		out = append(out, m.alter(table)+"ADD COLUMN "+m.columnDef(col))
	}
	for _, ix := range c.AddIndexes {
		out = append(out, createIndex(m, table, ix))
	}
	return out, nil
}

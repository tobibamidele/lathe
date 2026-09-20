package ddl

import (
	"fmt"
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

// ColumnRename records a column that keeps its data under a new name.
type ColumnRename struct{ From, To string }

// ColumnAlter records a column whose definition changed.
type ColumnAlter struct{ Old, New *schema.ColumnDef }

// TableChange describes how an existing table changes. Old and New are the
// complete definitions before and after; Old already carries the table's new
// name if the table was renamed in the same migration.
type TableChange struct {
	Old, New *schema.TableDef

	AddColumns    []*schema.ColumnDef
	DropColumns   []*schema.ColumnDef
	RenameColumns []ColumnRename
	AlterColumns  []ColumnAlter
	AddIndexes    []*schema.IndexDef
	DropIndexes   []*schema.IndexDef

	// AddForeignKeys and DropForeignKeys are only populated for dialects that
	// cannot alter constraints in place.
	AddForeignKeys  []*schema.ForeignKeyDef
	DropForeignKeys []*schema.ForeignKeyDef
}

// Alter records a changed column.
func (c *TableChange) Alter(old, cur *schema.ColumnDef) {
	c.AlterColumns = append(c.AlterColumns, ColumnAlter{Old: old, New: cur})
}

// IsEmpty reports whether nothing changes.
func (c *TableChange) IsEmpty() bool {
	return len(c.AddColumns)+len(c.DropColumns)+len(c.RenameColumns)+len(c.AlterColumns)+
		len(c.AddIndexes)+len(c.DropIndexes)+len(c.AddForeignKeys)+len(c.DropForeignKeys) == 0
}

// Invert returns the change that undoes c.
func (c *TableChange) Invert() *TableChange {
	inv := &TableChange{
		Old:             c.New,
		New:             c.Old,
		AddColumns:      c.DropColumns,
		DropColumns:     c.AddColumns,
		AddIndexes:      c.DropIndexes,
		DropIndexes:     c.AddIndexes,
		AddForeignKeys:  c.DropForeignKeys,
		DropForeignKeys: c.AddForeignKeys,
	}
	for _, r := range c.RenameColumns {
		inv.RenameColumns = append(inv.RenameColumns, ColumnRename{From: r.To, To: r.From})
	}
	for _, a := range c.AlterColumns {
		inv.AlterColumns = append(inv.AlterColumns, ColumnAlter{Old: a.New, New: a.Old})
	}
	return inv
}

// Describe summarises the change for humans.
func (c *TableChange) Describe() string {
	var parts []string
	for _, r := range c.RenameColumns {
		parts = append(parts, fmt.Sprintf("rename column %s to %s", r.From, r.To))
	}
	for _, col := range c.AddColumns {
		parts = append(parts, "add column "+col.Name)
	}
	for _, col := range c.DropColumns {
		parts = append(parts, "drop column "+col.Name)
	}
	for _, a := range c.AlterColumns {
		parts = append(parts, "alter column "+a.New.Name)
	}
	for _, ix := range c.AddIndexes {
		parts = append(parts, "add index "+ix.Name)
	}
	for _, ix := range c.DropIndexes {
		parts = append(parts, "drop index "+ix.Name)
	}
	for _, fk := range c.AddForeignKeys {
		parts = append(parts, "add foreign key "+fk.Name)
	}
	for _, fk := range c.DropForeignKeys {
		parts = append(parts, "drop foreign key "+fk.Name)
	}
	return fmt.Sprintf("alter table %s: %s", c.New.Name, strings.Join(parts, ", "))
}

// Warnings lists changes that can lose data.
func (c *TableChange) Warnings() []string {
	var w []string
	for _, col := range c.DropColumns {
		w = append(w, fmt.Sprintf("drops column %s.%s and its data", c.New.Name, col.Name))
	}
	for _, a := range c.AlterColumns {
		if !a.Old.Type.Equal(a.New.Type) {
			w = append(w, fmt.Sprintf("changes the type of %s.%s; existing values are converted and may fail or lose precision", c.New.Name, a.New.Name))
		} else if a.Old.Nullable && !a.New.Nullable {
			w = append(w, fmt.Sprintf("makes %s.%s NOT NULL; the migration fails if NULLs exist", c.New.Name, a.New.Name))
		}
	}
	return w
}

// Step is one reversible unit of a migration plan.
type Step interface {
	SQL(d Dialect) ([]string, error)
	Inverse() Step
	Describe() string
	Warnings() []string
}

// CreateTable creates a table. InlineFKs are declared with the table; foreign
// keys that must wait for other tables are separate AddForeignKey steps.
type CreateTable struct {
	Table     *schema.TableDef
	InlineFKs []*schema.ForeignKeyDef
}

func (s CreateTable) SQL(d Dialect) ([]string, error) {
	return d.CreateTable(s.Table, s.InlineFKs), nil
}
func (s CreateTable) Inverse() Step      { return DropTable(s) }
func (s CreateTable) Describe() string   { return "create table " + s.Table.Name }
func (s CreateTable) Warnings() []string { return nil }

// DropTable drops a table. It carries the definition so it can be inverted.
type DropTable struct {
	Table     *schema.TableDef
	InlineFKs []*schema.ForeignKeyDef
}

func (s DropTable) SQL(d Dialect) ([]string, error) { return d.DropTable(s.Table.Name), nil }
func (s DropTable) Inverse() Step                   { return CreateTable(s) }
func (s DropTable) Describe() string                { return "drop table " + s.Table.Name }
func (s DropTable) Warnings() []string {
	return []string{fmt.Sprintf("drops table %s and all of its data", s.Table.Name)}
}

// RenameTable renames a table.
type RenameTable struct{ From, To string }

func (s RenameTable) SQL(d Dialect) ([]string, error) { return d.RenameTable(s.From, s.To), nil }
func (s RenameTable) Inverse() Step                   { return RenameTable{From: s.To, To: s.From} }
func (s RenameTable) Describe() string                { return fmt.Sprintf("rename table %s to %s", s.From, s.To) }
func (s RenameTable) Warnings() []string              { return nil }

// AlterTable applies a TableChange.
type AlterTable struct{ Change *TableChange }

func (s AlterTable) SQL(d Dialect) ([]string, error) { return d.AlterTable(s.Change) }
func (s AlterTable) Inverse() Step                   { return AlterTable{Change: s.Change.Invert()} }
func (s AlterTable) Describe() string                { return s.Change.Describe() }
func (s AlterTable) Warnings() []string              { return s.Change.Warnings() }

// AddForeignKey adds a constraint to an existing table.
type AddForeignKey struct {
	Table string
	FK    *schema.ForeignKeyDef
}

func (s AddForeignKey) SQL(d Dialect) ([]string, error) { return d.AddForeignKey(s.Table, s.FK), nil }
func (s AddForeignKey) Inverse() Step                   { return DropForeignKey(s) }
func (s AddForeignKey) Describe() string {
	return fmt.Sprintf("add foreign key %s on %s", s.FK.Name, s.Table)
}
func (s AddForeignKey) Warnings() []string { return nil }

// DropForeignKey drops a constraint from an existing table.
type DropForeignKey struct {
	Table string
	FK    *schema.ForeignKeyDef
}

func (s DropForeignKey) SQL(d Dialect) ([]string, error) { return d.DropForeignKey(s.Table, s.FK), nil }
func (s DropForeignKey) Inverse() Step                   { return AddForeignKey(s) }
func (s DropForeignKey) Describe() string {
	return fmt.Sprintf("drop foreign key %s on %s", s.FK.Name, s.Table)
}
func (s DropForeignKey) Warnings() []string { return nil }

// Package plan computes the difference between two schema snapshots and turns
// it into an ordered, reversible list of migration steps.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tobibamidele/lathe/internal/ddl"
	"github.com/tobibamidele/lathe/schema"
)

// Plan is the ordered list of steps that takes one snapshot to another.
type Plan struct {
	Steps []ddl.Step
}

// Empty reports whether the snapshots are equivalent.
func (p *Plan) Empty() bool { return len(p.Steps) == 0 }

// Describe returns a one line summary per step.
func (p *Plan) Describe() []string {
	out := make([]string, len(p.Steps))
	for i, s := range p.Steps {
		out[i] = s.Describe()
	}
	return out
}

// Warnings returns the data loss risks of the plan.
func (p *Plan) Warnings() []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Warnings()...)
	}
	return out
}

// Up renders the statements that apply the plan.
func (p *Plan) Up(d ddl.Dialect) ([]string, error) { return render(p.Steps, d) }

// Down renders the statements that undo the plan.
func (p *Plan) Down(d ddl.Dialect) ([]string, error) {
	inv := make([]ddl.Step, 0, len(p.Steps))
	for i := len(p.Steps) - 1; i >= 0; i-- {
		inv = append(inv, p.Steps[i].Inverse())
	}
	return render(inv, d)
}

func render(steps []ddl.Step, d ddl.Dialect) ([]string, error) {
	var out []string
	for _, s := range steps {
		stmts, err := s.SQL(d)
		if err != nil {
			return nil, err
		}
		out = append(out, stmts...)
	}
	return out, nil
}

// Diff plans the migration from old to cur. old may be nil (an empty schema).
func Diff(old, cur *schema.Snapshot, d ddl.Dialect) (*Plan, error) {
	if old == nil {
		old = &schema.Snapshot{Version: schema.SnapshotVersion, Dialect: cur.Dialect}
	}
	if old.Dialect != cur.Dialect {
		return nil, fmt.Errorf("the schema dialect changed from %s to %s; start a new migrations directory for the new database", old.Dialect, cur.Dialect)
	}

	// Table renames from RenamedFrom hints.
	tableRenames := map[string]string{} // old name -> new name
	for _, nt := range cur.Tables {
		if nt.RenamedFrom == "" || old.Table(nt.RenamedFrom) == nil || old.Table(nt.Name) != nil {
			continue // stale hint, or the rename already happened
		}
		if cur.Table(nt.RenamedFrom) != nil {
			return nil, fmt.Errorf("table %q is marked RenamedFrom(%q) but %q still exists in the schema", nt.Name, nt.RenamedFrom, nt.RenamedFrom)
		}
		tableRenames[nt.RenamedFrom] = nt.Name
	}
	newName := func(oldName string) string {
		if n, ok := tableRenames[oldName]; ok {
			return n
		}
		return oldName
	}

	oldFor := map[string]*schema.TableDef{} // new table name -> old definition
	matched := map[string]bool{}            // old table names that survive
	for _, ot := range old.Tables {
		if nt := cur.Table(newName(ot.Name)); nt != nil {
			oldFor[nt.Name] = ot
			matched[ot.Name] = true
		}
	}

	var renames, dropFKs, alters, addFKs []ddl.Step
	var created, dropped []*schema.TableDef
	for _, nt := range cur.Tables {
		if _, ok := oldFor[nt.Name]; !ok {
			created = append(created, nt)
		}
	}
	for _, ot := range old.Tables {
		if !matched[ot.Name] {
			dropped = append(dropped, ot)
		}
	}
	for _, from := range sortedKeys(tableRenames) {
		renames = append(renames, ddl.RenameTable{From: from, To: tableRenames[from]})
	}

	for _, nt := range cur.Tables {
		ot, ok := oldFor[nt.Name]
		if !ok {
			continue
		}
		ch, err := diffTable(ot, nt, tableRenames, d)
		if err != nil {
			return nil, err
		}
		if d.CanAlterForeignKeys() {
			for _, fk := range ch.fkDrops {
				// dropped before table renames run, so under the old name
				dropFKs = append(dropFKs, ddl.DropForeignKey{Table: ot.Name, FK: fk})
			}
			for _, fk := range ch.fkAdds {
				addFKs = append(addFKs, ddl.AddForeignKey{Table: nt.Name, FK: fk})
			}
		}
		if !ch.change.IsEmpty() {
			alters = append(alters, ddl.AlterTable{Change: ch.change})
		}
	}

	// Order matters, and the reverse plan (the down migration) inherits it:
	//  1. drop foreign keys that stop existing, so nothing blocks step 2 and 5
	//  2. drop removed tables, before renames: recreating them on the way down
	//     must happen after a renamed table has its old name back
	//  3. rename tables
	//  4. create new tables, referenced tables first
	//  5. alter existing tables
	//  6. add new foreign keys, once every column and index they need exists
	var steps []ddl.Step
	steps = append(steps, dropFKs...)
	// Dropping tables is the mirror image of creating them.
	drops := createSteps(dropped, d)
	for i := len(drops) - 1; i >= 0; i-- {
		steps = append(steps, drops[i].Inverse())
	}
	steps = append(steps, renames...)
	steps = append(steps, createSteps(created, d)...)
	steps = append(steps, alters...)
	steps = append(steps, addFKs...)
	return &Plan{Steps: steps}, nil
}

type tableDiff struct {
	change  *ddl.TableChange
	fkAdds  []*schema.ForeignKeyDef
	fkDrops []*schema.ForeignKeyDef
}

func diffTable(old, cur *schema.TableDef, tableRenames map[string]string, d ddl.Dialect) (*tableDiff, error) {
	// The old definition, as it looks once the table rename has run.
	oldNow := old.Clone()
	oldNow.Name = cur.Name

	// Column renames.
	colRenames := map[string]string{} // old -> new
	for _, nc := range cur.Columns {
		if nc.RenamedFrom == "" || old.Column(nc.RenamedFrom) == nil || old.Column(nc.Name) != nil {
			continue
		}
		if cur.Column(nc.RenamedFrom) != nil {
			return nil, fmt.Errorf("table %q: column %q is marked RenamedFrom(%q) but %q still exists", cur.Name, nc.Name, nc.RenamedFrom, nc.RenamedFrom)
		}
		colRenames[nc.RenamedFrom] = nc.Name
	}
	mapCol := func(name string) string {
		if n, ok := colRenames[name]; ok {
			return n
		}
		return name
	}
	mapCols := func(cols []string) []string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = mapCol(c)
		}
		return out
	}

	ch := &ddl.TableChange{Old: oldNow, New: cur}
	oldFor := map[string]*schema.ColumnDef{}
	for _, oc := range old.Columns {
		if nc := cur.Column(mapCol(oc.Name)); nc != nil {
			oldFor[nc.Name] = oc
		} else {
			ch.DropColumns = append(ch.DropColumns, oc)
		}
	}
	for _, from := range sortedKeys(colRenames) {
		ch.RenameColumns = append(ch.RenameColumns, ddl.ColumnRename{From: from, To: colRenames[from]})
	}
	for _, nc := range cur.Columns {
		oc, ok := oldFor[nc.Name]
		switch {
		case !ok:
			ch.AddColumns = append(ch.AddColumns, nc)
		case !oc.SameDDL(nc):
			ch.Alter(oc, nc)
		}
	}

	if !sameList(mapCols(old.PrimaryKey), cur.PrimaryKey) {
		return nil, fmt.Errorf("table %q: the primary key changed from (%s) to (%s); this cannot be migrated automatically. "+
			"Write the migration by hand with `lathe migrate new` and refresh the snapshot with `lathe migrate snapshot`",
			cur.Name, strings.Join(old.PrimaryKey, ", "), strings.Join(cur.PrimaryKey, ", "))
	}

	// Indexes: compare by name, after mapping renamed columns.
	for _, oi := range old.Indexes {
		mapped := *oi
		mapped.Columns = mapCols(oi.Columns)
		ni := cur.Index(oi.Name)
		if ni == nil || !mapped.Equal(ni) {
			ch.DropIndexes = append(ch.DropIndexes, oi)
		}
	}
	for _, ni := range cur.Indexes {
		oi := old.Index(ni.Name)
		if oi == nil {
			ch.AddIndexes = append(ch.AddIndexes, ni)
			continue
		}
		mapped := *oi
		mapped.Columns = mapCols(oi.Columns)
		if !mapped.Equal(ni) {
			ch.AddIndexes = append(ch.AddIndexes, ni)
		}
	}

	// Foreign keys, after mapping renamed columns and renamed target tables.
	td := &tableDiff{change: ch}
	var fkDrops, fkAdds []*schema.ForeignKeyDef
	for _, of := range old.ForeignKeys {
		mapped := *of
		mapped.Columns = mapCols(of.Columns)
		if n, ok := tableRenames[of.RefTable]; ok {
			mapped.RefTable = n
		}
		// referenced columns may have been renamed in the target table too;
		// that shows up as a difference and is handled as drop + add.
		nf := cur.ForeignKey(of.Name)
		if nf == nil || !mapped.Equal(nf) {
			fkDrops = append(fkDrops, of)
		}
	}
	for _, nf := range cur.ForeignKeys {
		of := old.ForeignKey(nf.Name)
		if of == nil {
			fkAdds = append(fkAdds, nf)
			continue
		}
		mapped := *of
		mapped.Columns = mapCols(of.Columns)
		if n, ok := tableRenames[of.RefTable]; ok {
			mapped.RefTable = n
		}
		if !mapped.Equal(nf) {
			fkAdds = append(fkAdds, nf)
		}
	}
	if d.CanAlterForeignKeys() {
		td.fkDrops, td.fkAdds = fkDrops, fkAdds
	} else {
		ch.DropForeignKeys, ch.AddForeignKeys = fkDrops, fkAdds
	}
	return td, nil
}

// createSteps orders table creation so that referenced tables come first.
// Foreign keys that cannot be satisfied inline (cycles) become AddForeignKey
// steps after every table exists, on databases that can alter constraints.
func createSteps(tables []*schema.TableDef, d ddl.Dialect) []ddl.Step {
	sorted := append([]*schema.TableDef(nil), tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	inSet := map[string]bool{}
	for _, t := range sorted {
		inSet[t.Name] = true
	}
	done := map[string]bool{}
	ready := func(t *schema.TableDef) bool {
		for _, fk := range t.ForeignKeys {
			if fk.RefTable != t.Name && inSet[fk.RefTable] && !done[fk.RefTable] {
				return false
			}
		}
		return true
	}

	var steps, deferred []ddl.Step
	remaining := sorted
	for len(remaining) > 0 {
		pick := -1
		for i, t := range remaining {
			if ready(t) {
				pick = i
				break
			}
		}
		if pick < 0 {
			pick = 0 // a cycle: break it at the alphabetically first table
		}
		t := remaining[pick]
		remaining = append(append([]*schema.TableDef(nil), remaining[:pick]...), remaining[pick+1:]...)

		var inline []*schema.ForeignKeyDef
		for _, fk := range t.ForeignKeys {
			// Databases that cannot ALTER constraints (SQLite) resolve
			// references lazily, so every foreign key can be declared inline.
			if !d.CanAlterForeignKeys() || fk.RefTable == t.Name || !inSet[fk.RefTable] || done[fk.RefTable] {
				inline = append(inline, fk)
			} else {
				deferred = append(deferred, ddl.AddForeignKey{Table: t.Name, FK: fk})
			}
		}
		steps = append(steps, ddl.CreateTable{Table: t, InlineFKs: inline})
		done[t.Name] = true
	}
	return append(steps, deferred...)
}

func sameList(a, b []string) bool {
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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package importer

import (
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

// finalize turns the applied migration state into a snapshot lathe can either
// load or tell the user how to fix. Most problems are warnings: an importer
// should still produce a useful file when a migration uses a feature lathe
// does not model.
func (s *state) finalize() {
	for _, name := range append([]string(nil), s.order...) {
		t := s.table(name)
		if t == nil {
			continue
		}
		if !validIdent(t.Name) {
			s.warn("table %q: not a valid lathe/Go identifier; skipped", t.Name)
			s.removeTable(t.Name)
			continue
		}
		if len(t.Columns) == 0 {
			s.warn("table %q has no columns; skipped", t.Name)
			s.removeTable(t.Name)
			continue
		}

		// Columns with names lathe cannot generate Go fields for are dropped,
		// along with any index or constraint that depends on them.
		for _, col := range append([]*schema.ColumnDef(nil), t.Columns...) {
			if !validIdent(col.Name) {
				s.warn("table %q column %q: not a valid lathe/Go identifier; skipped", t.Name, col.Name)
				s.dropColumn(t, col.Name)
			}
		}
		if len(t.Columns) == 0 {
			s.warn("table %q has no usable columns; skipped", t.Name)
			s.removeTable(t.Name)
			continue
		}

		s.resolveEnumColumns(t)
		s.fixPrimaryKey(t)
		s.fixDefaults(t)
		s.pruneIndexes(t)
	}

	// Foreign keys are pruned last so every table has its final shape.
	for _, name := range s.order {
		if t := s.tables[name]; t != nil {
			s.pruneForeignKeys(t)
		}
	}

	snap := &schema.Snapshot{Version: schema.SnapshotVersion, Dialect: s.dialect, Tables: s.tablesInOrder()}
	if err := schema.Validate(snap); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				s.warn("schema validation: %s", line)
			}
		}
	}
}

func validIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (s *state) resolveEnumColumns(t *schema.TableDef) {
	for _, col := range t.Columns {
		if col.Type.Kind != schema.KindEnum || len(col.Type.Values) > 0 {
			continue
		}
		if name := s.enumOf[col]; name != "" {
			if e := s.enum(name); e != nil {
				col.Type.Values = append([]string(nil), e.values...)
				continue
			}
			s.warn("table %q column %q: enum type %q is never defined; imported as text", t.Name, col.Name, name)
		} else {
			s.warn("table %q column %q: enum has no values; imported as text", t.Name, col.Name)
		}
		col.Type = schema.Type{Kind: schema.KindText}
		delete(s.enumOf, col)
	}
}

func (s *state) fixPrimaryKey(t *schema.TableDef) {
	var pk []string
	seen := map[string]bool{}
	for _, name := range t.PrimaryKey {
		if t.Column(name) == nil {
			s.warn("table %q: primary key references missing column %q; removed", t.Name, name)
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		pk = append(pk, name)
	}
	t.PrimaryKey = pk

	auto := s.autoIncrementColumn(t)
	if len(t.PrimaryKey) == 0 && auto != nil {
		s.warn("table %q has no primary key; using auto-increment column %q", t.Name, auto.Name)
		t.PrimaryKey = []string{auto.Name}
	}
	if len(t.PrimaryKey) == 0 {
		s.warn("table %q has no primary key; the generated schema will not load until you add one", t.Name)
	}
	for _, name := range t.PrimaryKey {
		if c := t.Column(name); c != nil {
			c.Nullable = false
		}
	}
	if auto != nil {
		if !auto.Type.Kind.IsInteger() {
			s.warn("table %q column %q: AutoIncrement needs an integer column; cleared", t.Name, auto.Name)
			auto.AutoIncrement = false
		} else if len(t.PrimaryKey) != 1 || t.PrimaryKey[0] != auto.Name {
			s.warn("table %q column %q: AutoIncrement needs to be the only primary key column; AutoIncrement cleared", t.Name, auto.Name)
			auto.AutoIncrement = false
		} else {
			auto.Nullable = false
			auto.Default = nil
		}
	}
	// SQLite's INTEGER PRIMARY KEY is a rowid alias even without AUTOINCREMENT.
	if s.dialect == schema.SQLite && len(t.PrimaryKey) == 1 && auto == nil {
		if c := t.Column(t.PrimaryKey[0]); c != nil && c.Type.Kind == schema.KindInt && c.Default == nil {
			c.AutoIncrement = true
		}
	}
}

func (s *state) autoIncrementColumn(t *schema.TableDef) *schema.ColumnDef {
	var found *schema.ColumnDef
	for _, c := range t.Columns {
		if !c.AutoIncrement {
			continue
		}
		if found != nil {
			return found
		}
		found = c
	}
	return found
}

func (s *state) fixDefaults(t *schema.TableDef) {
	for _, c := range t.Columns {
		d := c.Default
		if d == nil {
			continue
		}
		if c.AutoIncrement {
			c.Default = nil
			continue
		}
		switch d.Kind {
		case schema.DefaultNow:
			if c.Type.Kind != schema.KindTimestamp && c.Type.Kind != schema.KindDate {
				c.Default = nil
				s.warn("table %q column %q: now default is not valid for %s; removed", t.Name, c.Name, c.Type.Kind)
			}
		case schema.DefaultUUID:
			if c.Type.Kind != schema.KindUUID {
				c.Default = nil
				s.warn("table %q column %q: UUID default is not valid for %s; removed", t.Name, c.Name, c.Type.Kind)
			}
		case schema.DefaultLiteral:
			if c.Type.Kind == schema.KindEnum {
				found := false
				for _, v := range c.Type.Values {
					found = found || v == d.Value
				}
				if !found {
					s.warn("table %q column %q: default %q is not an enum value; removed", t.Name, c.Name, d.Value)
					c.Default = nil
				}
			}
		}
	}
}

func (s *state) pruneIndexes(t *schema.TableDef) {
	names := map[string]bool{}
	kept := t.Indexes[:0]
	for _, ix := range t.Indexes {
		bad := false
		for _, cn := range ix.Columns {
			if t.Column(cn) == nil {
				s.warn("table %q: index %q references missing column %q; skipped", t.Name, ix.Name, cn)
				bad = true
				break
			}
		}
		if bad || len(ix.Columns) == 0 {
			continue
		}
		if names[ix.Name] {
			s.warn("table %q: index name %q is used more than once; keeping the first", t.Name, ix.Name)
			continue
		}
		names[ix.Name] = true
		kept = append(kept, ix)
	}
	t.Indexes = kept
}

func (s *state) pruneForeignKeys(t *schema.TableDef) {
	names := map[string]bool{}
	shapes := map[string]bool{}
	kept := t.ForeignKeys[:0]
	for _, fk := range t.ForeignKeys {
		bad := false
		if fk.Name == "" {
			fk.Name = schema.AutoName("fk", t.Name, fk.Columns...)
		}
		if names[fk.Name] {
			s.warn("table %q: foreign key name %q is used more than once; keeping the first", t.Name, fk.Name)
			continue
		}
		for _, cn := range fk.Columns {
			if t.Column(cn) == nil {
				s.warn("table %q: foreign key %q references missing local column %q; skipped", t.Name, fk.Name, cn)
				bad = true
				break
			}
		}
		ref := s.table(fk.RefTable)
		if ref == nil {
			s.warn("table %q: foreign key %q references missing table %q; skipped", t.Name, fk.Name, fk.RefTable)
			continue
		}
		if !bad && len(fk.RefColumns) == 0 {
			fk.RefColumns = append([]string(nil), ref.PrimaryKey...)
		}
		if len(fk.RefColumns) != len(fk.Columns) || len(fk.RefColumns) == 0 {
			s.warn("table %q: foreign key %q has mismatched columns; skipped", t.Name, fk.Name)
			continue
		}
		for _, cn := range fk.RefColumns {
			if ref.Column(cn) == nil {
				s.warn("table %q: foreign key %q references missing column %s.%s; skipped", t.Name, fk.Name, ref.Name, cn)
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		if !coveredByKey(ref, fk.RefColumns) {
			s.warn("table %q: foreign key %q references (%s), which is not a key of %q; skipped",
				t.Name, fk.Name, strings.Join(fk.RefColumns, ", "), ref.Name)
			continue
		}
		if reason := fkTypeMismatch(t, ref, fk); reason != "" {
			s.warn("table %q: foreign key %q: %s; skipped", t.Name, fk.Name, reason)
			continue
		}
		// Several migrations sometimes add the same constraint twice under
		// different names. lathe derives one relation per foreign key, so a
		// duplicate would make generation fail.
		shape := strings.Join(fk.Columns, ",") + "->" + fk.RefTable + "(" + strings.Join(fk.RefColumns, ",") + ")" +
			string(fk.OnDelete) + "/" + string(fk.OnUpdate)
		if shapes[shape] {
			s.warn("table %q: foreign key %q duplicates an earlier constraint; keeping the first", t.Name, fk.Name)
			continue
		}
		shapes[shape] = true
		names[fk.Name] = true
		kept = append(kept, fk)
	}
	t.ForeignKeys = kept
}

func fkTypeMismatch(t, ref *schema.TableDef, fk *schema.ForeignKeyDef) string {
	for i, cn := range fk.Columns {
		local, target := t.Column(cn), ref.Column(fk.RefColumns[i])
		if local == nil || target == nil {
			continue
		}
		if local.Type.Kind != target.Type.Kind {
			return "column " + cn + " is " + string(local.Type.Kind) + " but " + ref.Name + "." + target.Name + " is " + string(target.Type.Kind)
		}
		if local.Type.Kind == schema.KindVarChar && local.Type.Length != target.Type.Length {
			return "column " + cn + " has a different VARCHAR length than " + ref.Name + "." + target.Name
		}
	}
	return ""
}

func coveredByKey(t *schema.TableDef, cols []string) bool {
	if sameSet(t.PrimaryKey, cols) {
		return true
	}
	for _, ix := range t.Indexes {
		if ix.Unique && sameSet(ix.Columns, cols) {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

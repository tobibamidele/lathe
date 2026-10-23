package importer

import (
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

func (s *state) alterTable(p *parser) {
	p.acceptWord("IF")
	p.acceptWord("EXISTS")
	p.acceptWord("ONLY")
	name, ok := p.qualifiedIdent()
	if !ok {
		s.warn("ALTER TABLE without a name; skipped")
		return
	}
	t := s.table(name)
	if t == nil {
		s.warn("ALTER TABLE %q: table does not exist; skipped", name)
		return
	}
	for _, act := range splitTopLevel(p.rest(), ",") {
		if len(act) == 0 {
			continue
		}
		s.alterAction(t, &parser{toks: act})
	}
}

func (s *state) alterAction(t *schema.TableDef, p *parser) {
	switch {
	case p.acceptWord("ADD"):
		switch {
		case p.acceptWord("COLUMN"):
			s.addColumnAction(t, p)
		case isTableConstraintStart(p):
			s.tableConstraint(t, p)
		default:
			s.addColumnAction(t, p)
		}
	case p.acceptWord("DROP"):
		s.dropAction(t, p)
	case p.acceptWord("ALTER"):
		p.acceptWord("COLUMN")
		s.alterColumnAction(t, p)
	case p.acceptWord("MODIFY"):
		p.acceptWord("COLUMN")
		name, ok := p.ident()
		if !ok {
			s.warn("table %q: MODIFY COLUMN without a name; ignored", t.Name)
			return
		}
		s.replaceColumn(t, "", name, p)
	case p.acceptWord("CHANGE"):
		p.acceptWord("COLUMN")
		oldName, ok := p.ident()
		if !ok {
			s.warn("table %q: CHANGE COLUMN without a name; ignored", t.Name)
			return
		}
		newName, ok := p.ident()
		if !ok {
			s.warn("table %q: CHANGE COLUMN %q without a new name; ignored", t.Name, oldName)
			return
		}
		s.replaceColumn(t, oldName, newName, p)
	case p.acceptWord("RENAME"):
		s.renameAction(t, p)
	case p.acceptWord("SET"), p.acceptWord("OWNER"), p.acceptWord("CLUSTER"),
		p.acceptWord("ENABLE"), p.acceptWord("DISABLE"), p.acceptWord("VALIDATE"),
		p.acceptWord("REPLICA"), p.acceptWord("RESET"), p.acceptWord("INHERIT"),
		p.acceptWord("NO"), p.acceptWord("OF"), p.acceptWord("NOT"):
		// Storage, ownership, triggers and constraint validation do not change
		// the shape lathe models.
	default:
		// Unknown action; ignore.
	}
}

func (s *state) addColumnAction(t *schema.TableDef, p *parser) {
	ifNotExists := false
	if p.acceptWord("IF") {
		p.acceptWord("NOT")
		p.acceptWord("EXISTS")
		ifNotExists = true
	}
	pc := s.columnDef(p, t.Name)
	if pc == nil {
		return
	}
	if existing := t.Column(pc.col.Name); existing != nil {
		if ifNotExists {
			return
		}
		s.warn("table %q: column %q already exists; replacing it", t.Name, pc.col.Name)
		delete(s.enumOf, existing)
		*existing = *pc.col
	} else {
		t.Columns = append(t.Columns, pc.col)
	}
	if pc.pk && !containsName(t.PrimaryKey, pc.col.Name) {
		t.PrimaryKey = append(t.PrimaryKey, pc.col.Name)
		pc.col.Nullable = false
	}
	for _, ix := range pc.indexes {
		s.addIndex(t, ix)
	}
	for _, fk := range pc.fks {
		s.addForeignKey(t, fk)
	}
}

// replaceColumn implements MySQL MODIFY / CHANGE. oldName may be empty when the
// column keeps its name.
func (s *state) replaceColumn(t *schema.TableDef, oldName, newName string, p *parser) {
	pc := s.columnDefAfterName(p, t.Name, newName)
	if oldName == "" {
		oldName = newName
	}
	old := t.Column(oldName)
	if old == nil {
		if t.Column(newName) != nil {
			s.warn("table %q: column %q already exists; replacing it", t.Name, newName)
			old = t.Column(newName)
		} else {
			s.warn("table %q: column %q does not exist; adding it", t.Name, oldName)
			t.Columns = append(t.Columns, pc.col)
			s.applyParsedColumnExtras(t, pc)
			return
		}
	}
	delete(s.enumOf, old)
	*old = *pc.col
	if oldName != newName {
		s.renameColumnRefs(t, oldName, newName)
	}
	s.applyParsedColumnExtras(t, pc)
}

func (s *state) applyParsedColumnExtras(t *schema.TableDef, pc *parsedColumn) {
	if pc.pk && !containsName(t.PrimaryKey, pc.col.Name) {
		t.PrimaryKey = append(t.PrimaryKey, pc.col.Name)
		pc.col.Nullable = false
	}
	for _, ix := range pc.indexes {
		s.addIndex(t, ix)
	}
	for _, fk := range pc.fks {
		s.addForeignKey(t, fk)
	}
}

func (s *state) alterColumnAction(t *schema.TableDef, p *parser) {
	name, ok := p.ident()
	if !ok {
		s.warn("table %q: ALTER COLUMN without a name; ignored", t.Name)
		return
	}
	col := t.Column(name)
	if col == nil {
		s.warn("table %q: ALTER COLUMN %q: column does not exist; ignored", t.Name, name)
		return
	}
	switch {
	case p.acceptWord("SET"):
		switch {
		case p.acceptWord("DEFAULT"):
			s.applyDefault(col, p.parseDefaultExpr())
		case p.acceptWord("NOT"):
			p.acceptWord("NULL")
			col.Nullable = false
		case p.acceptWord("DATA"):
			p.acceptWord("TYPE")
			s.setColumnType(t, col, p)
		default:
			// SET STATISTICS, SET STORAGE, SET (attribute_option), ...
		}
	case p.acceptWord("DROP"):
		switch {
		case p.acceptWord("DEFAULT"):
			col.Default = nil
		case p.acceptWord("NOT"):
			p.acceptWord("NULL")
			col.Nullable = true
		case p.acceptWord("IDENTITY"):
			col.AutoIncrement = false
		default:
			// DROP EXPRESSION and similar
		}
	case p.acceptWord("TYPE"):
		s.setColumnType(t, col, p)
	case p.acceptWord("ADD"):
		if p.acceptWord("GENERATED") {
			p.acceptWord("ALWAYS")
			p.acceptWord("BY")
			p.acceptWord("DEFAULT")
			p.acceptWord("AS")
			p.acceptWord("IDENTITY")
			if p.isPunct("(") {
				p.next()
				p.parenBody()
			}
			col.AutoIncrement = true
			col.Nullable = false
			col.Default = nil
		}
	case p.acceptWord("RESTART"), p.acceptWord("RESET"):
		// sequence options
	}
}

func (s *state) setColumnType(t *schema.TableDef, col *schema.ColumnDef, p *parser) {
	ti := s.parseType(p)
	if ti.warning != "" {
		s.warn("table %q column %q: %s", t.Name, col.Name, ti.warning)
	}
	col.Type = ti.typ
	delete(s.enumOf, col)
	if ti.enumName != "" {
		s.enumOf[col] = ti.enumName
	}
	if ti.autoIncrement {
		col.AutoIncrement = true
		col.Nullable = false
	}
}

func (s *state) dropAction(t *schema.TableDef, p *parser) {
	switch {
	case p.acceptWord("COLUMN"):
		p.acceptWord("IF")
		p.acceptWord("EXISTS")
		name, ok := p.ident()
		if !ok {
			s.warn("table %q: DROP COLUMN without a name; ignored", t.Name)
			return
		}
		s.dropColumn(t, name)
	case p.acceptWord("CONSTRAINT"):
		p.acceptWord("IF")
		p.acceptWord("EXISTS")
		name, ok := p.ident()
		if !ok {
			s.warn("table %q: DROP CONSTRAINT without a name; ignored", t.Name)
			return
		}
		s.dropConstraint(t, name)
	case p.acceptWord("PRIMARY"):
		p.acceptWord("KEY")
		t.PrimaryKey = nil
	case p.acceptWord("INDEX"), p.acceptWord("KEY"):
		name, ok := p.ident()
		if !ok {
			s.warn("table %q: DROP INDEX without a name; ignored", t.Name)
			return
		}
		s.dropIndexFrom(t, name)
	case p.acceptWord("FOREIGN"):
		p.acceptWord("KEY")
		name, ok := p.ident()
		if !ok {
			s.warn("table %q: DROP FOREIGN KEY without a name; ignored", t.Name)
			return
		}
		s.dropForeignKeyFrom(t, name)
	default:
		// DROP DEFAULT / DROP NOT NULL / DROP IDENTITY only after ALTER COLUMN.
	}
}

func (s *state) renameAction(t *schema.TableDef, p *parser) {
	switch {
	case p.acceptWord("COLUMN"):
		oldName, ok := p.ident()
		if !ok || !p.acceptWord("TO") {
			s.warn("table %q: malformed RENAME COLUMN; ignored", t.Name)
			return
		}
		newName, ok := p.ident()
		if !ok {
			s.warn("table %q: RENAME COLUMN %q without a new name; ignored", t.Name, oldName)
			return
		}
		s.renameColumn(t, oldName, newName)
	case p.acceptWord("CONSTRAINT"):
		oldName, ok := p.ident()
		if !ok || !p.acceptWord("TO") {
			s.warn("table %q: malformed RENAME CONSTRAINT; ignored", t.Name)
			return
		}
		newName, ok := p.ident()
		if !ok {
			return
		}
		for _, fk := range t.ForeignKeys {
			if fk.Name == oldName {
				fk.Name = newName
				return
			}
		}
		for _, ix := range t.Indexes {
			if ix.Name == oldName {
				ix.Name = newName
				return
			}
		}
		s.warn("table %q: constraint %q does not exist", t.Name, oldName)
	case p.acceptWord("INDEX"), p.acceptWord("KEY"):
		oldName, ok := p.ident()
		if !ok || !p.acceptWord("TO") {
			s.warn("table %q: malformed RENAME INDEX; ignored", t.Name)
			return
		}
		newName, ok := p.ident()
		if !ok {
			return
		}
		for _, ix := range t.Indexes {
			if ix.Name == oldName {
				ix.Name = newName
				return
			}
		}
		s.warn("table %q: index %q does not exist", t.Name, oldName)
	default:
		// RENAME TO new
		p.acceptWord("TO")
		newName, ok := p.qualifiedIdent()
		if !ok {
			s.warn("table %q: malformed RENAME TO; ignored", t.Name)
			return
		}
		s.renameTable(t.Name, newName)
	}
}

func (s *state) renameTable(oldName, newName string) {
	t := s.table(oldName)
	if t == nil {
		s.warn("RENAME TABLE %q: table does not exist; ignored", oldName)
		return
	}
	if other := s.table(newName); other != nil && other != t {
		s.warn("RENAME TABLE %q: table %q already exists; ignored", oldName, newName)
		return
	}
	old := t.Name
	delete(s.tables, old)
	t.Name = newName
	s.tables[newName] = t
	for i, n := range s.order {
		if n == old {
			s.order[i] = newName
			break
		}
	}
	for _, other := range s.tables {
		for _, fk := range other.ForeignKeys {
			if strings.EqualFold(fk.RefTable, old) {
				fk.RefTable = newName
			}
		}
	}
}

func (s *state) renameColumn(t *schema.TableDef, oldName, newName string) {
	if t.Column(newName) != nil {
		s.warn("table %q: column %q already exists; cannot rename %q", t.Name, newName, oldName)
		return
	}
	col := t.Column(oldName)
	if col == nil {
		s.warn("table %q: column %q does not exist; cannot rename", t.Name, oldName)
		return
	}
	col.Name = newName
	s.renameColumnRefs(t, oldName, newName)
}

// renameColumnRefs updates every key, index and foreign key that names oldName
// as a column of t, plus foreign keys elsewhere that reference the column.
func (s *state) renameColumnRefs(t *schema.TableDef, oldName, newName string) {
	for i, n := range t.PrimaryKey {
		if n == oldName {
			t.PrimaryKey[i] = newName
		}
	}
	for _, ix := range t.Indexes {
		for i, n := range ix.Columns {
			if n == oldName {
				ix.Columns[i] = newName
			}
		}
	}
	for _, fk := range t.ForeignKeys {
		for i, n := range fk.Columns {
			if n == oldName {
				fk.Columns[i] = newName
			}
		}
	}
	for _, other := range s.tables {
		for _, fk := range other.ForeignKeys {
			if !strings.EqualFold(fk.RefTable, t.Name) {
				continue
			}
			for i, n := range fk.RefColumns {
				if n == oldName {
					fk.RefColumns[i] = newName
				}
			}
		}
	}
}

func (s *state) dropColumn(t *schema.TableDef, name string) {
	col := t.Column(name)
	if col == nil {
		s.warn("table %q: column %q does not exist; cannot drop", t.Name, name)
		return
	}
	idx := -1
	for i, c := range t.Columns {
		if c == col {
			idx = i
			break
		}
	}
	t.Columns = append(t.Columns[:idx], t.Columns[idx+1:]...)
	delete(s.enumOf, col)

	keptPK := t.PrimaryKey[:0]
	for _, n := range t.PrimaryKey {
		if n != name {
			keptPK = append(keptPK, n)
		}
	}
	t.PrimaryKey = keptPK

	keptIdx := t.Indexes[:0]
	for _, ix := range t.Indexes {
		if containsName(ix.Columns, name) {
			s.warn("table %q: dropping index %q because its column %q is dropped", t.Name, ix.Name, name)
			continue
		}
		keptIdx = append(keptIdx, ix)
	}
	t.Indexes = keptIdx

	keptFK := t.ForeignKeys[:0]
	for _, fk := range t.ForeignKeys {
		if containsName(fk.Columns, name) {
			s.warn("table %q: dropping foreign key %q because its column %q is dropped", t.Name, fk.Name, name)
			continue
		}
		keptFK = append(keptFK, fk)
	}
	t.ForeignKeys = keptFK

	for _, other := range s.tables {
		kept := other.ForeignKeys[:0]
		for _, fk := range other.ForeignKeys {
			if strings.EqualFold(fk.RefTable, t.Name) && containsName(fk.RefColumns, name) {
				s.warn("table %q: dropping foreign key %q because %s.%s is dropped", other.Name, fk.Name, t.Name, name)
				continue
			}
			kept = append(kept, fk)
		}
		other.ForeignKeys = kept
	}
}

func (s *state) dropConstraint(t *schema.TableDef, name string) {
	for i, fk := range t.ForeignKeys {
		if fk.Name == name {
			t.ForeignKeys = append(t.ForeignKeys[:i], t.ForeignKeys[i+1:]...)
			return
		}
	}
	for i, ix := range t.Indexes {
		if ix.Name == name {
			t.Indexes = append(t.Indexes[:i], t.Indexes[i+1:]...)
			return
		}
	}
	s.warn("table %q: constraint %q does not exist", t.Name, name)
}

func (s *state) dropIndexFrom(t *schema.TableDef, name string) {
	for i, ix := range t.Indexes {
		if ix.Name == name {
			t.Indexes = append(t.Indexes[:i], t.Indexes[i+1:]...)
			return
		}
	}
	s.warn("table %q: index %q does not exist", t.Name, name)
}

func (s *state) dropForeignKeyFrom(t *schema.TableDef, name string) {
	for i, fk := range t.ForeignKeys {
		if fk.Name == name {
			t.ForeignKeys = append(t.ForeignKeys[:i], t.ForeignKeys[i+1:]...)
			return
		}
	}
	s.warn("table %q: foreign key %q does not exist", t.Name, name)
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

package importer

import (
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

// parsedColumn is a column plus the constraints that were declared inline.
type parsedColumn struct {
	col     *schema.ColumnDef
	pk      bool
	indexes []*schema.IndexDef
	fks     []*schema.ForeignKeyDef
}

func (s *state) createTable(p *parser) {
	ifNotExists := false
	if p.acceptWord("IF") {
		p.acceptWord("NOT")
		p.acceptWord("EXISTS")
		ifNotExists = true
	}
	name, ok := p.qualifiedIdent()
	if !ok {
		s.warn("CREATE TABLE without a name; skipped")
		return
	}
	if !p.isPunct("(") {
		s.warn("table %q: CREATE TABLE ... AS / LIKE is not supported; skipped", name)
		return
	}
	p.next()
	body, ok := p.parenBody()
	if !ok {
		s.warn("table %q: unbalanced CREATE TABLE column list; skipped", name)
		return
	}
	if existing := s.table(name); existing != nil {
		if ifNotExists {
			return
		}
		s.warn("table %q is created more than once; replacing the earlier definition", name)
		s.removeTable(existing.Name)
	}

	t := &schema.TableDef{Name: name}
	var inlinePK []string
	for _, seg := range splitTopLevel(body, ",") {
		if len(seg) == 0 {
			continue
		}
		sp := &parser{toks: seg}
		if isTableConstraintStart(sp) {
			s.tableConstraint(t, sp)
			continue
		}
		pc := s.columnDef(sp, name)
		if pc == nil {
			continue
		}
		if !s.addColumn(t, pc.col) {
			continue
		}
		if pc.pk {
			inlinePK = append(inlinePK, pc.col.Name)
		}
		for _, ix := range pc.indexes {
			s.addIndex(t, ix)
		}
		for _, fk := range pc.fks {
			s.addForeignKey(t, fk)
		}
	}
	if len(inlinePK) > 0 {
		if len(t.PrimaryKey) > 0 && !sameNames(t.PrimaryKey, inlinePK) {
			s.warn("table %q: primary key is declared more than once; using (%s)", name, strings.Join(t.PrimaryKey, ", "))
		} else {
			t.PrimaryKey = inlinePK
		}
	}
	s.tables[name] = t
	s.order = append(s.order, name)
}

// columnDef parses one column definition. table is used for deterministic
// constraint names.
func (s *state) columnDef(p *parser, table string) *parsedColumn {
	name, ok := p.ident()
	if !ok {
		s.warn("table %q: column definition has no name; skipped", table)
		return nil
	}
	return s.columnDefAfterName(p, table, name)
}

func (s *state) columnDefAfterName(p *parser, table, name string) *parsedColumn {
	ti := s.parseType(p)
	if ti.warning != "" {
		s.warn("table %q column %q: %s", table, name, ti.warning)
	}
	col := &schema.ColumnDef{Name: name, Type: ti.typ, Nullable: true}
	if ti.enumName != "" {
		s.enumOf[col] = ti.enumName
	}
	if ti.autoIncrement {
		col.AutoIncrement = true
		col.Nullable = false
	}
	pc := &parsedColumn{col: col}
	pendingName := ""

	for !p.done() {
		switch {
		case p.acceptWord("NOT"):
			if p.acceptWord("NULL") {
				col.Nullable = false
			} else {
				s.warn("table %q column %q: expected NULL after NOT; ignored", table, name)
			}
		case p.acceptWord("NULL"):
			col.Nullable = true
		case p.acceptWord("PRIMARY"):
			p.acceptWord("KEY")
			pc.pk = true
			col.Nullable = false
		case p.acceptWord("UNIQUE"):
			p.acceptWord("KEY")
			ixName := pendingName
			pendingName = ""
			pc.indexes = append(pc.indexes, &schema.IndexDef{Name: ixName, Columns: []string{name}, Unique: true})
		case p.acceptWord("DEFAULT"):
			s.applyDefault(col, p.parseDefaultExpr())
		case p.acceptWord("REFERENCES"):
			fk := s.references(p, []string{name}, pendingName)
			pendingName = ""
			if fk != nil {
				pc.fks = append(pc.fks, fk)
			}
		case p.acceptWord("CHECK"):
			p.next()
			if p.isPunct("(") {
				p.parenBody()
			}
			s.warn("table %q column %q: CHECK constraint is not supported by lathe; ignored", table, name)
		case p.acceptWord("CONSTRAINT"):
			n, ok := p.ident()
			if ok {
				pendingName = n
			}
		case p.acceptWord("COLLATE"):
			p.ident()
		case p.acceptWord("AUTO_INCREMENT"), p.acceptWord("AUTOINCREMENT"):
			col.AutoIncrement = true
			col.Nullable = false
		case p.acceptWord("GENERATED"):
			s.generatedClause(p, table, name, col)
		case p.acceptWord("ON"):
			if p.acceptWord("UPDATE") {
				p.parseDefaultExpr()
				s.warn("table %q column %q: ON UPDATE clause is not supported by lathe; ignored", table, name)
			}
		case p.acceptWord("COMMENT"):
			p.next()
		case p.acceptWord("CHARACTER"):
			p.acceptWord("SET")
			p.ident()
		case p.acceptWord("ENCRYPTED"), p.acceptWord("STORAGE"):
			p.next()
		case p.acceptWord("INVISIBLE"), p.acceptWord("VISIBLE"):
			s.warn("table %q column %q: %s column attribute is not supported by lathe; ignored", table, name, strings.ToUpper(p.toks[p.i-1].text))
		default:
			// Unknown modifier: consume one token so real-world SQL cannot
			// trap the parser, and move on.
			p.next()
		}
	}

	// The unique branch above built a throwaway table for naming. Rebuild it
	// with the proper deterministic name now that the table is known.
	for _, ix := range pc.indexes {
		if ix.Name == "" {
			ix.Name = schema.AutoName("uq", table, ix.Columns...)
		}
	}
	for _, fk := range pc.fks {
		if fk.Name == "" {
			fk.Name = schema.AutoName("fk", table, fk.Columns...)
		}
	}
	if col.AutoIncrement {
		col.Nullable = false
		col.Default = nil
	}
	return pc
}

// generatedClause handles GENERATED ALWAYS AS IDENTITY and generated columns.
func (s *state) generatedClause(p *parser, table, name string, col *schema.ColumnDef) {
	p.acceptWord("ALWAYS")
	p.acceptWord("BY")
	p.acceptWord("DEFAULT")
	if p.acceptWord("AS") {
		if p.acceptWord("IDENTITY") {
			if p.isPunct("(") {
				p.next()
				p.parenBody()
			}
			col.AutoIncrement = true
			col.Nullable = false
			return
		}
		// GENERATED ALWAYS AS (expression) STORED/VIRTUAL
		if p.isPunct("(") {
			p.next()
			p.parenBody()
		}
		p.acceptWord("STORED")
		p.acceptWord("VIRTUAL")
		s.warn("table %q column %q: generated column expression is not supported by lathe; imported as a plain column", table, name)
	}
}

// tableConstraint parses a table-level PRIMARY KEY, UNIQUE, FOREIGN KEY,
// CHECK, or MySQL KEY clause.
func (s *state) tableConstraint(t *schema.TableDef, p *parser) {
	name := ""
	if p.acceptWord("CONSTRAINT") {
		n, ok := p.ident()
		if ok {
			name = n
		}
	}
	switch {
	case p.acceptWord("PRIMARY"):
		p.acceptWord("KEY")
		cols, err := p.identList()
		if err != nil {
			s.warn("table %q: unsupported primary key: %v; ignored", t.Name, err)
			return
		}
		s.setPrimaryKey(t, cols)
	case p.acceptWord("UNIQUE"):
		p.acceptWord("KEY")
		p.acceptWord("INDEX")
		if name == "" && p.peek().isIdent() && p.peekN(1).isPunct("(") {
			name, _ = p.ident()
		}
		cols, err := p.identList()
		if err != nil {
			s.warn("table %q: unsupported unique constraint: %v; ignored", t.Name, err)
			return
		}
		s.addIndex(t, &schema.IndexDef{Name: name, Columns: cols, Unique: true})
	case p.acceptWord("FOREIGN"):
		p.acceptWord("KEY")
		cols, err := p.identList()
		if err != nil {
			s.warn("table %q: unsupported foreign key: %v; ignored", t.Name, err)
			return
		}
		if !p.acceptWord("REFERENCES") {
			s.warn("table %q: foreign key has no REFERENCES clause; ignored", t.Name)
			return
		}
		fk := s.references(p, cols, name)
		if fk != nil {
			s.addForeignKey(t, fk)
		}
	case p.acceptWord("CHECK"):
		if p.isPunct("(") {
			p.next()
			p.parenBody()
		}
		s.warn("table %q: CHECK constraint is not supported by lathe; ignored", t.Name)
	case p.acceptWord("EXCLUDE"):
		s.warn("table %q: EXCLUDE constraint is not supported by lathe; ignored", t.Name)
	case p.acceptWord("LIKE"):
		s.warn("table %q: LIKE clause is not supported by lathe; ignored", t.Name)
	case p.acceptWord("FULLTEXT"), p.acceptWord("SPATIAL"):
		s.warn("table %q: %s index is not supported by lathe; ignored", t.Name, strings.ToUpper(p.toks[p.i-1].text))
	case p.acceptWord("INDEX"), p.acceptWord("KEY"):
		ixName, ok := p.ident()
		if !ok {
			s.warn("table %q: index without a name; ignored", t.Name)
			return
		}
		cols, err := p.identList()
		if err != nil {
			s.warn("table %q: unsupported index %q: %v; ignored", t.Name, ixName, err)
			return
		}
		s.addIndex(t, &schema.IndexDef{Name: ixName, Columns: cols})
	default:
		s.warn("table %q: unrecognised table constraint; ignored", t.Name)
	}
}

// references parses the part after REFERENCES: the table, its columns and the
// referential actions.
func (s *state) references(p *parser, local []string, name string) *schema.ForeignKeyDef {
	refTable, ok := p.qualifiedIdent()
	if !ok {
		s.warn("foreign key on (%s) has no referenced table; ignored", strings.Join(local, ", "))
		return nil
	}
	var refCols []string
	if p.isPunct("(") {
		cols, err := p.identList()
		if err != nil {
			s.warn("foreign key on (%s): unsupported referenced columns: %v; ignored", strings.Join(local, ", "), err)
			return nil
		}
		refCols = cols
	}
	fk := &schema.ForeignKeyDef{Name: name, Columns: local, RefTable: refTable, RefColumns: refCols}
	for !p.done() {
		switch {
		case p.acceptWord("ON"):
			switch {
			case p.acceptWord("DELETE"):
				fk.OnDelete = parseAction(p)
			case p.acceptWord("UPDATE"):
				fk.OnUpdate = parseAction(p)
			default:
				return fk
			}
		case p.acceptWord("MATCH"):
			p.ident()
		case p.acceptWord("NOT"):
			if !p.acceptWord("DEFERRABLE") {
				return fk
			}
		case p.acceptWord("DEFERRABLE"):
		case p.acceptWord("INITIALLY"):
			p.ident()
		case p.acceptWord("DEFERRED"), p.acceptWord("IMMEDIATE"):
		default:
			return fk
		}
	}
	return fk
}

func parseAction(p *parser) schema.Action {
	switch {
	case p.acceptWord("CASCADE"):
		return schema.Cascade
	case p.acceptWord("RESTRICT"):
		return schema.Restrict
	case p.acceptWord("SET"):
		switch {
		case p.acceptWord("NULL"):
			return schema.SetNull
		case p.acceptWord("DEFAULT"):
			return schema.SetDefault
		}
	case p.acceptWord("NO"):
		p.acceptWord("ACTION")
		return schema.NoAction
	}
	return schema.NoAction
}

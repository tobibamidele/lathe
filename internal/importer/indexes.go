package importer

import (
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

func (s *state) createIndex(p *parser, unique bool) {
	p.acceptWord("CONCURRENTLY")
	ifNotExists := false
	if p.acceptWord("IF") {
		p.acceptWord("NOT")
		p.acceptWord("EXISTS")
		ifNotExists = true
	}
	name, ok := p.qualifiedIdent()
	if !ok {
		s.warn("CREATE INDEX without a name; skipped")
		return
	}
	if !p.acceptWord("ON") {
		s.warn("index %q: expected ON; skipped", name)
		return
	}
	p.acceptWord("ONLY")
	tableName, ok := p.qualifiedIdent()
	if !ok {
		s.warn("index %q: no table name; skipped", name)
		return
	}
	t := s.table(tableName)
	if t == nil {
		s.warn("index %q: table %q does not exist; skipped", name, tableName)
		return
	}

	method := "btree"
	if p.acceptWord("USING") {
		method, _ = p.ident()
	}
	if !p.isPunct("(") {
		s.warn("index %q: no column list; skipped", name)
		return
	}
	p.next()
	body, ok := p.parenBody()
	if !ok {
		s.warn("index %q: unbalanced column list; skipped", name)
		return
	}
	cols, unsupported := indexColumns(body)
	if unsupported != "" {
		s.warn("index %q: %s; skipped", name, unsupported)
		return
	}
	if len(cols) == 0 {
		s.warn("index %q: no columns; skipped", name)
		return
	}

	for !p.done() {
		switch {
		case p.acceptWord("USING"):
			m, _ := p.ident()
			if m != "" {
				method = m
			}
		case p.acceptWord("INCLUDE"):
			if p.isPunct("(") {
				p.next()
				p.parenBody()
			}
			s.warn("index %q: INCLUDE columns are not supported by lathe; skipped", name)
			return
		case p.acceptWord("WHERE"):
			s.warn("index %q is partial (WHERE); lathe has no partial indexes; skipped", name)
			return
		case p.acceptWord("WITH"):
			if p.isPunct("(") {
				p.next()
				p.parenBody()
			}
		case p.acceptWord("NULLS"):
			p.acceptWord("DISTINCT")
			p.acceptWord("NOT")
			p.acceptWord("DISTINCT")
			s.warn("index %q uses NULLS NOT DISTINCT; imported as an ordinary unique index", name)
		case p.acceptWord("TABLESPACE"):
			p.ident()
		default:
			p.next()
		}
	}
	if method != "" && !strings.EqualFold(method, "btree") {
		s.warn("index %q uses the %s access method; lathe only models btree; skipped", name, method)
		return
	}
	if ifNotExists && t.Index(name) != nil {
		return
	}
	s.addIndex(t, &schema.IndexDef{Name: name, Columns: cols, Unique: unique})
}

// indexColumns parses the inside of CREATE INDEX's column list. It returns a
// human-readable reason when the list contains an expression.
func indexColumns(body []token) ([]string, string) {
	var cols []string
	for _, seg := range splitTopLevel(body, ",") {
		if len(seg) == 0 {
			continue
		}
		p := &parser{toks: seg}
		if p.isPunct("(") {
			return nil, "expression indexes are not supported by lathe"
		}
		name, ok := p.ident()
		if !ok {
			return nil, "expression indexes are not supported by lathe"
		}
		if p.isPunct("(") {
			return nil, "expression indexes are not supported by lathe"
		}
		cols = append(cols, name)
		for !p.done() {
			switch {
			case p.acceptWord("ASC"), p.acceptWord("DESC"):
			case p.acceptWord("NULLS"):
				p.acceptWord("FIRST")
				p.acceptWord("LAST")
			case p.acceptWord("COLLATE"):
				p.ident()
			case p.peek().kind == tokWord:
				// Operator class (text_pattern_ops, gin_trgm_ops, ...).
				p.next()
			default:
				return nil, "expression indexes are not supported by lathe"
			}
		}
	}
	return cols, ""
}

func (s *state) dropIndex(p *parser) {
	p.acceptWord("CONCURRENTLY")
	ifExists := false
	if p.acceptWord("IF") {
		p.acceptWord("EXISTS")
		ifExists = true
	}
	for {
		name, ok := p.qualifiedIdent()
		if !ok {
			if !ifExists {
				s.warn("DROP INDEX without a name; ignored")
			}
			return
		}
		var table *schema.TableDef
		if p.acceptWord("ON") {
			tableName, _ := p.qualifiedIdent()
			table = s.table(tableName)
		}
		found := false
		if table != nil {
			if table.Index(name) != nil {
				s.dropIndexFrom(table, name)
				found = true
			}
		} else {
			for _, t := range s.tables {
				if t.Index(name) != nil {
					s.dropIndexFrom(t, name)
					found = true
					break
				}
			}
		}
		if !found && !ifExists {
			s.warn("DROP INDEX %q: index does not exist; ignored", name)
		}
		p.acceptWord("CASCADE")
		p.acceptWord("RESTRICT")
		if !p.acceptPunct(",") {
			return
		}
	}
}

package importer

import (
	"strings"

	"github.com/tobibamidele/lathe/schema"
)

func (s *state) createType(p *parser) {
	name, ok := p.qualifiedIdent()
	if !ok {
		s.warn("CREATE TYPE without a name; skipped")
		return
	}
	if !p.acceptWord("AS") {
		s.warn("type %q: unsupported CREATE TYPE form; skipped", name)
		return
	}
	if !p.acceptWord("ENUM") {
		s.warn("type %q: only ENUM types are supported; skipped", name)
		return
	}
	if !p.isPunct("(") {
		s.warn("type %q: ENUM has no values; skipped", name)
		return
	}
	p.next()
	body, ok := p.parenBody()
	if !ok {
		s.warn("type %q: unbalanced ENUM value list; skipped", name)
		return
	}
	var values []string
	seen := map[string]bool{}
	for _, seg := range splitTopLevel(body, ",") {
		if len(seg) != 1 || seg[0].kind != tokString {
			s.warn("type %q: ENUM value is not a string literal; skipped", name)
			continue
		}
		v := seg[0].text
		if v == "" {
			s.warn("type %q: empty ENUM value is not supported by lathe; skipped", name)
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		values = append(values, v)
	}
	if len(values) == 0 {
		s.warn("type %q: ENUM has no usable values; skipped", name)
		return
	}
	if s.enum(name) != nil {
		s.warn("type %q is defined more than once; replacing the earlier definition", name)
	}
	e := &enumDef{name: name, values: values}
	s.enums[name] = e
	s.syncEnum(name)
}

func (s *state) alterType(p *parser) {
	name, ok := p.qualifiedIdent()
	if !ok {
		s.warn("ALTER TYPE without a name; skipped")
		return
	}
	e := s.enum(name)
	if e == nil {
		// Composite types, domains and unknown types have no bearing on a
		// lathe schema.
		return
	}
	switch {
	case p.acceptWord("ADD"):
		if !p.acceptWord("VALUE") {
			return
		}
		ifNotExists := false
		if p.acceptWord("IF") {
			p.acceptWord("NOT")
			p.acceptWord("EXISTS")
			ifNotExists = true
		}
		value, ok := enumValue(p)
		if !ok {
			s.warn("type %q: ADD VALUE without a value; ignored", name)
			return
		}
		for _, v := range e.values {
			if v == value {
				if !ifNotExists {
					s.warn("type %q: value %q already exists", name, value)
				}
				return
			}
		}
		pos := len(e.values)
		if p.acceptWord("BEFORE") {
			at, ok := enumValue(p)
			if ok {
				pos = indexOfValue(e.values, at)
				if pos < 0 {
					pos = len(e.values)
				}
			}
		} else if p.acceptWord("AFTER") {
			at, ok := enumValue(p)
			if ok {
				pos = indexOfValue(e.values, at)
				if pos < 0 {
					pos = len(e.values) - 1
				}
				pos++
			}
		}
		e.values = append(e.values, "")
		copy(e.values[pos+1:], e.values[pos:])
		e.values[pos] = value
		s.syncEnum(name)
	case p.acceptWord("RENAME"):
		if p.acceptWord("VALUE") {
			from, ok1 := enumValue(p)
			if !ok1 || !p.acceptWord("TO") {
				return
			}
			to, ok2 := enumValue(p)
			if !ok2 {
				return
			}
			for i, v := range e.values {
				if v == from {
					e.values[i] = to
					s.syncEnum(name)
					return
				}
			}
			s.warn("type %q: value %q does not exist", name, from)
			return
		}
		p.acceptWord("TO")
		newName, ok := p.qualifiedIdent()
		if !ok {
			return
		}
		old := e.name
		delete(s.enums, old)
		e.name = newName
		s.enums[newName] = e
		for col, en := range s.enumOf {
			if strings.EqualFold(en, old) {
				s.enumOf[col] = newName
			}
		}
	default:
		// ADD ATTRIBUTE etc. belong to composite types.
	}
}

func enumValue(p *parser) (string, bool) {
	t := p.peek()
	if t.kind == tokString {
		p.next()
		return t.text, true
	}
	if t.isIdent() {
		p.next()
		return t.identValue(), true
	}
	return "", false
}

func indexOfValue(values []string, v string) int {
	for i, x := range values {
		if x == v {
			return i
		}
	}
	return -1
}

func (s *state) syncEnum(name string) {
	e := s.enum(name)
	if e == nil {
		return
	}
	for col, en := range s.enumOf {
		if strings.EqualFold(en, name) {
			col.Type = schema.Type{Kind: schema.KindEnum, Values: append([]string(nil), e.values...)}
		}
	}
}

func (s *state) dropType(p *parser) {
	p.acceptWord("IF")
	p.acceptWord("EXISTS")
	for {
		name, ok := p.qualifiedIdent()
		if !ok {
			return
		}
		if e := s.enum(name); e != nil {
			for col, en := range s.enumOf {
				if strings.EqualFold(en, e.name) {
					s.warn("column %q used dropped type %q; imported as text", col.Name, e.name)
					col.Type = schema.Type{Kind: schema.KindText}
					delete(s.enumOf, col)
				}
			}
			delete(s.enums, e.name)
		}
		p.acceptWord("CASCADE")
		p.acceptWord("RESTRICT")
		if !p.acceptPunct(",") {
			return
		}
	}
}

package importer

import (
	"errors"
	"strings"
)

// parser walks the tokens of a single statement or ALTER TABLE action.
type parser struct {
	toks []token
	i    int
}

func (p *parser) done() bool { return p.i >= len(p.toks) }

func (p *parser) peek() token {
	if p.done() {
		return token{}
	}
	return p.toks[p.i]
}

func (p *parser) peekN(n int) token {
	if p.i+n >= len(p.toks) {
		return token{}
	}
	return p.toks[p.i+n]
}

func (p *parser) next() token {
	if p.done() {
		return token{}
	}
	t := p.toks[p.i]
	p.i++
	return t
}

func (p *parser) back() {
	if p.i > 0 {
		p.i--
	}
}

func (p *parser) isWord(s string) bool { return p.peek().isWord(s) }

func (p *parser) acceptWord(s string) bool {
	if p.isWord(s) {
		p.i++
		return true
	}
	return false
}

// acceptWords consumes the sequence when all of it matches.
func (p *parser) acceptWords(words ...string) bool {
	if p.i+len(words) > len(p.toks) {
		return false
	}
	for j, w := range words {
		if !p.toks[p.i+j].isWord(w) {
			return false
		}
	}
	p.i += len(words)
	return true
}

func (p *parser) isPunct(s string) bool { return p.peek().isPunct(s) }

func (p *parser) acceptPunct(s string) bool {
	if p.isPunct(s) {
		p.i++
		return true
	}
	return false
}

func (p *parser) rest() []token { return p.toks[p.i:] }

// ident reads a bare or quoted identifier.
func (p *parser) ident() (string, bool) {
	if p.done() {
		return "", false
	}
	t := p.peek()
	if !t.isIdent() {
		return "", false
	}
	p.i++
	return t.identValue(), true
}

// qualifiedIdent reads a possibly schema-qualified name and returns its last
// component, because lathe schemas are single-namespace.
func (p *parser) qualifiedIdent() (string, bool) {
	name, ok := p.ident()
	if !ok {
		return "", false
	}
	last := name
	for p.acceptPunct(".") {
		n, ok := p.ident()
		if !ok {
			break
		}
		last = n
	}
	return last, true
}

// parenBody returns the tokens inside a parenthesised group. The caller has
// already consumed the opening parenthesis.
func (p *parser) parenBody() ([]token, bool) {
	depth := 1
	var out []token
	for !p.done() {
		t := p.next()
		switch {
		case t.isPunct("("):
			depth++
			out = append(out, t)
		case t.isPunct(")"):
			depth--
			if depth == 0 {
				return out, true
			}
			out = append(out, t)
		default:
			out = append(out, t)
		}
	}
	return out, false
}

// identList reads a parenthesised column list. It returns an error when the
// list contains anything but identifiers, which usually means an expression
// index that lathe cannot represent.
func (p *parser) identList() ([]string, error) {
	if !p.acceptPunct("(") {
		return nil, errors.New("expected '('")
	}
	var out []string
	for {
		if p.acceptPunct(")") {
			return out, nil
		}
		if p.acceptPunct(",") {
			continue
		}
		if p.done() {
			return nil, errors.New("unterminated column list")
		}
		name, ok := p.ident()
		if !ok {
			return nil, errors.New("column list contains an expression")
		}
		out = append(out, name)
		// Reject expression decorations such as lower(email) or email DESC.
		if !p.done() && !p.isPunct(",") && !p.isPunct(")") {
			return nil, errors.New("column list contains an expression")
		}
	}
}

// splitTopLevel splits tokens on sep at nesting depth zero.
func splitTopLevel(toks []token, sep string) [][]token {
	var out [][]token
	var cur []token
	depth := 0
	for _, t := range toks {
		switch {
		case t.isPunct("("), t.isPunct("["):
			depth++
		case t.isPunct(")"), t.isPunct("]"):
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && t.isPunct(sep) {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// joinTokens renders tokens as SQL. Spacing is conservative; expression
// defaults are emitted verbatim and do not need to be pretty.
func joinTokens(toks []token) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		if t.raw != "" {
			b.WriteString(t.raw)
		} else {
			b.WriteString(t.text)
		}
	}
	return b.String()
}

// stripOuterParens removes one matched pair of parentheses around toks.
func stripOuterParens(toks []token) []token {
	for len(toks) >= 2 && toks[0].isPunct("(") && toks[len(toks)-1].isPunct(")") {
		depth := 0
		matched := true
		for i, t := range toks {
			if t.isPunct("(") {
				depth++
			} else if t.isPunct(")") {
				depth--
				if depth == 0 && i != len(toks)-1 {
					matched = false
					break
				}
			}
		}
		if !matched || depth != 0 {
			return toks
		}
		toks = toks[1 : len(toks)-1]
	}
	return toks
}

func isTableConstraintStart(p *parser) bool {
	if p.done() {
		return false
	}
	t := p.peek()
	for _, w := range []string{"CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "EXCLUDE", "LIKE", "KEY", "INDEX", "FULLTEXT", "SPATIAL"} {
		if t.isWord(w) {
			return true
		}
	}
	return false
}

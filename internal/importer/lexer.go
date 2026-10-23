package importer

import (
	"errors"
	"fmt"
	"strings"
)

// tokKind is the lexical class of a SQL token.
type tokKind int

const (
	tokWord tokKind = iota
	tokQuoted
	tokString
	tokNumber
	tokPunct
)

// token is one piece of a SQL statement. raw keeps the exact source text so
// expressions can be re-rendered verbatim; text holds the decoded value for
// strings and quoted identifiers.
type token struct {
	kind tokKind
	text string
	raw  string
}

func (t token) isWord(s string) bool {
	return t.kind == tokWord && strings.EqualFold(t.text, s)
}

func (t token) isPunct(s string) bool { return t.kind == tokPunct && t.text == s }

func (t token) isIdent() bool { return t.kind == tokWord || t.kind == tokQuoted }

// identValue returns an identifier as SQL resolves it: unquoted words fold to
// lower case, quoted identifiers keep their spelling.
func (t token) identValue() string {
	switch t.kind {
	case tokQuoted:
		return t.text
	case tokWord:
		return strings.ToLower(t.text)
	}
	return t.text
}

// lex splits a SQL statement into tokens. backslashEscapes should be true for
// MySQL, where backslashes escape characters inside string literals.
func lex(src string, backslashEscapes bool) ([]token, error) {
	var out []token
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case c == '-' && i+1 < n && src[i+1] == '-':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			depth := 1
			i += 2
			for i < n && depth > 0 {
				switch {
				case i+1 < n && src[i] == '/' && src[i+1] == '*':
					depth++
					i += 2
				case i+1 < n && src[i] == '*' && src[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}
		case (c == 'E' || c == 'e') && i+1 < n && src[i+1] == '\'':
			s, used, err := scanString(src[i+1:], true)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokString, text: s, raw: src[i : i+1+used]})
			i += 1 + used
		case (c == 'N' || c == 'n') && i+1 < n && src[i+1] == '\'':
			s, used, err := scanString(src[i+1:], backslashEscapes)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokString, text: s, raw: src[i : i+1+used]})
			i += 1 + used
		case c == '\'':
			s, used, err := scanString(src[i:], backslashEscapes)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokString, text: s, raw: src[i : i+used]})
			i += used
		case c == '"':
			s, used, err := scanQuoted(src[i:], '"')
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokQuoted, text: s, raw: src[i : i+used]})
			i += used
		case c == '`':
			s, used, err := scanQuoted(src[i:], '`')
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokQuoted, text: s, raw: src[i : i+used]})
			i += used
		case c == '[':
			// [] is the PostgreSQL array suffix; [name] is a quoted identifier.
			if i+1 < n && src[i+1] == ']' {
				out = append(out, token{kind: tokPunct, text: "["}, token{kind: tokPunct, text: "]"})
				i += 2
				continue
			}
			s, used, err := scanQuoted(src[i:], ']')
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokQuoted, text: s, raw: src[i : i+used]})
			i += used
		case c == '$':
			if tag, ok := dollarTagAt(src[i:]); ok {
				rest := src[i+len(tag):]
				end := strings.Index(rest, tag)
				if end < 0 {
					return nil, fmt.Errorf("unterminated dollar-quoted string starting %s", tag)
				}
				stop := i + len(tag) + end
				out = append(out, token{kind: tokString, text: src[i+len(tag) : stop], raw: src[i : stop+len(tag)]})
				i = stop + len(tag)
				continue
			}
			out = append(out, token{kind: tokPunct, text: "$"})
			i++
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(src[i+1])):
			start := i
			if c == '0' && i+1 < n && (src[i+1] == 'x' || src[i+1] == 'X') {
				i += 2
				for i < n && isHex(src[i]) {
					i++
				}
			} else {
				for i < n && isDigit(src[i]) {
					i++
				}
				if i < n && src[i] == '.' {
					i++
					for i < n && isDigit(src[i]) {
						i++
					}
				}
				if i < n && (src[i] == 'e' || src[i] == 'E') {
					j := i + 1
					if j < n && (src[j] == '+' || src[j] == '-') {
						j++
					}
					if j < n && isDigit(src[j]) {
						i = j
						for i < n && isDigit(src[i]) {
							i++
						}
					}
				}
			}
			raw := src[start:i]
			out = append(out, token{kind: tokNumber, text: raw, raw: raw})
		case isIdentStart(c):
			start := i
			i++
			for i < n && isIdentPart(src[i]) {
				i++
			}
			raw := src[start:i]
			out = append(out, token{kind: tokWord, text: raw, raw: raw})
		default:
			if i+1 < n {
				switch two := src[i : i+2]; two {
				case "::", "->", ">=", "<=", "<>", "!=", "||", ":=", "=>", "->>", "#>", "#>>":
					out = append(out, token{kind: tokPunct, text: two, raw: two})
					i += 2
					continue
				}
			}
			out = append(out, token{kind: tokPunct, text: string(c), raw: string(c)})
			i++
		}
	}
	return out, nil
}

func scanString(src string, backslash bool) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(src); {
		c := src[i]
		if c == '\'' {
			if i+1 < len(src) && src[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			return b.String(), i + 1, nil
		}
		if backslash && c == '\\' && i+1 < len(src) {
			switch src[i+1] {
			case '0':
				b.WriteByte(0)
			case 'b':
				b.WriteByte('\b')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'Z':
				b.WriteByte(26)
			default:
				b.WriteByte(src[i+1])
			}
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return "", 0, errors.New("unterminated string literal")
}

func scanQuoted(src string, close byte) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(src); {
		c := src[i]
		if c == close {
			if i+1 < len(src) && src[i+1] == close {
				b.WriteByte(close)
				i += 2
				continue
			}
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return "", 0, errors.New("unterminated quoted identifier")
}

func dollarTagAt(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for j := 1; j < len(s); j++ {
		ch := s[j]
		switch {
		case ch == '$':
			return s[:j+1], true
		case ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (j > 1 && ch >= '0' && ch <= '9'):
		default:
			return "", false
		}
	}
	return "", false
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

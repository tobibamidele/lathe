package migrate

import "strings"

// SplitStatements splits a SQL script into individual statements. It
// understands quoted strings and identifiers, line and block comments and
// PostgreSQL dollar quoting, so semicolons inside them do not split.
//
// backslashEscapes should be true for MySQL, where a backslash escapes the
// next character inside a string literal.
func SplitStatements(script string, backslashEscapes bool) []string {
	var (
		out     []string
		cur     strings.Builder
		content bool // the current statement has something besides comments
	)
	flush := func() {
		if content {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
		}
		cur.Reset()
		content = false
	}

	n := len(script)
	for i := 0; i < n; {
		c := script[i]
		switch {
		case c == '-' && i+1 < n && script[i+1] == '-':
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				end = n - i
			}
			cur.WriteString(script[i : i+end])
			i += end
		case c == '/' && i+1 < n && script[i+1] == '*':
			end := strings.Index(script[i+2:], "*/")
			if end < 0 {
				cur.WriteString(script[i:])
				i = n
			} else {
				cur.WriteString(script[i : i+2+end+2])
				i += 2 + end + 2
			}
		case c == '\'' || c == '"' || c == '`':
			content = true
			j := i + 1
			for j < n {
				if backslashEscapes && c != '`' && script[j] == '\\' {
					j += 2
					continue
				}
				if script[j] == c {
					if j+1 < n && script[j+1] == c { // doubled quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= n {
				j = n - 1
			}
			cur.WriteString(script[i : j+1])
			i = j + 1
		case c == '$':
			if tag, ok := dollarTag(script[i:]); ok {
				content = true
				end := strings.Index(script[i+len(tag):], tag)
				if end < 0 {
					cur.WriteString(script[i:])
					i = n
				} else {
					stop := i + len(tag) + end + len(tag)
					cur.WriteString(script[i:stop])
					i = stop
				}
			} else {
				content = true
				cur.WriteByte(c)
				i++
			}
		case c == ';':
			flush()
			i++
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				content = true
			}
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// dollarTag recognises $$ or $tag$ at the start of s.
func dollarTag(s string) (string, bool) {
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

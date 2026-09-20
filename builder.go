package lathe

import (
	"fmt"
	"strings"
)

// builder accumulates SQL text and bind arguments.
type builder struct {
	d    Dialect
	sb   strings.Builder
	args []any
	err  error
}

func newBuilder(d Dialect) *builder { return &builder{d: d} }

func (b *builder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

func (b *builder) write(s string) { b.sb.WriteString(s) }
func (b *builder) ident(s string) { b.sb.WriteString(b.d.Quote(s)) }
func (b *builder) String() string { return b.sb.String() }
func (b *builder) column(table, name string) {
	b.ident(table)
	b.sb.WriteByte('.')
	b.ident(name)
}

// arg binds v and writes its placeholder.
func (b *builder) arg(v any) {
	b.args = append(b.args, v)
	b.sb.WriteString(b.d.Placeholder(len(b.args)))
}

// rawSQL writes a user supplied fragment, binding its ? placeholders.
func (b *builder) rawSQL(sql string, args []any) {
	i := 0
	out := scanPlaceholders(sql, func() string {
		if i >= len(args) {
			b.fail(fmt.Errorf("lathe: SQL fragment has more placeholders than arguments: %q", sql))
			return "?"
		}
		b.args = append(b.args, args[i])
		i++
		return b.d.Placeholder(len(b.args))
	})
	if i != len(args) {
		b.fail(fmt.Errorf("lathe: SQL fragment has %d placeholders but %d arguments: %q", i, len(args), sql))
	}
	b.sb.WriteString(out)
}

// rebind rewrites the ? placeholders of a complete statement for d.
func rebind(d Dialect, query string) string {
	n := 0
	return scanPlaceholders(query, func() string {
		n++
		return d.Placeholder(n)
	})
}

// scanPlaceholders replaces each ? outside of quotes and comments with the
// result of emit. A doubled ?? is an escaped literal ? (for example the
// PostgreSQL jsonb operators).
func scanPlaceholders(sql string, emit func() string) string {
	if !strings.Contains(sql, "?") {
		return sql
	}
	var out strings.Builder
	out.Grow(len(sql) + 8)
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(sql) {
				if sql[j] == c {
					if j+1 < len(sql) && sql[j+1] == c { // doubled quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(sql) {
				j = len(sql) - 1
			}
			out.WriteString(sql[i : j+1])
			i = j
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = len(sql) - i
			}
			out.WriteString(sql[i : i+j])
			i += j - 1
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				out.WriteString(sql[i:])
				return out.String()
			}
			end := i + 2 + j + 2
			out.WriteString(sql[i:end])
			i = end - 1
		case c == '?':
			if i+1 < len(sql) && sql[i+1] == '?' {
				out.WriteByte('?')
				i++
				continue
			}
			out.WriteString(emit())
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

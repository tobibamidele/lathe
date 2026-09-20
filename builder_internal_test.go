package lathe

import "testing"

type testDialect struct {
	pg bool
}

func (d testDialect) Name() string { return "test" }
func (d testDialect) Placeholder(n int) string {
	if d.pg {
		return "$" + itoa(n)
	}
	return "?"
}
func (d testDialect) Quote(s string) string {
	if d.pg {
		return `"` + s + `"`
	}
	return "`" + s + "`"
}
func (d testDialect) SupportsReturning() bool { return d.pg }
func (d testDialect) NativeILike() bool       { return d.pg }
func (d testDialect) DefaultValues(t string) string {
	return "INSERT INTO " + t + " DEFAULT VALUES"
}
func (d testDialect) LimitOffset(limit, offset int64) string {
	s := ""
	if limit >= 0 {
		s += " LIMIT " + itoa(int(limit))
	}
	if offset > 0 {
		s += " OFFSET " + itoa(int(offset))
	}
	return s
}
func (d testDialect) ConflictStyle() ConflictStyle {
	if d.pg {
		return OnConflict
	}
	return OnDuplicateKey
}
func (d testDialect) MaxParams() int            { return 100 }
func (d testDialect) ClassifyError(error) error { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRebind(t *testing.T) {
	pg := testDialect{pg: true}
	cases := []struct{ in, want string }{
		{"select ?, ?", "select $1, $2"},
		{"select '?' , ?", "select '?' , $1"},
		{`select "a?b", ?`, `select "a?b", $1`},
		{"select 'it''s ?', ?", "select 'it''s ?', $1"},
		{"select data ?? 'k', ?", "select data ? 'k', $1"},
		{"select 1 -- what?\n, ?", "select 1 -- what?\n, $1"},
		{"select /* ? */ ?", "select /* ? */ $1"},
		{"no placeholders", "no placeholders"},
	}
	for _, c := range cases {
		if got := rebind(pg, c.in); got != c.want {
			t.Errorf("rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := rebind(testDialect{}, "select ?? , ?"); got != "select ? , ?" {
		t.Errorf("?? must collapse on ? dialects too, got %q", got)
	}
}

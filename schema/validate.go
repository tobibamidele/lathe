package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	identRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	numberRE  = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	relNameRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)
)

// Validate checks a snapshot for internal consistency. All problems are
// reported together.
func Validate(s *Snapshot) error {
	var errs []error
	seen := map[string]bool{}
	for _, t := range s.Tables {
		if seen[t.Name] {
			errs = append(errs, fmt.Errorf("table %q is declared more than once", t.Name))
		}
		seen[t.Name] = true
	}
	for _, t := range s.Tables {
		errs = append(errs, validateTable(s, t)...)
	}
	return errors.Join(errs...)
}

func validateTable(s *Snapshot, t *TableDef) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("table %q: "+format, append([]any{t.Name}, args...)...))
	}

	if !identRE.MatchString(t.Name) {
		fail("invalid name (use letters, digits and underscores, not starting with a digit)")
	}
	if len(t.Name) > maxIdent {
		fail("name is longer than %d bytes", maxIdent)
	}
	if len(t.Columns) == 0 {
		fail("has no columns")
	}
	if len(t.PrimaryKey) == 0 {
		fail("has no primary key")
	}
	if t.RenamedFrom != "" && !identRE.MatchString(t.RenamedFrom) {
		fail("RenamedFrom(%q) is not a valid name", t.RenamedFrom)
	}

	cols := map[string]*ColumnDef{}
	for _, c := range t.Columns {
		if !identRE.MatchString(c.Name) {
			fail("column %q: invalid name", c.Name)
			continue
		}
		if len(c.Name) > maxIdent {
			fail("column %q: name is longer than %d bytes", c.Name, maxIdent)
		}
		if _, dup := cols[c.Name]; dup {
			fail("column %q is declared more than once", c.Name)
			continue
		}
		cols[c.Name] = c
		for _, e := range validateColumn(t, c) {
			fail("column %q: %s", c.Name, e)
		}
	}

	seenPK := map[string]bool{}
	for _, name := range t.PrimaryKey {
		if cols[name] == nil {
			fail("primary key references unknown column %q", name)
		}
		if seenPK[name] {
			fail("primary key lists column %q twice", name)
		}
		seenPK[name] = true
	}
	for _, c := range t.Columns {
		if c.AutoIncrement && (len(t.PrimaryKey) != 1 || t.PrimaryKey[0] != c.Name) {
			fail("column %q: AutoIncrement requires the column to be the only primary key column", c.Name)
		}
	}

	idxNames := map[string]bool{}
	for _, ix := range t.Indexes {
		if idxNames[ix.Name] {
			fail("index name %q is used more than once", ix.Name)
		}
		idxNames[ix.Name] = true
		if len(ix.Columns) == 0 {
			fail("index %q has no columns", ix.Name)
		}
		for _, cn := range ix.Columns {
			if cols[cn] == nil {
				fail("index %q references unknown column %q", ix.Name, cn)
			}
		}
	}

	fkNames := map[string]bool{}
	for _, fk := range t.ForeignKeys {
		if fkNames[fk.Name] {
			fail("foreign key name %q is used more than once", fk.Name)
		}
		fkNames[fk.Name] = true
		errs = append(errs, validateFK(s, t, cols, fk)...)
	}
	return errs
}

func validateFK(s *Snapshot, t *TableDef, cols map[string]*ColumnDef, fk *ForeignKeyDef) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("table %q: foreign key %q: "+format, append([]any{t.Name, fk.Name}, args...)...))
	}
	for _, name := range []string{fk.Relation, fk.Reverse} {
		if name != "" && name != "-" && !relNameRE.MatchString(name) {
			fail("relation name %q must be an exported Go identifier (or - to skip)", name)
		}
	}
	if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.RefColumns) {
		fail("needs the same, non-zero number of local and referenced columns")
		return errs
	}
	ref := s.Table(fk.RefTable)
	if ref == nil {
		fail("references unknown table %q", fk.RefTable)
		return errs
	}
	for i, cn := range fk.Columns {
		local := cols[cn]
		if local == nil {
			fail("unknown column %q", cn)
			continue
		}
		target := ref.Column(fk.RefColumns[i])
		if target == nil {
			fail("references unknown column %s.%s", ref.Name, fk.RefColumns[i])
			continue
		}
		if local.Type.Kind != target.Type.Kind {
			fail("column %q is %s but %s.%s is %s", cn, local.Type.Kind, ref.Name, target.Name, target.Type.Kind)
		}
		if local.Type.Kind == KindVarChar && local.Type.Length != target.Type.Length {
			fail("column %q has length %d but %s.%s has length %d", cn, local.Type.Length, ref.Name, target.Name, target.Type.Length)
		}
	}
	if !coveredByKey(ref, fk.RefColumns) {
		fail("referenced columns (%s) of %q must be its primary key or a unique index", strings.Join(fk.RefColumns, ", "), ref.Name)
	}
	return errs
}

// coveredByKey reports whether cols is exactly the primary key or the column
// list of a unique index of t.
func coveredByKey(t *TableDef, cols []string) bool {
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

func validateColumn(t *TableDef, c *ColumnDef) []string {
	var errs []string
	ty := c.Type
	switch ty.Kind {
	case KindSmallInt, KindInt, KindBigInt, KindReal, KindDouble, KindBool,
		KindText, KindUUID, KindTimestamp, KindDate, KindJSON, KindBytes:
	case KindVarChar:
		if ty.Length <= 0 {
			errs = append(errs, "VarChar needs a positive length")
		}
	case KindDecimal:
		if ty.Precision <= 0 || ty.Scale < 0 || ty.Scale > ty.Precision {
			errs = append(errs, fmt.Sprintf("Decimal(%d, %d) needs precision > 0 and 0 <= scale <= precision", ty.Precision, ty.Scale))
		}
	case KindEnum:
		if len(ty.Values) == 0 {
			errs = append(errs, "Enum needs at least one value")
		}
		seen := map[string]bool{}
		for _, v := range ty.Values {
			if v == "" {
				errs = append(errs, "Enum values must not be empty")
			}
			if seen[v] {
				errs = append(errs, fmt.Sprintf("Enum value %q is listed twice", v))
			}
			seen[v] = true
		}
	default:
		errs = append(errs, fmt.Sprintf("unknown type %q", ty.Kind))
	}
	if c.AutoIncrement {
		if !ty.Kind.IsInteger() {
			errs = append(errs, "AutoIncrement requires an integer column")
		}
		if c.Nullable {
			errs = append(errs, "AutoIncrement columns cannot be nullable")
		}
		if c.Default != nil {
			errs = append(errs, "AutoIncrement columns cannot have a default")
		}
	}
	if d := c.Default; d != nil {
		errs = append(errs, validateDefault(c, d)...)
	}
	return errs
}

func validateDefault(c *ColumnDef, d *Default) []string {
	ty := c.Type
	bad := func(msg string) []string { return []string{"default: " + msg} }
	switch d.Kind {
	case DefaultNow:
		if ty.Kind != KindTimestamp && ty.Kind != KindDate {
			return bad("DefaultNow needs a Timestamp or Date column")
		}
	case DefaultUUID:
		if ty.Kind != KindUUID {
			return bad("DefaultUUID needs a UUID column")
		}
	case DefaultExpr:
		if strings.TrimSpace(d.Value) == "" {
			return bad("empty expression")
		}
	case DefaultLiteral:
		switch {
		case ty.Kind == KindBool:
			if d.Lit != LitBool {
				return bad("a Bool column needs a bool default")
			}
		case ty.Kind.IsNumeric():
			if d.Lit == LitBool || !numberRE.MatchString(d.Value) {
				return bad(fmt.Sprintf("%q is not a number", d.Value))
			}
			if ty.Kind.IsInteger() && strings.Contains(d.Value, ".") {
				return bad("an integer column needs an integer default")
			}
		case ty.Kind == KindEnum:
			if d.Lit != LitString {
				return bad("an Enum column needs a string default")
			}
			found := false
			for _, v := range ty.Values {
				found = found || v == d.Value
			}
			if !found {
				return bad(fmt.Sprintf("%q is not one of the enum values", d.Value))
			}
		case ty.Kind == KindJSON:
			if d.Lit != LitString || !json.Valid([]byte(d.Value)) {
				return bad("a JSON column needs a string default holding valid JSON")
			}
		case ty.Kind.IsString():
			if d.Lit != LitString {
				return bad(fmt.Sprintf("a %s column needs a string default", ty.Kind))
			}
		default:
			return bad(fmt.Sprintf("literal defaults are not supported for %s columns; use DefaultExpr", ty.Kind))
		}
	default:
		return bad(fmt.Sprintf("unknown default kind %q", d.Kind))
	}
	return nil
}

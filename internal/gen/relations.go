package gen

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tobibamidele/lathe/schema"
)

// relView is one generated relation: a field on the model struct plus a
// lathe.Relation member on the table variable.
type relView struct {
	Field       string
	GoType      string // *User or []Post
	Target      string // model name of the related table
	JSON        string
	Doc         string
	Constructor string // Go expression building the lathe.Relation
}

// deriveRelations turns foreign keys into relations:
//
//   - a foreign key gives its table a belongs-to relation (Post.Author);
//   - and the referenced table a has-many relation (User.Posts), or has-one when
//     the foreign key columns are unique in the referencing table (User.Profile);
//   - a table with exactly two foreign keys whose columns form its primary key is
//     a join table and gives both sides a many-to-many relation (Post.Tags).
func deriveRelations(exp *schema.Export, views []*tableView) error {
	byTable := map[string]*tableView{}
	for _, v := range views {
		byTable[v.Name] = v
	}
	used := map[string]map[string]string{} // model -> field -> origin
	for _, v := range views {
		used[v.Model] = map[string]string{}
		for _, f := range v.Fields {
			used[v.Model][f.Name] = "column " + f.Column
		}
	}
	add := func(v *tableView, r relView, origin string) error {
		if prev, dup := used[v.Model][r.Field]; dup {
			return fmt.Errorf("gen: the relation %s.%s (from %s) collides with %s; name it with .Relation(\"...\", \"...\") on the foreign key",
				v.Model, r.Field, origin, prev)
		}
		used[v.Model][r.Field] = origin
		v.Relations = append(v.Relations, r)
		return nil
	}

	for _, t := range exp.Snapshot.Tables {
		tv := byTable[t.Name]
		for _, fk := range t.ForeignKeys {
			pv := byTable[fk.RefTable]
			if pv == nil {
				continue
			}
			origin := fmt.Sprintf("the foreign key %s.%s", t.Name, fk.Name)
			forward := belongsToName(fk, pv.Model)

			if fk.Relation != "-" {
				name := fk.Relation
				if name == "" {
					name = forward
				}
				spec := fmt.Sprintf("lathe.BelongsToSpec[%s, %s]", tv.Model, pv.Model)
				ctor := fmt.Sprintf(`lathe.BelongsTo(%q, %s{
					Source: %s, Target: %s,
					SourceCols: %s, TargetCols: %s,
					Set: func(m *%s, v *%s) { m.%s = v },
				})`, name, spec, tv.Spec, pv.Spec, strs(fk.Columns), strs(fk.RefColumns), tv.Model, pv.Model, name)
				err := add(tv, relView{
					Field: name, GoType: "*" + pv.Model, Target: pv.Model, JSON: snake(name),
					Doc:         fmt.Sprintf("%s is the %s this row belongs to. It is nil until loaded with With(%s.%s) or Load.", name, pv.Model, "<table>", name),
					Constructor: ctor,
				}, origin)
				if err != nil {
					return err
				}
			}

			if fk.Reverse == "-" {
				continue
			}
			multi := 0
			for _, other := range t.ForeignKeys {
				if other.RefTable == fk.RefTable {
					multi++
				}
			}
			suffix := ""
			if multi > 1 {
				suffix = "By" + forward
				if forward == pv.Model {
					suffix = "By" + pascal(strings.Join(fk.Columns, "_"))
				}
			}
			one := uniqueIn(t, fk.Columns)
			name, goType := fk.Reverse, ""
			var ctor string
			if one {
				if name == "" {
					name = tv.Model + suffix
				}
				goType = "*" + tv.Model
				ctor = fmt.Sprintf(`lathe.HasOne(%q, lathe.HasOneSpec[%s, %s]{
					Source: %s, Target: %s,
					SourceCols: %s, TargetCols: %s,
					Set: func(m *%s, v *%s) { m.%s = v },
				})`, name, pv.Model, tv.Model, pv.Spec, tv.Spec, strs(fk.RefColumns), strs(fk.Columns), pv.Model, tv.Model, name)
			} else {
				if name == "" {
					name = pascal(t.Name) + suffix
				}
				goType = "[]" + tv.Model
				ctor = fmt.Sprintf(`lathe.HasMany(%q, lathe.HasManySpec[%s, %s]{
					Source: %s, Target: %s,
					SourceCols: %s, TargetCols: %s,
					Set: func(m *%s, v []%s) { m.%s = v },
				})`, name, pv.Model, tv.Model, pv.Spec, tv.Spec, strs(fk.RefColumns), strs(fk.Columns), pv.Model, tv.Model, name)
			}
			err := add(pv, relView{
				Field: name, GoType: goType, Target: tv.Model, JSON: snake(name),
				Doc:         fmt.Sprintf("%s holds the related %s rows. It is nil until loaded with With(%s.%s) or Load.", name, tv.Model, "<table>", name),
				Constructor: ctor,
			}, origin)
			if err != nil {
				return err
			}
		}
	}

	// many-to-many through join tables
	for _, j := range exp.Snapshot.Tables {
		f1, f2, ok := joinTable(j)
		if !ok {
			continue
		}
		jv := byTable[j.Name]
		for _, pair := range [][2]*schema.ForeignKeyDef{{f1, f2}, {f2, f1}} {
			from, to := pair[0], pair[1]
			av, bv := byTable[from.RefTable], byTable[to.RefTable]
			if av == nil || bv == nil {
				continue
			}
			name := pascal(bv.Name)
			ctor := fmt.Sprintf(`lathe.ManyToMany(%q, lathe.ManyToManySpec[%s, %s, %s]{
				Source: %s, Target: %s, Through: %s,
				SourceCols: %s, TargetCols: %s,
				ThroughSourceCols: %s, ThroughTargetCols: %s,
				Set: func(m *%s, v []%s) { m.%s = v },
			})`, name, av.Model, bv.Model, jv.Model, av.Spec, bv.Spec, jv.Spec,
				strs(from.RefColumns), strs(to.RefColumns), strs(from.Columns), strs(to.Columns),
				av.Model, bv.Model, name)
			err := add(av, relView{
				Field: name, GoType: "[]" + bv.Model, Target: bv.Model, JSON: snake(name),
				Doc:         fmt.Sprintf("%s holds the related %s rows through %s. It is nil until loaded with With(%s.%s) or Load.", name, bv.Model, j.Name, "<table>", name),
				Constructor: ctor,
			}, "the join table "+j.Name)
			if err != nil {
				return err
			}
		}
	}

	// fill in the table variable name used in doc comments
	for _, v := range views {
		for i := range v.Relations {
			v.Relations[i].Doc = strings.ReplaceAll(v.Relations[i].Doc, "<table>", exp.Config.Package+"."+v.Vars)
		}
	}
	return nil
}

// belongsToName derives the field name of a belongs-to relation from the
// foreign key column: author_id -> Author. Columns that do not look like keys
// fall back to the target model's name.
func belongsToName(fk *schema.ForeignKeyDef, targetModel string) string {
	if len(fk.Columns) == 1 {
		c := fk.Columns[0]
		lc := strings.ToLower(c)
		for _, suf := range []string{"_id", "_uuid", "_key"} {
			if strings.HasSuffix(lc, suf) && len(c) > len(suf) {
				return pascal(c[:len(c)-len(suf)])
			}
		}
	}
	return targetModel
}

// uniqueIn reports whether cols are unique in t: its primary key or a unique
// index. A foreign key over unique columns is one-to-one.
func uniqueIn(t *schema.TableDef, cols []string) bool {
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

// joinTable recognises a pure many-to-many join table: two foreign keys to two
// different tables whose columns together are exactly the primary key.
func joinTable(t *schema.TableDef) (f1, f2 *schema.ForeignKeyDef, ok bool) {
	if len(t.ForeignKeys) != 2 {
		return nil, nil, false
	}
	f1, f2 = t.ForeignKeys[0], t.ForeignKeys[1]
	if f1.RefTable == f2.RefTable || f1.RefTable == t.Name || f2.RefTable == t.Name {
		return nil, nil, false
	}
	union := append(append([]string{}, f1.Columns...), f2.Columns...)
	if !sameSet(union, t.PrimaryKey) {
		return nil, nil, false
	}
	return f1, f2, true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func strs(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[]string{" + strings.Join(q, ", ") + "}"
}

// snake converts PascalCase to snake_case for JSON keys: PostTags -> post_tags.
func snake(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 && (unicode.IsLower(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

package lathe

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Preloader loads a relation for a batch of rows of M. The relations on the
// generated table variables (db.Posts.Author...) implement it; pass them to
// With, Load or LoadMany.
type Preloader[M any] interface {
	preload(ctx context.Context, db *DB, parents []*M) error
	// requires lists the columns of M the relation reads to find related rows.
	requires() []string
	relationName() string
}

type relOpts[T any] struct {
	where   []Expr
	order   []Order
	exclude []ColumnRef
	nested  []Preloader[T]
}

// Relation is a typed link from model M to model T, loaded in batches: one
// extra query per relation (two for many-to-many), never one per row.
//
//	posts, err := client.Posts.FindMany().
//		With(db.Posts.Author, db.Posts.Comments.OrderBy(db.Comments.ID.Asc())).
//		All(ctx)
//
// Relation values are immutable: Where, OrderBy, Exclude and With return a
// modified copy, so db.Posts.Comments itself is never changed.
type Relation[M, T any] struct {
	name string
	cols []string
	load func(ctx context.Context, db *DB, parents []*M, o relOpts[T]) error
	opts relOpts[T]
}

// Name returns the relation's field name on M.
func (r Relation[M, T]) Name() string { return r.name }

// Where restricts which related rows are loaded. Conditions use the columns of
// the related table.
func (r Relation[M, T]) Where(exprs ...Expr) Relation[M, T] {
	r.opts.where = append(slices.Clip(r.opts.where), exprs...)
	return r
}

// OrderBy orders the related rows. For has-many and many-to-many relations the
// order applies to each parent's list.
func (r Relation[M, T]) OrderBy(orders ...Order) Relation[M, T] {
	r.opts.order = append(slices.Clip(r.opts.order), orders...)
	return r
}

// Exclude leaves columns of the related table out. The columns that link the
// tables cannot be excluded.
func (r Relation[M, T]) Exclude(cols ...ColumnRef) Relation[M, T] {
	r.opts.exclude = append(slices.Clip(r.opts.exclude), cols...)
	return r
}

// With loads relations of the related rows too (nested preloading).
func (r Relation[M, T]) With(rels ...Preloader[T]) Relation[M, T] {
	r.opts.nested = append(slices.Clip(r.opts.nested), rels...)
	return r
}

func (r Relation[M, T]) preload(ctx context.Context, db *DB, parents []*M) error {
	if len(parents) == 0 {
		return nil
	}
	if err := r.load(ctx, db, parents, r.opts); err != nil {
		return fmt.Errorf("lathe: loading %s: %w", r.name, err)
	}
	return nil
}

func (r Relation[M, T]) requires() []string   { return r.cols }
func (r Relation[M, T]) relationName() string { return r.name }

// ---- relation kinds ----

// BelongsToSpec describes a relation where M holds the foreign key.
type BelongsToSpec[M, T any] struct {
	Source     *TableSpec[M]
	Target     *TableSpec[T]
	SourceCols []string // foreign key columns on M
	TargetCols []string // referenced columns on T
	Set        func(*M, *T)
}

// BelongsTo builds a many-to-one relation (Post.Author). Generated code calls it.
func BelongsTo[M, T any](name string, s BelongsToSpec[M, T]) Relation[M, T] {
	s.Source.init()
	s.Target.init()
	return Relation[M, T]{name: name, cols: s.SourceCols,
		load: func(ctx context.Context, db *DB, parents []*M, o relOpts[T]) error {
			groups, keys := groupParents(s.Source, parents, s.SourceCols)
			if len(keys) == 0 {
				return nil
			}
			rows, err := fetchByKeys(ctx, db, s.Target, s.TargetCols, keys, o)
			if err != nil {
				return err
			}
			for i := range rows {
				k, _ := keyOf(s.Target, &rows[i], s.TargetCols)
				for _, p := range groups[k] {
					c := rows[i]
					s.Set(p, &c)
				}
			}
			return nil
		}}
}

// HasManySpec describes a relation where T holds the foreign key.
type HasManySpec[M, T any] struct {
	Source     *TableSpec[M]
	Target     *TableSpec[T]
	SourceCols []string // referenced columns on M
	TargetCols []string // foreign key columns on T
	Set        func(*M, []T)
}

// HasMany builds a one-to-many relation (User.Posts). Parents without related
// rows get an empty, non-nil slice, so "loaded but empty" differs from "not
// loaded". Generated code calls it.
func HasMany[M, T any](name string, s HasManySpec[M, T]) Relation[M, T] {
	s.Source.init()
	s.Target.init()
	return Relation[M, T]{name: name, cols: s.SourceCols,
		load: func(ctx context.Context, db *DB, parents []*M, o relOpts[T]) error {
			groups, keys := groupParents(s.Source, parents, s.SourceCols)
			if len(keys) == 0 {
				return nil
			}
			rows, err := fetchByKeys(ctx, db, s.Target, s.TargetCols, keys, o)
			if err != nil {
				return err
			}
			children := map[string][]T{}
			for i := range rows {
				k, _ := keyOf(s.Target, &rows[i], s.TargetCols)
				children[k] = append(children[k], rows[i])
			}
			for k, ps := range groups {
				for _, p := range ps {
					list := append([]T{}, children[k]...)
					s.Set(p, list)
				}
			}
			return nil
		}}
}

// HasOneSpec describes a relation where T holds a unique foreign key.
type HasOneSpec[M, T any] struct {
	Source     *TableSpec[M]
	Target     *TableSpec[T]
	SourceCols []string
	TargetCols []string
	Set        func(*M, *T)
}

// HasOne builds a one-to-one relation (User.Profile). Generated code calls it.
func HasOne[M, T any](name string, s HasOneSpec[M, T]) Relation[M, T] {
	s.Source.init()
	s.Target.init()
	return Relation[M, T]{name: name, cols: s.SourceCols,
		load: func(ctx context.Context, db *DB, parents []*M, o relOpts[T]) error {
			groups, keys := groupParents(s.Source, parents, s.SourceCols)
			if len(keys) == 0 {
				return nil
			}
			rows, err := fetchByKeys(ctx, db, s.Target, s.TargetCols, keys, o)
			if err != nil {
				return err
			}
			for i := range rows {
				k, _ := keyOf(s.Target, &rows[i], s.TargetCols)
				for _, p := range groups[k] {
					c := rows[i]
					s.Set(p, &c)
				}
			}
			return nil
		}}
}

// ManyToManySpec describes a relation through a join table J.
type ManyToManySpec[M, T, J any] struct {
	Source            *TableSpec[M]
	Target            *TableSpec[T]
	Through           *TableSpec[J]
	SourceCols        []string // columns on M that J references
	TargetCols        []string // columns on T that J references
	ThroughSourceCols []string // J's foreign key to M
	ThroughTargetCols []string // J's foreign key to T
	Set               func(*M, []T)
}

// ManyToMany builds a many-to-many relation (Post.Tags). Generated code calls it.
//
// Rows are ordered by the relation's OrderBy across all parents at once, so each
// parent's list is ordered correctly as long as fewer than half the driver's
// parameter limit of distinct related rows are loaded in one call.
func ManyToMany[M, T, J any](name string, s ManyToManySpec[M, T, J]) Relation[M, T] {
	s.Source.init()
	s.Target.init()
	s.Through.init()
	return Relation[M, T]{name: name, cols: s.SourceCols,
		load: func(ctx context.Context, db *DB, parents []*M, o relOpts[T]) error {
			groups, keys := groupParents(s.Source, parents, s.SourceCols)
			if len(keys) == 0 {
				return nil
			}
			links, err := fetchByKeys(ctx, db, s.Through, s.ThroughSourceCols, keys, relOpts[J]{})
			if err != nil {
				return err
			}
			parentsOf := map[string][]string{} // target key -> source keys
			seenPair := map[string]bool{}
			var targetKeys [][]any
			seenTarget := map[string]bool{}
			for i := range links {
				sk, ok1 := keyOf(s.Through, &links[i], s.ThroughSourceCols)
				tvals, ok2 := keyValues(s.Through, &links[i], s.ThroughTargetCols)
				if !ok1 || !ok2 {
					continue
				}
				tk := keyString(tvals)
				if !seenTarget[tk] {
					seenTarget[tk] = true
					targetKeys = append(targetKeys, tvals)
				}
				if pair := sk + "\x01" + tk; !seenPair[pair] {
					seenPair[pair] = true
					parentsOf[tk] = append(parentsOf[tk], sk)
				}
			}

			perParent := map[string][]T{}
			if len(targetKeys) > 0 {
				rows, err := fetchByKeys(ctx, db, s.Target, s.TargetCols, targetKeys, o)
				if err != nil {
					return err
				}
				for i := range rows {
					tk, _ := keyOf(s.Target, &rows[i], s.TargetCols)
					for _, sk := range parentsOf[tk] {
						perParent[sk] = append(perParent[sk], rows[i])
					}
				}
			}
			for sk, ps := range groups {
				for _, p := range ps {
					s.Set(p, append([]T{}, perParent[sk]...))
				}
			}
			return nil
		}}
}

// ---- machinery ----

// runPreloads runs each relation for a batch of parents.
func runPreloads[M any](ctx context.Context, db *DB, parents []*M, rels []Preloader[M]) error {
	if len(parents) == 0 {
		return nil
	}
	for _, r := range rels {
		if err := r.preload(ctx, db, parents); err != nil {
			return err
		}
	}
	return nil
}

// keyValues reads the named columns of row, dereferencing pointer fields. ok is
// false when any of them is NULL, in which case the row has no related rows.
func keyValues[M any](spec *TableSpec[M], row *M, cols []string) ([]any, bool) {
	vals := make([]any, len(cols))
	for i, c := range cols {
		fi, found := spec.byCol[c]
		if !found {
			return nil, false
		}
		rv := reflect.ValueOf(spec.Fields[fi].Ptr(row)).Elem()
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return nil, false
			}
			rv = rv.Elem()
		}
		vals[i] = rv.Interface()
	}
	return vals, true
}

// keyString turns key values into a map key. Values are formatted rather than
// used as-is so equal keys match even when the two sides use different Go types
// for the same column (for example a named enum type).
func keyString(vals []any) string {
	if len(vals) == 1 {
		return fmt.Sprint(vals[0])
	}
	var b strings.Builder
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(0)
		}
		fmt.Fprint(&b, v)
	}
	return b.String()
}

func keyOf[M any](spec *TableSpec[M], row *M, cols []string) (string, bool) {
	vals, ok := keyValues(spec, row, cols)
	if !ok {
		return "", false
	}
	return keyString(vals), true
}

// groupParents indexes parents by their key, skipping parents whose key is NULL.
func groupParents[M any](spec *TableSpec[M], parents []*M, cols []string) (map[string][]*M, [][]any) {
	groups := map[string][]*M{}
	var keys [][]any
	for _, p := range parents {
		vals, ok := keyValues(spec, p, cols)
		if !ok {
			continue
		}
		k := keyString(vals)
		if _, seen := groups[k]; !seen {
			keys = append(keys, vals)
		}
		groups[k] = append(groups[k], p)
	}
	return groups, keys
}

// keyCondition matches rows whose columns equal any of the key tuples.
func keyCondition(table string, cols []string, keys [][]any) Expr {
	if len(cols) == 1 {
		vals := make([]any, len(keys))
		for i, k := range keys {
			vals[i] = k[0]
		}
		return inList{col: NewColumn[any](table, cols[0]), vals: vals}
	}
	alts := make([]Expr, len(keys))
	for i, k := range keys {
		parts := make([]Expr, len(cols))
		for j, c := range cols {
			parts[j] = binary{NewColumn[any](table, c), "=", value{k[j]}}
		}
		alts[i] = logical{op: "AND", parts: parts}
	}
	return logical{op: "OR", parts: alts}
}

// fetchByKeys loads the rows of T whose key columns match any of keys, in
// chunks that respect the driver's parameter limit.
func fetchByKeys[T any](ctx context.Context, db *DB, spec *TableSpec[T], cols []string, keys [][]any, o relOpts[T]) ([]T, error) {
	spec.init()
	for _, c := range o.exclude {
		if c.TableName() == spec.Table && slices.Contains(cols, c.ColumnName()) {
			return nil, fmt.Errorf("cannot exclude %s.%s: it links the related rows to their parents", spec.Table, c.ColumnName())
		}
	}
	m := &Model[T]{db: db, spec: spec}
	chunk := db.dialect.MaxParams() / 2 / len(cols)
	if chunk < 1 {
		chunk = 1
	}
	var out []T
	for start := 0; start < len(keys); start += chunk {
		end := min(start+chunk, len(keys))
		q := m.FindMany().Where(keyCondition(spec.Table, cols, keys[start:end])).Where(o.where...).OrderBy(o.order...)
		if len(o.exclude) > 0 {
			q.Exclude(o.exclude...)
		}
		if len(o.nested) > 0 {
			q.With(o.nested...)
		}
		rows, err := q.All(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// checkRequirements makes sure a query selects the columns its relations need.
func checkRequirements[M any](m *Model[M], selected []int, rels []Preloader[M]) error {
	have := map[string]bool{}
	for _, i := range selected {
		have[m.spec.Fields[i].Column] = true
	}
	var errs []error
	for _, r := range rels {
		for _, c := range r.requires() {
			if !have[c] {
				errs = append(errs, fmt.Errorf("With(%s) needs the column %s.%s, which this query does not select", r.relationName(), m.spec.Table, c))
			}
		}
	}
	return errors.Join(errs...)
}

package lathe

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

// Row is one row of a [ReturnQuery]: the values of exactly the columns passed
// to [FindQuery.Returning], in that order. NULL comes back as nil. Because no
// other column is ever scanned, a field you did not ask for can never be
// mistaken for an empty one (a uuid.Nil or an empty string).
type Row []any

// ReturnQuery is the projection form of a [FindQuery]: it returns rows
// containing only the columns given to [FindQuery.Returning]. Build it with
// FindQuery.Returning; a ReturnQuery must not be shared between goroutines.
type ReturnQuery[M any] struct {
	m         *Model[M]
	cols      []int
	where     []Expr
	order     []Order
	limit     int64
	offset    int64
	withError error
}

// Returning turns the query into a projection. Every row it returns holds only
// the given columns, in the given order, with nil for NULL; [ReturnQuery.All]
// and [ReturnQuery.One] return [Row]s whose positions line up 1:1 with cols.
// Unlike [FindQuery.Select] and [FindQuery.Exclude], a column left out of
// Returning never surfaces as a fabricated zero value, so this is the safe
// choice when a column may be empty (a nullable email) and you need to tell
// "not requested" apart from "empty".
//
// Returning defines its own column list: it ignores any [FindQuery.Select], and
// repeated columns keep their position and appear once per mention. It cannot
// be combined with [FindQuery.With], because relation loading needs whole rows.
func (q *FindQuery[M]) Returning(cols ...ColumnRef) *ReturnQuery[M] {
	rq := &ReturnQuery[M]{m: q.m, where: q.where, order: q.order, limit: q.limit, offset: q.offset}
	if len(q.rels) > 0 {
		rq.withError = errors.New("lathe: Returning cannot be combined with With")
	}
	if q.err != nil {
		rq.withError = errors.Join(rq.withError, q.err)
	}
	for _, c := range cols {
		i, err := q.m.index(c)
		if err != nil {
			rq.withError = errors.Join(rq.withError, err)
			continue
		}
		rq.cols = append(rq.cols, i)
	}
	return rq
}

// Where adds conditions, combined with AND.
func (q *ReturnQuery[M]) Where(exprs ...Expr) *ReturnQuery[M] {
	q.where = append(q.where, exprs...)
	return q
}

// OrderBy adds ORDER BY terms.
func (q *ReturnQuery[M]) OrderBy(orders ...Order) *ReturnQuery[M] {
	q.order = append(q.order, orders...)
	return q
}

// Limit caps the number of rows.
func (q *ReturnQuery[M]) Limit(n int) *ReturnQuery[M] { q.limit = int64(n); return q }

// Offset skips rows.
func (q *ReturnQuery[M]) Offset(n int) *ReturnQuery[M] { q.offset = int64(n); return q }

// Build renders the query without running it, for debugging and tests.
func (q *ReturnQuery[M]) Build() (string, []any, error) { return q.build(q.limit) }

func (q *ReturnQuery[M]) build(limit int64) (string, []any, error) {
	if q.withError != nil {
		return "", nil, q.withError
	}
	if len(q.cols) == 0 {
		return "", nil, errors.New("lathe: Returning needs at least one column")
	}
	b := newBuilder(q.m.db.dialect)
	b.write("SELECT ")
	for i, fi := range q.cols {
		if i > 0 {
			b.write(", ")
		}
		b.column(q.m.spec.Table, q.m.spec.Fields[fi].Column)
	}
	b.write(" FROM ")
	b.ident(q.m.spec.Table)
	writeWhere(b, q.where)
	writeOrder(b, q.order)
	b.write(b.d.LimitOffset(limit, q.offset))
	return b.String(), b.args, b.err
}

// All runs the query and returns every row.
func (q *ReturnQuery[M]) All(ctx context.Context) ([]Row, error) {
	sqlText, args, err := q.build(q.limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.m.db.query(ctx, sqlText, args)
	if err != nil {
		return nil, err
	}
	return q.scan(rows)
}

// One returns the first row, or [ErrNotFound].
func (q *ReturnQuery[M]) One(ctx context.Context) (Row, error) {
	limit := q.limit
	if limit < 0 || limit > 1 {
		limit = 1
	}
	sqlText, args, err := q.build(limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.m.db.query(ctx, sqlText, args)
	if err != nil {
		return nil, err
	}
	out, err := q.scan(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out[0], nil
}

func (q *ReturnQuery[M]) scan(rows *sql.Rows) ([]Row, error) {
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row M
		dests := make([]any, len(q.cols))
		for i, fi := range q.cols {
			dests[i] = q.m.spec.Fields[fi].Ptr(&row)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, q.m.db.wrap(err)
		}
		out = append(out, rowValues(dests))
	}
	return out, q.m.db.wrap(rows.Err())
}

// rowValues dereferences the scan destinations back to the model-typed values.
// A nil pointer (a NULL column, or a nilable field that stayed nil) becomes nil
// in the row; anything else keeps the model's Go type, so a string comes back
// as string, not []byte.
func rowValues(dests []any) Row {
	r := make(Row, len(dests))
	for i, d := range dests {
		v := reflect.ValueOf(d).Elem()
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				r[i] = nil
				continue
			}
			r[i] = v.Elem().Interface()
			continue
		}
		r[i] = v.Interface()
	}
	return r
}

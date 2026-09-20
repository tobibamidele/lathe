package lathe

import (
	"context"
	"errors"
)

type join struct {
	kind string
	src  Source
	on   Expr
}

// SelectQuery is a free-form SELECT for projections, joins and aggregates. For
// plain "give me rows of this table" queries use a model's FindMany.
type SelectQuery struct {
	cols     []Selectable
	from     Source
	joins    []join
	where    []Expr
	groupBy  []Expr
	having   []Expr
	order    []Order
	limit    int64
	offset   int64
	distinct bool
}

// Select starts a query with the given select list.
func Select(cols ...Selectable) *SelectQuery {
	return &SelectQuery{cols: cols, limit: -1}
}

// Distinct turns the query into SELECT DISTINCT.
func (q *SelectQuery) Distinct() *SelectQuery { q.distinct = true; return q }

// From sets the primary table.
func (q *SelectQuery) From(src Source) *SelectQuery { q.from = src; return q }

// Join adds an INNER JOIN.
func (q *SelectQuery) Join(src Source, on Expr) *SelectQuery {
	q.joins = append(q.joins, join{"INNER JOIN", src, on})
	return q
}

// LeftJoin adds a LEFT JOIN.
func (q *SelectQuery) LeftJoin(src Source, on Expr) *SelectQuery {
	q.joins = append(q.joins, join{"LEFT JOIN", src, on})
	return q
}

// Where adds conditions, combined with AND.
func (q *SelectQuery) Where(exprs ...Expr) *SelectQuery {
	q.where = append(q.where, exprs...)
	return q
}

// GroupBy sets the GROUP BY terms.
func (q *SelectQuery) GroupBy(exprs ...Expr) *SelectQuery {
	q.groupBy = append(q.groupBy, exprs...)
	return q
}

// Having adds HAVING conditions, combined with AND.
func (q *SelectQuery) Having(exprs ...Expr) *SelectQuery {
	q.having = append(q.having, exprs...)
	return q
}

// OrderBy adds ORDER BY terms.
func (q *SelectQuery) OrderBy(orders ...Order) *SelectQuery {
	q.order = append(q.order, orders...)
	return q
}

// Limit caps the number of rows.
func (q *SelectQuery) Limit(n int) *SelectQuery { q.limit = int64(n); return q }

// Offset skips rows.
func (q *SelectQuery) Offset(n int) *SelectQuery { q.offset = int64(n); return q }

func (q *SelectQuery) write(b *builder) {
	if len(q.cols) == 0 {
		b.fail(errors.New("lathe: Select needs at least one column"))
		return
	}
	if q.from == nil {
		b.fail(errors.New("lathe: Select needs a From table"))
		return
	}
	b.write("SELECT ")
	if q.distinct {
		b.write("DISTINCT ")
	}
	for i, c := range q.cols {
		if i > 0 {
			b.write(", ")
		}
		c.appendSelect(b)
	}
	b.write(" FROM ")
	b.ident(q.from.TableName())
	for _, j := range q.joins {
		b.write(" " + j.kind + " ")
		b.ident(j.src.TableName())
		b.write(" ON ")
		j.on.appendSQL(b)
	}
	writeWhere(b, q.where)
	if len(q.groupBy) > 0 {
		b.write(" GROUP BY ")
		for i, g := range q.groupBy {
			if i > 0 {
				b.write(", ")
			}
			g.appendSQL(b)
		}
	}
	if len(q.having) > 0 {
		b.write(" HAVING ")
		And(q.having...).appendSQL(b)
	}
	writeOrder(b, q.order)
	b.write(b.d.LimitOffset(q.limit, q.offset))
}

// appendSQL lets a query be used as a subquery.
func (q *SelectQuery) appendSQL(b *builder) {
	b.write("(")
	q.write(b)
	b.write(")")
}

// Build renders the query for d. It is meant for debugging and tests; the
// terminal helpers ([Scan], [ScanOne]) render and run it for you.
func (q *SelectQuery) Build(d Dialect) (string, []any, error) {
	b := newBuilder(d)
	q.write(b)
	return b.String(), b.args, b.err
}

func writeWhere(b *builder, where []Expr) {
	if len(where) == 0 {
		return
	}
	b.write(" WHERE ")
	And(where...).appendSQL(b)
}

func writeOrder(b *builder, order []Order) {
	if len(order) == 0 {
		return
	}
	b.write(" ORDER BY ")
	for i, o := range order {
		if i > 0 {
			b.write(", ")
		}
		o.appendOrder(b)
	}
}

// Scan runs q and scans every row into a T. T is a struct, whose fields are
// matched to result columns by their `db` tag or by name (case and underscores
// are ignored, so an alias "total_paid" fills TotalPaid), or a single scalar
// for one-column queries. Use pointer fields for nullable columns.
func Scan[T any](ctx context.Context, db *DB, q *SelectQuery) ([]T, error) {
	sqlText, args, err := q.Build(db.dialect)
	if err != nil {
		return nil, err
	}
	rows, err := db.query(ctx, sqlText, args)
	if err != nil {
		return nil, err
	}
	return scanAll[T](rows)
}

// ScanOne is like [Scan] but returns the first row, or [ErrNotFound].
func ScanOne[T any](ctx context.Context, db *DB, q *SelectQuery) (T, error) {
	one := *q
	if one.limit < 0 || one.limit > 1 {
		one.limit = 1
	}
	var zero T
	out, err := Scan[T](ctx, db, &one)
	if err != nil {
		return zero, err
	}
	if len(out) == 0 {
		return zero, ErrNotFound
	}
	return out[0], nil
}

// Raw runs a raw SQL query (? placeholders on every dialect) and scans the
// rows like [Scan].
func Raw[T any](ctx context.Context, db *DB, query string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanAll[T](rows)
}

// RawOne is like [Raw] but returns the first row, or [ErrNotFound].
func RawOne[T any](ctx context.Context, db *DB, query string, args ...any) (T, error) {
	var zero T
	out, err := Raw[T](ctx, db, query, args...)
	if err != nil {
		return zero, err
	}
	if len(out) == 0 {
		return zero, ErrNotFound
	}
	return out[0], nil
}

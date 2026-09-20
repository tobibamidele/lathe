package lathe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Field describes one column of a generated model. Generated code builds a
// [TableSpec] out of these; user code does not need them.
type Field[M any] struct {
	Column        string
	Ptr           func(*M) any // pointer to the struct field
	PrimaryKey    bool
	AutoIncrement bool // the database assigns the value
	HasDefault    bool // a zero value means "not set": leave it out of INSERT so the database default applies

	// Default, when set, fills the field in Go before INSERT if it still holds
	// its zero value (client-side defaults such as random UUIDs). A field it
	// fills is sent like any other value, so it can be read back on every
	// dialect.
	Default func(*M)
}

// TableSpec describes a generated model M.
type TableSpec[M any] struct {
	Table  string
	Fields []Field[M]

	once  sync.Once
	byCol map[string]int
	pk    []int
}

func (s *TableSpec[M]) init() {
	s.once.Do(func() {
		s.byCol = make(map[string]int, len(s.Fields))
		for i, f := range s.Fields {
			s.byCol[f.Column] = i
			if f.PrimaryKey {
				s.pk = append(s.pk, i)
			}
		}
	})
}

// Model gives access to one table through its generated struct M.
type Model[M any] struct {
	db   *DB
	spec *TableSpec[M]
}

// NewModel binds a table spec to a database handle. Generated clients call it.
func NewModel[M any](db *DB, spec *TableSpec[M]) *Model[M] {
	spec.init()
	return &Model[M]{db: db, spec: spec}
}

// DB returns the handle the model runs on.
func (m *Model[M]) DB() *DB { return m.db }

// TableName returns the table name.
func (m *Model[M]) TableName() string { return m.spec.Table }

func (m *Model[M]) index(ref ColumnRef) (int, error) {
	if ref.TableName() != m.spec.Table {
		return 0, fmt.Errorf("lathe: column %s.%s does not belong to table %s", ref.TableName(), ref.ColumnName(), m.spec.Table)
	}
	i, ok := m.spec.byCol[ref.ColumnName()]
	if !ok {
		return 0, fmt.Errorf("lathe: table %s has no column %s", m.spec.Table, ref.ColumnName())
	}
	return i, nil
}

func (m *Model[M]) allFields() []int {
	idx := make([]int, len(m.spec.Fields))
	for i := range idx {
		idx[i] = i
	}
	return idx
}

func (m *Model[M]) columnList(idx []int) string {
	names := make([]string, len(idx))
	for i, fi := range idx {
		names[i] = m.db.dialect.Quote(m.spec.Fields[fi].Column)
	}
	return strings.Join(names, ", ")
}

func (m *Model[M]) scanRows(rows *sql.Rows, idx []int) ([]M, error) {
	defer rows.Close()
	var out []M
	dests := make([]any, len(idx))
	for rows.Next() {
		var row M
		for i, fi := range idx {
			dests[i] = m.spec.Fields[fi].Ptr(&row)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, m.db.wrap(err)
		}
		out = append(out, row)
	}
	return out, m.db.wrap(rows.Err())
}

// ---- reading ----

// FindMany starts a query for any number of rows.
func (m *Model[M]) FindMany() *FindQuery[M] { return &FindQuery[M]{m: m, limit: -1} }

// FindFirst starts a query for a single row.
func (m *Model[M]) FindFirst() *FindQuery[M] { return &FindQuery[M]{m: m, limit: 1} }

// Count counts rows matching all of the conditions.
func (m *Model[M]) Count(ctx context.Context, where ...Expr) (int64, error) {
	return m.FindMany().Where(where...).Count(ctx)
}

// Exists reports whether any row matches all of the conditions.
func (m *Model[M]) Exists(ctx context.Context, where ...Expr) (bool, error) {
	return m.FindMany().Where(where...).Exists(ctx)
}

// FindQuery reads rows of a model. Methods modify and return the query, so a
// FindQuery must not be shared between goroutines.
type FindQuery[M any] struct {
	m       *Model[M]
	where   []Expr
	order   []Order
	limit   int64
	offset  int64
	only    map[int]bool
	exclude map[int]bool
	err     error
}

// Where adds conditions, combined with AND.
func (q *FindQuery[M]) Where(exprs ...Expr) *FindQuery[M] {
	q.where = append(q.where, exprs...)
	return q
}

// OrderBy adds ORDER BY terms.
func (q *FindQuery[M]) OrderBy(orders ...Order) *FindQuery[M] {
	q.order = append(q.order, orders...)
	return q
}

// Limit caps the number of rows.
func (q *FindQuery[M]) Limit(n int) *FindQuery[M] { q.limit = int64(n); return q }

// Offset skips rows.
func (q *FindQuery[M]) Offset(n int) *FindQuery[M] { q.offset = int64(n); return q }

// Select restricts the query to the given columns. The other fields of the
// returned structs keep their zero values.
func (q *FindQuery[M]) Select(cols ...ColumnRef) *FindQuery[M] {
	if q.only == nil {
		q.only = map[int]bool{}
	}
	for _, c := range cols {
		i, err := q.m.index(c)
		if err != nil {
			q.err = errors.Join(q.err, err)
			continue
		}
		q.only[i] = true
	}
	return q
}

// Exclude leaves columns out of the query (a password hash, a blob...). The
// matching fields of the returned structs keep their zero values.
func (q *FindQuery[M]) Exclude(cols ...ColumnRef) *FindQuery[M] {
	if q.exclude == nil {
		q.exclude = map[int]bool{}
	}
	for _, c := range cols {
		i, err := q.m.index(c)
		if err != nil {
			q.err = errors.Join(q.err, err)
			continue
		}
		q.exclude[i] = true
	}
	return q
}

func (q *FindQuery[M]) selected() []int {
	var idx []int
	for _, i := range q.m.allFields() {
		if (q.only == nil || q.only[i]) && !q.exclude[i] {
			idx = append(idx, i)
		}
	}
	return idx
}

func (q *FindQuery[M]) build(limit int64, idx []int) (string, []any, error) {
	if q.err != nil {
		return "", nil, q.err
	}
	if len(idx) == 0 {
		return "", nil, errors.New("lathe: the query selects no columns")
	}
	b := newBuilder(q.m.db.dialect)
	b.write("SELECT ")
	for i, fi := range idx {
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

// Build renders the query without running it, for debugging and tests.
func (q *FindQuery[M]) Build() (string, []any, error) { return q.build(q.limit, q.selected()) }

// All runs the query and returns every row.
func (q *FindQuery[M]) All(ctx context.Context) ([]M, error) {
	idx := q.selected()
	sqlText, args, err := q.build(q.limit, idx)
	if err != nil {
		return nil, err
	}
	rows, err := q.m.db.query(ctx, sqlText, args)
	if err != nil {
		return nil, err
	}
	return q.m.scanRows(rows, idx)
}

// One returns the first row, or [ErrNotFound].
func (q *FindQuery[M]) One(ctx context.Context) (*M, error) {
	idx := q.selected()
	sqlText, args, err := q.build(1, idx)
	if err != nil {
		return nil, err
	}
	rows, err := q.m.db.query(ctx, sqlText, args)
	if err != nil {
		return nil, err
	}
	out, err := q.m.scanRows(rows, idx)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return &out[0], nil
}

// Count returns the number of matching rows (ignoring ordering and paging).
func (q *FindQuery[M]) Count(ctx context.Context) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	b := newBuilder(q.m.db.dialect)
	b.write("SELECT COUNT(*) FROM ")
	b.ident(q.m.spec.Table)
	writeWhere(b, q.where)
	if b.err != nil {
		return 0, b.err
	}
	rows, err := q.m.db.query(ctx, b.String(), b.args)
	if err != nil {
		return 0, err
	}
	n, err := scanAll[int64](rows, q.m.db.wrap)
	if err != nil {
		return 0, err
	}
	if len(n) != 1 {
		return 0, errors.New("lathe: COUNT returned no row")
	}
	return n[0], nil
}

// Exists reports whether at least one row matches.
func (q *FindQuery[M]) Exists(ctx context.Context) (bool, error) {
	if q.err != nil {
		return false, q.err
	}
	b := newBuilder(q.m.db.dialect)
	b.write("SELECT 1 FROM ")
	b.ident(q.m.spec.Table)
	writeWhere(b, q.where)
	b.write(b.d.LimitOffset(1, 0))
	if b.err != nil {
		return false, b.err
	}
	rows, err := q.m.db.query(ctx, b.String(), b.args)
	if err != nil {
		return false, err
	}
	n, err := scanAll[int64](rows, q.m.db.wrap)
	return len(n) > 0, err
}

// ---- inserting ----

func isZeroPtr(ptr any) bool { return reflect.ValueOf(ptr).Elem().IsZero() }

func valueOf(ptr any) any { return reflect.ValueOf(ptr).Elem().Interface() }

func setInt(ptr any, id int64) {
	switch p := ptr.(type) {
	case *int64:
		*p = id
	case *int32:
		*p = int32(id)
	case *int16:
		*p = int16(id)
	case *int:
		*p = int(id)
	default:
		reflect.ValueOf(ptr).Elem().SetInt(id)
	}
}

// insertPlan decides which columns take part in an INSERT: database generated
// columns (auto increment, defaults) are left out while they hold their zero
// value, so the database fills them.
type insertPlan struct {
	cols     []int // columns sent
	defaults []int // omitted columns that have a default (refreshed after insert)
	auto     int   // omitted auto increment column, or -1
}

func (m *Model[M]) planInsert(row *M) insertPlan {
	p := insertPlan{auto: -1}
	for i, f := range m.spec.Fields {
		ptr := f.Ptr(row)
		if f.Default != nil && isZeroPtr(ptr) {
			f.Default(row)
		}
		switch {
		case (f.AutoIncrement || f.HasDefault) && isZeroPtr(ptr):
			if f.AutoIncrement {
				p.auto = i
			} else {
				p.defaults = append(p.defaults, i)
			}
		default:
			p.cols = append(p.cols, i)
		}
	}
	return p
}

// Create inserts row. Columns the database generates (auto increment keys,
// defaults such as timestamps) are read back into row.
//
// Columns whose zero value means "not set" are left out of the INSERT so the
// database fills them in: auto increment keys, columns defaulting to now, a
// UUID or an expression, enums with a default, and nullable columns with any
// default (a nil pointer). Zero bools, numbers and strings are always sent,
// because they are legitimate values; make such a column nullable to get its
// constant default when the field is nil.
func (m *Model[M]) Create(ctx context.Context, row *M) error {
	d := m.db.dialect
	p := m.planInsert(row)
	if !d.SupportsReturning() {
		for _, di := range p.defaults {
			if m.spec.Fields[di].PrimaryKey {
				return fmt.Errorf("lathe: %s.%s is a primary key generated by a database default, which %s cannot read back after INSERT; assign it in Go",
					m.spec.Table, m.spec.Fields[di].Column, d.Name())
			}
		}
	}

	b := newBuilder(d)
	if len(p.cols) == 0 {
		b.write(d.DefaultValues(d.Quote(m.spec.Table)))
	} else {
		b.write("INSERT INTO ")
		b.ident(m.spec.Table)
		b.write(" (" + m.columnList(p.cols) + ") VALUES (")
		for i, ci := range p.cols {
			if i > 0 {
				b.write(", ")
			}
			b.arg(valueOf(m.spec.Fields[ci].Ptr(row)))
		}
		b.write(")")
	}

	if d.SupportsReturning() {
		all := m.allFields()
		b.write(" RETURNING " + m.columnList(all))
		rows, err := m.db.query(ctx, b.String(), b.args)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return m.db.wrap(err) // the database reports INSERT errors here
			}
			return errors.New("lathe: INSERT returned no row")
		}
		dests := make([]any, len(all))
		for i, fi := range all {
			dests[i] = m.spec.Fields[fi].Ptr(row)
		}
		if err := rows.Scan(dests...); err != nil {
			return m.db.wrap(err)
		}
		return m.db.wrap(rows.Err())
	}

	res, err := m.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	if p.auto >= 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		setInt(m.spec.Fields[p.auto].Ptr(row), id)
	}
	return m.refresh(ctx, row, p.defaults)
}

// refresh reads the given columns of row back by primary key.
func (m *Model[M]) refresh(ctx context.Context, row *M, cols []int) error {
	if len(cols) == 0 {
		return nil
	}
	b := newBuilder(m.db.dialect)
	b.write("SELECT " + m.columnList(cols) + " FROM ")
	b.ident(m.spec.Table)
	b.write(" WHERE ")
	for i, pi := range m.spec.pk {
		if i > 0 {
			b.write(" AND ")
		}
		b.ident(m.spec.Fields[pi].Column)
		b.write(" = ")
		b.arg(valueOf(m.spec.Fields[pi].Ptr(row)))
	}
	rows, err := m.db.query(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return m.db.wrap(err)
		}
		return ErrNotFound
	}
	dests := make([]any, len(cols))
	for i, ci := range cols {
		dests[i] = m.spec.Fields[ci].Ptr(row)
	}
	if err := rows.Scan(dests...); err != nil {
		return m.db.wrap(err)
	}
	return m.db.wrap(rows.Err())
}

// CreateMany inserts rows in one transaction, batching them into multi-row
// INSERT statements where the database supports RETURNING. Generated columns
// are read back into rows.
func (m *Model[M]) CreateMany(ctx context.Context, rows []M) error {
	if len(rows) == 0 {
		return nil
	}
	return m.db.Tx(ctx, func(tx *DB) error {
		mm := &Model[M]{db: tx, spec: m.spec}
		if !tx.dialect.SupportsReturning() {
			for i := range rows {
				if err := mm.Create(ctx, &rows[i]); err != nil {
					return err
				}
			}
			return nil
		}
		return mm.createBatch(ctx, rows)
	})
}

func (m *Model[M]) createBatch(ctx context.Context, rows []M) error {
	type group struct {
		cols []int
		rows []int
	}
	var order []string
	groups := map[string]*group{}
	for i := range rows {
		p := m.planInsert(&rows[i])
		key := fmt.Sprint(p.cols)
		g, ok := groups[key]
		if !ok {
			g = &group{cols: p.cols}
			groups[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, i)
	}

	d := m.db.dialect
	all := m.allFields()
	for _, key := range order {
		g := groups[key]
		if len(g.cols) == 0 { // DEFAULT VALUES cannot be batched
			for _, ri := range g.rows {
				if err := m.Create(ctx, &rows[ri]); err != nil {
					return err
				}
			}
			continue
		}
		per := d.MaxParams() / len(g.cols)
		if per < 1 {
			per = 1
		}
		for start := 0; start < len(g.rows); start += per {
			end := start + per
			if end > len(g.rows) {
				end = len(g.rows)
			}
			chunk := g.rows[start:end]

			b := newBuilder(d)
			b.write("INSERT INTO ")
			b.ident(m.spec.Table)
			b.write(" (" + m.columnList(g.cols) + ") VALUES ")
			for i, ri := range chunk {
				if i > 0 {
					b.write(", ")
				}
				b.write("(")
				for j, ci := range g.cols {
					if j > 0 {
						b.write(", ")
					}
					b.arg(valueOf(m.spec.Fields[ci].Ptr(&rows[ri])))
				}
				b.write(")")
			}
			b.write(" RETURNING " + m.columnList(all))

			res, err := m.db.query(ctx, b.String(), b.args)
			if err != nil {
				return err
			}
			n := 0
			for res.Next() {
				if n >= len(chunk) {
					res.Close()
					return errors.New("lathe: INSERT returned more rows than were inserted")
				}
				dests := make([]any, len(all))
				for i, fi := range all {
					dests[i] = m.spec.Fields[fi].Ptr(&rows[chunk[n]])
				}
				if err := res.Scan(dests...); err != nil {
					res.Close()
					return m.db.wrap(err)
				}
				n++
			}
			err = res.Err()
			res.Close()
			if err != nil {
				return m.db.wrap(err)
			}
			if n != len(chunk) {
				return fmt.Errorf("lathe: INSERT returned %d rows for %d inserted", n, len(chunk))
			}
		}
	}
	return nil
}

// ---- updating and deleting ----

// Save writes every non-key column of row to the row with the same primary
// key. It returns [ErrNotFound] if no such row exists.
//
// On MySQL, pass clientFoundRows=true in the DSN (mysql.Open does) so an
// UPDATE that changes nothing still counts as a match.
func (m *Model[M]) Save(ctx context.Context, row *M) error {
	var set []int
	for i, f := range m.spec.Fields {
		if !f.PrimaryKey {
			set = append(set, i)
		}
	}
	if len(set) == 0 {
		return nil
	}
	b := newBuilder(m.db.dialect)
	b.write("UPDATE ")
	b.ident(m.spec.Table)
	b.write(" SET ")
	for i, si := range set {
		if i > 0 {
			b.write(", ")
		}
		b.ident(m.spec.Fields[si].Column)
		b.write(" = ")
		b.arg(valueOf(m.spec.Fields[si].Ptr(row)))
	}
	b.write(" WHERE ")
	for i, pi := range m.spec.pk {
		if i > 0 {
			b.write(" AND ")
		}
		b.ident(m.spec.Fields[pi].Column)
		b.write(" = ")
		b.arg(valueOf(m.spec.Fields[pi].Ptr(row)))
	}
	res, err := m.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Update starts a bulk UPDATE.
func (m *Model[M]) Update() *UpdateQuery {
	return &UpdateQuery{db: m.db, table: m.spec.Table}
}

// Delete starts a DELETE.
func (m *Model[M]) Delete() *DeleteQuery {
	return &DeleteQuery{db: m.db, table: m.spec.Table}
}

// UpdateQuery is a bulk UPDATE.
type UpdateQuery struct {
	db    *DB
	table string
	sets  []Assignment
	where []Expr
	all   bool
}

// Set adds assignments, built with Column.Set, Column.SetNull, Incr and friends.
func (q *UpdateQuery) Set(assignments ...Assignment) *UpdateQuery {
	q.sets = append(q.sets, assignments...)
	return q
}

// Where adds conditions, combined with AND.
func (q *UpdateQuery) Where(exprs ...Expr) *UpdateQuery {
	q.where = append(q.where, exprs...)
	return q
}

// AllRows confirms that the update is meant to touch every row.
func (q *UpdateQuery) AllRows() *UpdateQuery { q.all = true; return q }

// Exec runs the update and returns the number of rows matched.
func (q *UpdateQuery) Exec(ctx context.Context) (int64, error) {
	if len(q.sets) == 0 {
		return 0, errors.New("lathe: Update needs at least one Set(...)")
	}
	if len(q.where) == 0 && !q.all {
		return 0, ErrMissingWhere
	}
	b := newBuilder(q.db.dialect)
	b.write("UPDATE ")
	b.ident(q.table)
	b.write(" SET ")
	for i, a := range q.sets {
		if a.col.TableName() != q.table {
			return 0, fmt.Errorf("lathe: cannot set %s.%s in an update of %s", a.col.TableName(), a.col.ColumnName(), q.table)
		}
		if i > 0 {
			b.write(", ")
		}
		b.ident(a.col.ColumnName())
		b.write(" = ")
		a.val.appendSQL(b)
	}
	writeWhere(b, q.where)
	if b.err != nil {
		return 0, b.err
	}
	res, err := q.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteQuery is a DELETE.
type DeleteQuery struct {
	db    *DB
	table string
	where []Expr
	all   bool
}

// Where adds conditions, combined with AND.
func (q *DeleteQuery) Where(exprs ...Expr) *DeleteQuery {
	q.where = append(q.where, exprs...)
	return q
}

// AllRows confirms that the delete is meant to touch every row.
func (q *DeleteQuery) AllRows() *DeleteQuery { q.all = true; return q }

// Exec runs the delete and returns the number of rows removed.
func (q *DeleteQuery) Exec(ctx context.Context) (int64, error) {
	if len(q.where) == 0 && !q.all {
		return 0, ErrMissingWhere
	}
	b := newBuilder(q.db.dialect)
	b.write("DELETE FROM ")
	b.ident(q.table)
	writeWhere(b, q.where)
	if b.err != nil {
		return 0, b.err
	}
	res, err := q.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

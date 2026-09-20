package lathe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// UpsertQuery inserts a row, or resolves a conflict with an existing one.
//
//	err := client.Users.Upsert(&u).
//		OnConflict(db.Users.Email).
//		DoUpdate(db.Users.Name, db.Users.Bio).
//		Exec(ctx)
//
// It becomes INSERT ... ON CONFLICT (email) DO UPDATE on PostgreSQL and SQLite
// and INSERT ... ON DUPLICATE KEY UPDATE on MySQL.
//
// After Exec the row reflects what is in the database: the inserted row, the
// updated row, or, with DoNothing, the row that was already there. (That needs
// OnConflict columns to look it up on MySQL, and after a skipped DoNothing
// everywhere.)
type UpsertQuery[M any] struct {
	m         *Model[M]
	row       *M
	target    []ColumnRef
	nothing   bool
	update    []ColumnRef
	updateAll bool
	set       []Assignment
}

// Upsert starts an upsert of row.
func (m *Model[M]) Upsert(row *M) *UpsertQuery[M] { return &UpsertQuery[M]{m: m, row: row} }

// OnConflict names the columns whose uniqueness defines a conflict: a unique
// index or the primary key. MySQL applies ON DUPLICATE KEY to every unique key
// and ignores the list except to read the row back.
func (q *UpsertQuery[M]) OnConflict(cols ...ColumnRef) *UpsertQuery[M] {
	q.target = append(q.target, cols...)
	return q
}

// DoNothing keeps the existing row untouched on conflict.
func (q *UpsertQuery[M]) DoNothing() *UpsertQuery[M] { q.nothing = true; return q }

// DoUpdate overwrites the given columns of the existing row with the values you
// tried to insert. With no arguments it overwrites every inserted column except
// the conflict columns and the primary key. Columns must be part of the INSERT,
// so a column left out because it holds its zero value and has a default cannot
// be listed.
func (q *UpsertQuery[M]) DoUpdate(cols ...ColumnRef) *UpsertQuery[M] {
	if len(cols) == 0 {
		q.updateAll = true
	}
	q.update = append(q.update, cols...)
	return q
}

// Set updates columns with arbitrary expressions on conflict, for example
// lathe.Incr(db.Counters.Hits, 1). It combines with DoUpdate; a column named in
// both takes the Set value.
func (q *UpsertQuery[M]) Set(assignments ...Assignment) *UpsertQuery[M] {
	q.set = append(q.set, assignments...)
	return q
}

func (q *UpsertQuery[M]) build() (*builder, insertPlan, []int, error) {
	m := q.m
	d := m.db.dialect
	updating := q.updateAll || len(q.update) > 0 || len(q.set) > 0
	switch {
	case q.nothing && updating:
		return nil, insertPlan{}, nil, errors.New("lathe: DoNothing cannot be combined with DoUpdate or Set")
	case !q.nothing && !updating:
		return nil, insertPlan{}, nil, errors.New("lathe: an upsert needs DoNothing() or DoUpdate(...)/Set(...)")
	}

	var target []int
	for _, c := range q.target {
		i, err := m.index(c)
		if err != nil {
			return nil, insertPlan{}, nil, err
		}
		target = append(target, i)
	}
	if updating && d.ConflictStyle() == OnConflict && len(target) == 0 {
		return nil, insertPlan{}, nil, fmt.Errorf("lathe: %s needs OnConflict(...) columns to update on conflict", d.Name())
	}

	b, p := m.insertBuilder(q.row)
	inserted := map[int]bool{}
	for _, ci := range p.cols {
		inserted[ci] = true
	}

	setCols := map[string]bool{}
	for _, a := range q.set {
		if a.col.TableName() != m.spec.Table {
			return nil, insertPlan{}, nil, fmt.Errorf("lathe: cannot set %s.%s in an upsert of %s", a.col.TableName(), a.col.ColumnName(), m.spec.Table)
		}
		setCols[a.col.ColumnName()] = true
	}
	var updCols []int
	if q.updateAll {
		for _, ci := range p.cols {
			if !m.spec.Fields[ci].PrimaryKey && !slices.Contains(target, ci) {
				updCols = append(updCols, ci)
			}
		}
		if len(updCols) == 0 && len(q.set) == 0 {
			return nil, insertPlan{}, nil, errors.New("lathe: DoUpdate() has no columns to update; every inserted column is a key")
		}
	}
	for _, c := range q.update {
		ci, err := m.index(c)
		if err != nil {
			return nil, insertPlan{}, nil, err
		}
		if !inserted[ci] {
			return nil, insertPlan{}, nil, fmt.Errorf("lathe: cannot update %s.%s from the inserted row: it is not part of the INSERT (it holds its zero value and has a database default)", m.spec.Table, c.ColumnName())
		}
		if !slices.Contains(updCols, ci) {
			updCols = append(updCols, ci)
		}
	}
	updCols = slices.DeleteFunc(updCols, func(ci int) bool { return setCols[m.spec.Fields[ci].Column] })

	incoming := func(col string) {
		if d.ConflictStyle() == OnDuplicateKey {
			b.write("VALUES(")
			b.ident(col)
			b.write(")")
		} else {
			b.write("EXCLUDED.")
			b.ident(col)
		}
	}
	writeSets := func() {
		first := true
		sep := func() {
			if !first {
				b.write(", ")
			}
			first = false
		}
		for _, ci := range updCols {
			sep()
			col := m.spec.Fields[ci].Column
			b.ident(col)
			b.write(" = ")
			incoming(col)
		}
		for _, a := range q.set {
			sep()
			b.ident(a.col.ColumnName())
			b.write(" = ")
			a.val.appendSQL(b)
		}
	}

	switch d.ConflictStyle() {
	case OnDuplicateKey:
		b.write(" ON DUPLICATE KEY UPDATE ")
		if q.nothing {
			pk := m.spec.Fields[m.spec.pk[0]].Column
			b.ident(pk)
			b.write(" = ")
			b.ident(pk)
		} else {
			writeSets()
		}
	default:
		b.write(" ON CONFLICT")
		if len(target) > 0 {
			b.write(" (" + m.columnList(target) + ")")
		}
		if q.nothing {
			b.write(" DO NOTHING")
		} else {
			b.write(" DO UPDATE SET ")
			writeSets()
		}
	}
	return b, p, target, b.err
}

// Build renders the statement without running it, for debugging and tests.
// Databases with RETURNING also get a RETURNING clause when it runs.
func (q *UpsertQuery[M]) Build() (string, []any, error) {
	b, _, _, err := q.build()
	if err != nil {
		return "", nil, err
	}
	return b.String(), b.args, nil
}

// Exec runs the upsert.
func (q *UpsertQuery[M]) Exec(ctx context.Context) error {
	m := q.m
	d := m.db.dialect
	b, p, target, err := q.build()
	if err != nil {
		return err
	}
	if len(target) == 0 && !d.SupportsReturning() {
		if err := m.checkReadBack(p); err != nil {
			return err
		}
	}

	if d.SupportsReturning() {
		all := m.allFields()
		b.write(" RETURNING " + m.columnList(all))
		rows, err := m.db.query(ctx, b.String(), b.args)
		if err != nil {
			return err
		}
		got, err := m.scanReturned(rows, q.row, all)
		if err != nil || got {
			return err
		}
		// DO NOTHING skipped the insert: read the row that is already there
		if len(target) == 0 {
			return nil
		}
		return m.refreshByColumns(ctx, q.row, target)
	}

	res, err := m.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	if len(target) > 0 {
		return m.refreshByColumns(ctx, q.row, target)
	}
	if p.auto >= 0 {
		if id, err := res.LastInsertId(); err == nil && id > 0 {
			setInt(m.spec.Fields[p.auto].Ptr(q.row), id)
		}
	}
	return nil
}

// scanReturned reads the first row of a RETURNING result into row and closes
// rows. It reports whether a row was present.
func (m *Model[M]) scanReturned(rows *sql.Rows, row *M, cols []int) (bool, error) {
	defer rows.Close()
	if !rows.Next() {
		return false, m.db.wrap(rows.Err())
	}
	dests := make([]any, len(cols))
	for i, ci := range cols {
		dests[i] = m.spec.Fields[ci].Ptr(row)
	}
	if err := rows.Scan(dests...); err != nil {
		return false, m.db.wrap(err)
	}
	return true, m.db.wrap(rows.Err())
}

// refreshByColumns reloads every column of row from the database row whose
// given columns equal row's values.
func (m *Model[M]) refreshByColumns(ctx context.Context, row *M, by []int) error {
	all := m.allFields()
	b := newBuilder(m.db.dialect)
	b.write("SELECT " + m.columnList(all) + " FROM ")
	b.ident(m.spec.Table)
	b.write(" WHERE ")
	for i, ci := range by {
		if i > 0 {
			b.write(" AND ")
		}
		b.ident(m.spec.Fields[ci].Column)
		b.write(" = ")
		b.arg(valueOf(m.spec.Fields[ci].Ptr(row)))
	}
	rows, err := m.db.query(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	got, err := m.scanReturned(rows, row, all)
	if err != nil {
		return err
	}
	if !got {
		return ErrNotFound
	}
	return nil
}

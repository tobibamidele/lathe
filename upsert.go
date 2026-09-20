package lathe

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// UpsertQuery inserts rows, or resolves conflicts with existing ones.
//
//	err := client.Users.Upsert(&u).
//		OnConflict(db.Users.Email).
//		DoUpdate(db.Users.Name, db.Users.Bio).
//		Exec(ctx)
//
//	err = client.Users.UpsertMany(users).OnConflict(db.Users.Email).DoUpdate().Exec(ctx)
//
// It becomes INSERT ... ON CONFLICT (email) DO UPDATE on PostgreSQL and SQLite
// and INSERT ... ON DUPLICATE KEY UPDATE on MySQL.
//
// After Exec every row reflects what is in the database: the inserted row, the
// updated row, or, with DoNothing, the row that was already there. Rows are
// matched to database rows by their OnConflict columns, so those must be set
// on every row; without OnConflict columns nothing can be read back except on
// PostgreSQL and SQLite (which return the affected rows).
//
// UpsertMany groups rows by the columns they insert, sends multi-row
// statements sized to the driver's parameter limit and runs everything in one
// transaction. Two rows with the same conflict key in one call are rejected:
// PostgreSQL refuses them and the other databases would apply them in an order
// that is easy to get wrong.
type UpsertQuery[M any] struct {
	m         *Model[M]
	rows      []*M
	target    []ColumnRef
	nothing   bool
	update    []ColumnRef
	updateAll bool
	set       []Assignment
}

// Upsert starts an upsert of one row.
func (m *Model[M]) Upsert(row *M) *UpsertQuery[M] {
	return &UpsertQuery[M]{m: m, rows: []*M{row}}
}

// UpsertMany starts an upsert of many rows. The slice's elements are updated in
// place with what the database holds afterwards.
func (m *Model[M]) UpsertMany(rows []M) *UpsertQuery[M] {
	ptrs := make([]*M, len(rows))
	for i := range rows {
		ptrs[i] = &rows[i]
	}
	return &UpsertQuery[M]{m: m, rows: ptrs}
}

// OnConflict names the columns whose uniqueness defines a conflict: a unique
// index or the primary key. MySQL applies ON DUPLICATE KEY to every unique key
// and uses the list only to read the rows back.
func (q *UpsertQuery[M]) OnConflict(cols ...ColumnRef) *UpsertQuery[M] {
	q.target = append(q.target, cols...)
	return q
}

// DoNothing keeps existing rows untouched on conflict.
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

// group is a set of rows that insert the same columns and can share statements.
type upsertGroup[M any] struct {
	cols     []int
	rows     []*M
	auto     int   // omitted auto increment column, or -1
	defaults []int // omitted columns with a database default
	update   []int // columns overwritten on conflict
}

type upsertPlan[M any] struct {
	target  []int
	groups  []*upsertGroup[M]
	setArgs int // bind parameters used by Set expressions in every statement
}

func (q *UpsertQuery[M]) prepare() (*upsertPlan[M], error) {
	m := q.m
	d := m.db.dialect
	updating := q.updateAll || len(q.update) > 0 || len(q.set) > 0
	switch {
	case q.nothing && updating:
		return nil, errors.New("lathe: DoNothing cannot be combined with DoUpdate or Set")
	case !q.nothing && !updating:
		return nil, errors.New("lathe: an upsert needs DoNothing() or DoUpdate(...)/Set(...)")
	}

	p := &upsertPlan[M]{}
	for _, c := range q.target {
		i, err := m.index(c)
		if err != nil {
			return nil, err
		}
		p.target = append(p.target, i)
	}
	if updating && d.ConflictStyle() == OnConflict && len(p.target) == 0 {
		return nil, fmt.Errorf("lathe: %s needs OnConflict(...) columns to update on conflict", d.Name())
	}

	scratch := newBuilder(d)
	setCols := map[string]bool{}
	for _, a := range q.set {
		if a.col.TableName() != m.spec.Table {
			return nil, fmt.Errorf("lathe: cannot set %s.%s in an upsert of %s", a.col.TableName(), a.col.ColumnName(), m.spec.Table)
		}
		setCols[a.col.ColumnName()] = true
		a.val.appendSQL(scratch)
	}
	if scratch.err != nil {
		return nil, scratch.err
	}
	p.setArgs = len(scratch.args)

	// rows with the same inserted columns share statements
	byKey := map[string]*upsertGroup[M]{}
	for _, row := range q.rows {
		plan := m.planInsert(row)
		key := fmt.Sprint(plan.cols)
		g, ok := byKey[key]
		if !ok {
			g = &upsertGroup[M]{cols: plan.cols, auto: plan.auto, defaults: plan.defaults}
			byKey[key] = g
			p.groups = append(p.groups, g)
		}
		g.rows = append(g.rows, row)
	}

	if len(p.target) > 0 && len(q.rows) > 1 {
		names := m.columnNames(p.target)
		seen := map[string]int{}
		for i, row := range q.rows {
			k, ok := keyOf(m.spec, row, names)
			if !ok {
				continue
			}
			if j, dup := seen[k]; dup {
				return nil, fmt.Errorf("lathe: rows %d and %d have the same conflict key (%s); an upsert cannot apply two rows to one key", j, i, joinNames(names))
			}
			seen[k] = i
		}
	}

	for _, g := range p.groups {
		inserted := map[int]bool{}
		for _, ci := range g.cols {
			inserted[ci] = true
		}
		if q.updateAll {
			for _, ci := range g.cols {
				if !m.spec.Fields[ci].PrimaryKey && !slices.Contains(p.target, ci) {
					g.update = append(g.update, ci)
				}
			}
			if len(g.update) == 0 && len(q.set) == 0 {
				return nil, errors.New("lathe: DoUpdate() has no columns to update; every inserted column is a key")
			}
		}
		for _, c := range q.update {
			ci, err := m.index(c)
			if err != nil {
				return nil, err
			}
			if !inserted[ci] {
				return nil, fmt.Errorf("lathe: cannot update %s.%s from the inserted row: it is not part of the INSERT (it holds its zero value and has a database default)", m.spec.Table, c.ColumnName())
			}
			if !slices.Contains(g.update, ci) {
				g.update = append(g.update, ci)
			}
		}
		g.update = slices.DeleteFunc(g.update, func(ci int) bool { return setCols[m.spec.Fields[ci].Column] })
	}
	return p, nil
}

func (m *Model[M]) columnNames(idx []int) []string {
	names := make([]string, len(idx))
	for i, fi := range idx {
		names[i] = m.spec.Fields[fi].Column
	}
	return names
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// stmt renders one INSERT with a conflict clause for rows of group g.
func (q *UpsertQuery[M]) stmt(p *upsertPlan[M], g *upsertGroup[M], rows []*M) *builder {
	m := q.m
	d := m.db.dialect
	b := newBuilder(d)

	if len(g.cols) == 0 {
		b.write(d.DefaultValues(d.Quote(m.spec.Table)))
	} else {
		b.write("INSERT INTO ")
		b.ident(m.spec.Table)
		b.write(" (" + m.columnList(g.cols) + ") VALUES ")
		for i, row := range rows {
			if i > 0 {
				b.write(", ")
			}
			b.write("(")
			for j, ci := range g.cols {
				if j > 0 {
					b.write(", ")
				}
				b.arg(valueOf(m.spec.Fields[ci].Ptr(row)))
			}
			b.write(")")
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
		for _, ci := range g.update {
			sep()
			col := m.spec.Fields[ci].Column
			b.ident(col)
			b.write(" = ")
			if d.ConflictStyle() == OnDuplicateKey {
				b.write("VALUES(")
				b.ident(col)
				b.write(")")
			} else {
				b.write("EXCLUDED.")
				b.ident(col)
			}
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
		if len(p.target) > 0 {
			b.write(" (" + m.columnList(p.target) + ")")
		}
		if q.nothing {
			b.write(" DO NOTHING")
		} else {
			b.write(" DO UPDATE SET ")
			writeSets()
		}
	}
	return b
}

// chunkSize is how many rows of g fit in one statement.
func (q *UpsertQuery[M]) chunkSize(p *upsertPlan[M], g *upsertGroup[M]) int {
	if len(g.cols) == 0 {
		return 1
	}
	n := (q.m.db.dialect.MaxParams() - p.setArgs) / len(g.cols)
	return max(n, 1)
}

// Build renders the first statement without running it, for debugging and
// tests. Databases with RETURNING also get a RETURNING clause when it runs.
func (q *UpsertQuery[M]) Build() (string, []any, error) {
	p, err := q.prepare()
	if err != nil {
		return "", nil, err
	}
	if len(p.groups) == 0 {
		return "", nil, errors.New("lathe: no rows to upsert")
	}
	g := p.groups[0]
	b := q.stmt(p, g, g.rows[:min(len(g.rows), q.chunkSize(p, g))])
	if b.err != nil {
		return "", nil, b.err
	}
	return b.String(), b.args, nil
}

// Exec runs the upsert. With more than one row it runs in a transaction.
func (q *UpsertQuery[M]) Exec(ctx context.Context) error {
	p, err := q.prepare()
	if err != nil {
		return err
	}
	if len(q.rows) == 0 {
		return nil
	}
	run := func(m *Model[M]) error {
		for _, g := range p.groups {
			per := q.chunkSize(p, g)
			for start := 0; start < len(g.rows); start += per {
				end := min(start+per, len(g.rows))
				if err := q.execChunk(ctx, m, p, g, g.rows[start:end]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(q.rows) == 1 {
		return run(q.m)
	}
	return q.m.db.Tx(ctx, func(tx *DB) error {
		return run(&Model[M]{db: tx, spec: q.m.spec})
	})
}

func (q *UpsertQuery[M]) execChunk(ctx context.Context, m *Model[M], p *upsertPlan[M], g *upsertGroup[M], chunk []*M) error {
	d := m.db.dialect
	b := q.stmt(p, g, chunk)
	if b.err != nil {
		return b.err
	}

	if d.SupportsReturning() {
		all := m.allFields()
		b.write(" RETURNING " + m.columnList(all))
		rows, err := m.db.query(ctx, b.String(), b.args)
		if err != nil {
			return err
		}
		returned, err := m.scanRows(rows, all)
		if err != nil {
			return err
		}
		return m.reconcile(ctx, p.target, chunk, returned)
	}

	if len(p.target) == 0 {
		if err := m.checkReadBack(insertPlan{defaults: g.defaults}); err != nil {
			return err
		}
	}
	res, err := m.db.exec(ctx, b.String(), b.args)
	if err != nil {
		return err
	}
	if len(p.target) > 0 {
		return m.reconcile(ctx, p.target, chunk, nil)
	}
	if g.auto >= 0 && len(chunk) == 1 {
		if id, err := res.LastInsertId(); err == nil && id > 0 {
			setInt(m.spec.Fields[g.auto].Ptr(chunk[0]), id)
		}
	}
	return nil
}

// reconcile copies the database's version of each row into the caller's rows.
// returned holds the rows a RETURNING clause produced (nil where there is none).
// Rows are matched by conflict key, because DO NOTHING skips rows and positions
// then no longer line up; whatever is not covered is loaded by key.
func (m *Model[M]) reconcile(ctx context.Context, target []int, chunk []*M, returned []M) error {
	if len(target) == 0 {
		// no key to match on: only a complete RETURNING result can be trusted
		if len(returned) == len(chunk) {
			for i := range chunk {
				m.copyRow(chunk[i], &returned[i])
			}
		}
		return nil
	}
	names := m.columnNames(target)

	byKey := make(map[string]int, len(returned))
	for i := range returned {
		if k, ok := keyOf(m.spec, &returned[i], names); ok {
			byKey[k] = i
		}
	}
	var missing []*M
	var missingKeys [][]any
	for _, row := range chunk {
		k, ok := keyOf(m.spec, row, names)
		if !ok {
			return fmt.Errorf("lathe: cannot read back a row of %s: a conflict column is NULL", m.spec.Table)
		}
		if i, found := byKey[k]; found {
			m.copyRow(row, &returned[i])
			continue
		}
		vals, _ := keyValues(m.spec, row, names)
		missing = append(missing, row)
		missingKeys = append(missingKeys, vals)
	}
	if len(missing) == 0 {
		return nil
	}

	fetched, err := fetchByKeys(ctx, m.db, m.spec, names, missingKeys, relOpts[M]{})
	if err != nil {
		return err
	}
	fetchedByKey := make(map[string]int, len(fetched))
	for i := range fetched {
		if k, ok := keyOf(m.spec, &fetched[i], names); ok {
			fetchedByKey[k] = i
		}
	}
	for _, row := range missing {
		k, _ := keyOf(m.spec, row, names)
		if i, found := fetchedByKey[k]; found {
			m.copyRow(row, &fetched[i])
			continue
		}
		// The database may compare the key differently than Go does (a
		// case-insensitive collation); let it decide for this one row.
		if err := m.refreshByColumns(ctx, row, target); err != nil {
			return fmt.Errorf("lathe: reading back a row of %s after the upsert: %w", m.spec.Table, err)
		}
	}
	return nil
}

// copyRow overwrites the mapped columns of dst with those of src. Relation
// fields, which are not part of the table spec, are left alone.
func (m *Model[M]) copyRow(dst, src *M) {
	for _, f := range m.spec.Fields {
		reflect.ValueOf(f.Ptr(dst)).Elem().Set(reflect.ValueOf(f.Ptr(src)).Elem())
	}
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
	got, err := m.scanRows(rows, all)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return ErrNotFound
	}
	m.copyRow(row, &got[0])
	return nil
}

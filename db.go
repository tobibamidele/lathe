package lathe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// QueryEvent describes one statement sent to the database.
type QueryEvent struct {
	SQL      string
	Args     []any
	Duration time.Duration
	Err      error
}

// Option configures a DB.
type Option func(*config)

type config struct {
	logger func(context.Context, QueryEvent)
}

// WithLogger installs a hook that is called after every statement. It is the
// place to plug in logging, tracing or metrics.
func WithLogger(fn func(context.Context, QueryEvent)) Option {
	return func(c *config) { c.logger = fn }
}

// queryer is what *sql.DB and *sql.Tx have in common.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DB is a database handle bound to a dialect. Inside [DB.Tx] callbacks it is
// bound to the transaction instead. It is safe for concurrent use, except for
// the transaction-bound variant which follows *sql.Tx rules.
type DB struct {
	q       queryer
	sqlDB   *sql.DB
	tx      *sql.Tx
	dialect Dialect
	cfg     *config
	spSeq   *atomic.Int64 // savepoint counter shared by a transaction
}

// New wraps an open *sql.DB. Most programs call postgres.Open, mysql.Open or
// sqlite.Open instead.
func New(db *sql.DB, d Dialect, opts ...Option) *DB {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	return &DB{q: db, sqlDB: db, dialect: d, cfg: cfg}
}

// Dialect returns the dialect in use.
func (d *DB) Dialect() Dialect { return d.dialect }

// SQL returns the underlying *sql.DB, or nil for a transaction-bound DB.
func (d *DB) SQL() *sql.DB { return d.sqlDB }

// InTx reports whether d is bound to a transaction.
func (d *DB) InTx() bool { return d.tx != nil }

// Close closes the underlying *sql.DB.
func (d *DB) Close() error {
	if d.sqlDB == nil {
		return errors.New("lathe: cannot close a transaction-bound DB")
	}
	return d.sqlDB.Close()
}

// Ping verifies the connection.
func (d *DB) Ping(ctx context.Context) error {
	if d.sqlDB == nil {
		return errors.New("lathe: cannot ping a transaction-bound DB")
	}
	return d.sqlDB.PingContext(ctx)
}

// Exec runs a statement written in raw SQL. Use ? for bind parameters on every
// dialect (write ?? for a literal question mark).
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.exec(ctx, rebind(d.dialect, query), args)
}

// Query runs a raw SQL query. The caller must close the rows. See [Raw] for a
// typed alternative.
func (d *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.query(ctx, rebind(d.dialect, query), args)
}

func (d *DB) exec(ctx context.Context, query string, args []any) (sql.Result, error) {
	start := time.Now()
	res, err := d.q.ExecContext(ctx, query, args...)
	d.log(ctx, query, args, start, err)
	return res, d.wrap(err)
}

func (d *DB) query(ctx context.Context, query string, args []any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := d.q.QueryContext(ctx, query, args...)
	d.log(ctx, query, args, start, err)
	return rows, d.wrap(err)
}

func (d *DB) log(ctx context.Context, query string, args []any, start time.Time, err error) {
	if d.cfg.logger != nil {
		d.cfg.logger(ctx, QueryEvent{SQL: query, Args: args, Duration: time.Since(start), Err: err})
	}
}

func (d *DB) wrap(err error) error {
	if err == nil {
		return nil
	}
	if kind := d.dialect.ClassifyError(err); kind != nil {
		return &constraintError{kind: kind, err: err}
	}
	return err
}

// Tx runs fn in a transaction. The transaction commits when fn returns nil and
// rolls back when it returns an error or panics. Use the *DB passed to fn (or
// the client built from it) for every statement that belongs to it.
//
// Calling Tx on a transaction-bound DB creates a savepoint, so nested calls
// compose: an inner error rolls back only the inner work.
func (d *DB) Tx(ctx context.Context, fn func(tx *DB) error) error {
	return d.TxOptions(ctx, nil, fn)
}

// TxOptions is like [DB.Tx] with explicit isolation and read-only settings.
// The options are ignored for nested (savepoint) calls.
func (d *DB) TxOptions(ctx context.Context, opts *sql.TxOptions, fn func(tx *DB) error) (err error) {
	if d.tx != nil {
		return d.savepoint(ctx, fn)
	}
	if d.sqlDB == nil {
		return errors.New("lathe: no database to begin a transaction on")
	}
	sqlTx, err := d.sqlDB.BeginTx(ctx, opts)
	if err != nil {
		return d.wrap(err)
	}
	txDB := &DB{q: sqlTx, tx: sqlTx, dialect: d.dialect, cfg: d.cfg, spSeq: new(atomic.Int64)}
	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
	}()
	if err := fn(txDB); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("lathe: rollback: %w", rbErr))
		}
		return err
	}
	return d.wrap(sqlTx.Commit())
}

func (d *DB) savepoint(ctx context.Context, fn func(tx *DB) error) (err error) {
	name := d.dialect.Quote(fmt.Sprintf("lathe_sp_%d", d.spSeq.Add(1)))
	if _, err := d.exec(ctx, "SAVEPOINT "+name, nil); err != nil {
		return err
	}
	rollback := func() error {
		if _, err := d.exec(ctx, "ROLLBACK TO SAVEPOINT "+name, nil); err != nil {
			return err
		}
		_, err := d.exec(ctx, "RELEASE SAVEPOINT "+name, nil)
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = rollback()
			panic(p)
		}
	}()
	if err := fn(d); err != nil {
		if rbErr := rollback(); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}
	_, err = d.exec(ctx, "RELEASE SAVEPOINT "+name, nil)
	return err
}

package lathe

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	timeType    = reflect.TypeOf(time.Time{})
	fieldCache  sync.Map // reflect.Type -> map[string][]int
)

// scanAll reads every row into a T and closes rows.
func scanAll[T any](rows *sql.Rows, wrap func(error) error) ([]T, error) {
	defer rows.Close()
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var out []T
	if !isStructTarget(t) {
		if len(cols) != 1 {
			return nil, fmt.Errorf("lathe: scanning into %s needs exactly one column, the query returned %d", t, len(cols))
		}
		for rows.Next() {
			var item T
			if err := rows.Scan(&item); err != nil {
				return nil, wrap(err)
			}
			out = append(out, item)
		}
		return out, wrap(rows.Err())
	}

	fields := structFields(t)
	paths := make([][]int, len(cols))
	for i, c := range cols {
		p, ok := fields[normalize(c)]
		if !ok {
			return nil, fmt.Errorf("lathe: result column %q has no matching field in %s (add a `db:%q` tag or select fewer columns)", c, t, c)
		}
		paths[i] = p
	}
	dests := make([]any, len(cols))
	for rows.Next() {
		var item T
		v := reflect.ValueOf(&item).Elem()
		for i, p := range paths {
			dests[i] = v.FieldByIndex(p).Addr().Interface()
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, wrap(err)
		}
		out = append(out, item)
	}
	return out, wrap(rows.Err())
}

func isStructTarget(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t == timeType {
		return false
	}
	return !reflect.PointerTo(t).Implements(scannerType)
}

// structFields maps normalised column names to field index paths.
func structFields(t reflect.Type) map[string][]int {
	if m, ok := fieldCache.Load(t); ok {
		return m.(map[string][]int)
	}
	m := map[string][]int{}
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || (f.Anonymous && f.Type.Kind() == reflect.Struct) {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("db"); ok {
			tag = strings.Split(tag, ",")[0]
			if tag == "-" {
				continue
			}
			if tag != "" {
				name = tag
			}
		}
		key := normalize(name)
		if _, dup := m[key]; !dup {
			m[key] = f.Index
		}
	}
	fieldCache.Store(t, m)
	return m
}

func normalize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

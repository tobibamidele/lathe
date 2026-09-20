// Package driverpick finds the database/sql driver a program has linked in.
package driverpick

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// Pick returns the first candidate driver name that is registered with
// database/sql. hint names a package to import when none is.
func Pick(pkg string, hint string, candidates ...string) (string, error) {
	registered := sql.Drivers()
	for _, c := range candidates {
		if slices.Contains(registered, c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("lathe/%s: no database/sql driver is registered (looked for %s); import one, for example: %s",
		pkg, strings.Join(candidates, ", "), hint)
}

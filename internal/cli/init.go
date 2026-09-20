package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tobibamidele/lathe/internal/loader"
	"github.com/tobibamidele/lathe/schema"
)

var schemaTemplate = template.Must(template.New("schema").Parse(`// Package {{.Package}} declares the database schema. lathe reads it to generate
// code ("lathe generate") and migrations ("lathe migrate diff").
package {{.Package}}

import s "github.com/tobibamidele/lathe/schema"

// Schema is the entry point lathe looks for.
var Schema = s.New(s.Config{
	Dialect:    s.{{.Dialect}},
	Output:     "db",         // the generated package is written here
	Migrations: "migrations", // SQL migrations and the schema snapshot live here
},
	s.Table("users",
		s.BigInt("id").PrimaryKey().AutoIncrement(),
		s.VarChar("email", 255).Unique(),
		s.VarChar("name", 120),
		s.Text("bio").Nullable(),
		s.Timestamp("created_at").DefaultNow(),
	),
)
`))

func (a *app) initCmd(args []string) error {
	fs := a.flags("init")
	dialect := fs.String("dialect", "postgres", "database dialect: postgres, mysql or sqlite")
	dir := fs.String("schema", "schema", "directory for the schema package")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	d := schema.Dialect(*dialect)
	if !d.Valid() {
		return fmt.Errorf("unsupported dialect %q (want postgres, mysql or sqlite)", *dialect)
	}
	proj, err := loader.Find(".")
	if err != nil {
		return err
	}

	path := filepath.Join(*dir, "schema.go")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	name := "Postgres"
	switch d {
	case schema.MySQL:
		name = "MySQL"
	case schema.SQLite:
		name = "SQLite"
	}
	pkg := filepath.Base(filepath.Clean(*dir))
	if pkg == "." || pkg == string(filepath.Separator) {
		return errors.New("choose a schema directory with --schema")
	}
	var buf bytes.Buffer
	if err := schemaTemplate.Execute(&buf, struct{ Package, Dialect string }{pkg, name}); err != nil {
		return err
	}
	if _, err := writeIfChanged(path, buf.Bytes()); err != nil {
		return err
	}
	if err := ensureIgnored(proj.Root, "_lathe/"); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "created %s\n\nNext steps:\n", path)
	fmt.Fprintln(a.out, "  1. go get github.com/tobibamidele/lathe@latest")
	fmt.Fprintf(a.out, "  2. edit %s\n", path)
	fmt.Fprintln(a.out, "  3. lathe generate")
	fmt.Fprintln(a.out, "  4. lathe migrate diff init")
	fmt.Fprintln(a.out, "  5. lathe migrate up --url \"$DATABASE_URL\"")
	return nil
}

// ensureIgnored appends pattern to the module's .gitignore if it is missing.
func ensureIgnored(root, pattern string) error {
	path := filepath.Join(root, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	prefix := ""
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(prefix + "# lathe scratch space\n" + pattern + "\n")
	return err
}

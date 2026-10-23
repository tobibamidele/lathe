package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tobibamidele/lathe/internal/importer"
	"github.com/tobibamidele/lathe/internal/loader"
	"github.com/tobibamidele/lathe/schema"
)

func (a *app) importCmd(args []string) error {
	fs := a.flags("import")
	from := fs.String("from", "", "directory of SQL migration files")
	schemaDir := fs.String("schema", "schema", "directory for the generated schema package")
	dialect := fs.String("dialect", "postgres", "database dialect: postgres, mysql or sqlite")
	output := fs.String("output", "db", "generated Go package directory")
	migrations := fs.String("migrations", "migrations", "migration directory the schema should point at")
	strict := fs.Bool("strict", false, "fail when a migration uses a construct lathe cannot represent")
	dry := fs.Bool("dry-run", false, "print the generated schema instead of writing it")
	force := fs.Bool("force", false, "overwrite an existing schema/schema.go")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *from == "" && len(pos) == 1 {
		*from = pos[0]
		pos = nil
	}
	if *from == "" {
		return errors.New("usage: lathe import --from DIR [--schema DIR] [--dialect postgres|mysql|sqlite]")
	}
	if len(pos) > 0 {
		return errors.New("import takes one source directory; use --from DIR")
	}
	d := schema.Dialect(*dialect)
	if !d.Valid() {
		return fmt.Errorf("unsupported dialect %q (want postgres, mysql or sqlite)", *dialect)
	}

	proj, err := loader.Find(".")
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(*from)
	if err != nil {
		return err
	}
	res, err := importer.ImportDir(dir, d)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(a.err, "warning: "+w)
	}
	if *strict && len(res.Warnings) > 0 {
		return fmt.Errorf("import found %d construct(s) lathe cannot represent; fix them or drop --strict", len(res.Warnings))
	}

	pkg, err := packageName(*schemaDir)
	if err != nil {
		return err
	}
	// Keep the generated header portable when --from is inside the module.
	source := filepath.ToSlash(*from)
	if rel, err := filepath.Rel(proj.Root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		source = filepath.ToSlash(rel)
	}
	src, err := importer.Render(res.Snapshot, importer.RenderConfig{
		Package:    pkg,
		Output:     *output,
		Migrations: *migrations,
		Source:     source,
	})
	if err != nil {
		return err
	}
	if *dry {
		fmt.Fprintf(a.out, "%s", src)
		return nil
	}

	path := filepath.Join(*schemaDir, "schema.go")
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
	}
	if _, err := writeIfChanged(path, src); err != nil {
		return err
	}
	if err := ensureIgnored(proj.Root, "_lathe/"); err != nil {
		return err
	}

	columns := 0
	for _, t := range res.Snapshot.Tables {
		columns += len(t.Columns)
	}
	absPath, _ := filepath.Abs(path)
	rel, _ := filepath.Rel(proj.Root, absPath)
	fmt.Fprintf(a.out, "imported %d migration file(s) into %s\n", len(res.Files), filepath.ToSlash(rel))
	fmt.Fprintf(a.out, "%d table(s), %d column(s)\n", len(res.Snapshot.Tables), columns)
	if len(res.Warnings) > 0 {
		fmt.Fprintf(a.err, "%d warning(s); review the generated schema before using it\n", len(res.Warnings))
	}
	fmt.Fprintln(a.out, "\nNext steps:")
	fmt.Fprintln(a.out, "  1. review "+filepath.ToSlash(rel))
	fmt.Fprintln(a.out, "  2. lathe generate")
	fmt.Fprintln(a.out, "  3. lathe migrate diff init   # optional: capture a baseline")
	return nil
}

// packageName turns a schema directory into a valid Go package identifier.
func packageName(dir string) (string, error) {
	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", errors.New("choose a schema directory with --schema")
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		return "", fmt.Errorf("cannot derive a package name from %q", dir)
	}
	if r := rune(name[0]); unicode.IsDigit(r) {
		name = "_" + name
	}
	return name, nil
}

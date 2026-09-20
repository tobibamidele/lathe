package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tobibamidele/lathe/internal/gen"
)

func (a *app) generateCmd(args []string) error {
	fs := a.flags("generate")
	schemaDir := fs.String("schema", "schema", "schema package directory")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	proj, exp, err := a.load(*schemaDir)
	if err != nil {
		return err
	}
	files, err := gen.Generate(exp)
	if err != nil {
		return err
	}

	outDir := proj.Abs(exp.Config.Output)
	keep := map[string]bool{}
	var changed []string
	all := ""
	for _, f := range files {
		keep[f.Name] = true
		all += string(f.Content)
		wrote, err := writeIfChanged(filepath.Join(outDir, f.Name), f.Content)
		if err != nil {
			return err
		}
		if wrote {
			changed = append(changed, f.Name)
		}
	}

	// remove generated files of tables that no longer exist
	var removed []string
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".gen.go") || keep[e.Name()] {
				continue
			}
			p := filepath.Join(outDir, e.Name())
			if body, err := os.ReadFile(p); err == nil && strings.HasPrefix(string(body), gen.Header) {
				if err := os.Remove(p); err != nil {
					return err
				}
				removed = append(removed, e.Name())
			}
		}
	}

	rel, _ := filepath.Rel(proj.Root, outDir)
	sort.Strings(changed)
	switch {
	case len(changed) == 0 && len(removed) == 0:
		fmt.Fprintf(a.out, "%s is up to date (%d files)\n", rel, len(files))
	default:
		for _, n := range changed {
			fmt.Fprintf(a.out, "wrote    %s\n", filepath.Join(rel, n))
		}
		for _, n := range removed {
			fmt.Fprintf(a.out, "removed  %s\n", filepath.Join(rel, n))
		}
	}

	var missing []string
	for _, dep := range []string{"github.com/tobibamidele/lathe", "github.com/shopspring/decimal", "github.com/google/uuid"} {
		if strings.Contains(all, `"`+dep+`"`) && !strings.Contains(proj.GoMod, dep) {
			missing = append(missing, dep)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(a.out, "\nthe generated code needs modules your go.mod does not list yet:\n  go get %s\n", strings.Join(missing, " "))
	}
	return nil
}

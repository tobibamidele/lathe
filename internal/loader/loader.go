// Package loader executes a user's schema package.
//
// A schema is Go code, so the only faithful way to read it is to compile and
// run it. The loader writes a tiny throwaway main package inside the user's
// module (so the user's go.mod resolves every import), runs it with `go run`
// and reads the result from its output.
package loader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tobibamidele/lathe/schema"
)

// Project is the Go module lathe operates on.
type Project struct {
	Root       string // directory of go.mod
	ModulePath string
	GoMod      string // contents of go.mod
}

// Find locates the module that contains dir.
func Find(dir string) (*Project, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running `go env GOMOD` (is Go installed and on PATH?): %w\n%s", err, stderr.String())
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull || gomod == "NUL" {
		return nil, errors.New("no go.mod found; run lathe inside a Go module (go mod init <path>)")
	}
	body, err := os.ReadFile(gomod)
	if err != nil {
		return nil, err
	}
	var modulePath string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`)
			break
		}
	}
	if modulePath == "" {
		return nil, fmt.Errorf("%s has no module line", gomod)
	}
	return &Project{Root: filepath.Dir(gomod), ModulePath: modulePath, GoMod: string(body)}, nil
}

// ImportPath returns the import path of a directory inside the module.
func (p *Project) ImportPath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p.Root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the module rooted at %s", dir, p.Root)
	}
	if rel == "." {
		return p.ModulePath, nil
	}
	return p.ModulePath + "/" + filepath.ToSlash(rel), nil
}

// Abs resolves a path that is relative to the module root.
func (p *Project) Abs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(p.Root, rel)
}

// Run compiles and runs a main package with the given source inside the
// module. Environment variables are added to the current environment.
func (p *Project) Run(source string, env []string, stdout, stderr io.Writer) error {
	base := filepath.Join(p.Root, "_lathe")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(base, fmt.Sprintf("run-%04x-", rand.Intn(1<<16)))
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(dir)
		_ = os.Remove(base) // only succeeds when empty
	}()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(p.Root, dir)
	cmd := exec.Command("go", "run", "./"+filepath.ToSlash(rel))
	cmd.Dir = p.Root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

var exportSrc = template.Must(template.New("export").Parse(`package main

import (
	"encoding/json"
	"fmt"
	"os"

	schemapkg "{{.Import}}"
)

func main() {
	exp, err := schemapkg.Schema.Export()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if err := json.NewEncoder(os.Stdout).Encode(exp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`))

// LoadExport runs the schema package in schemaDir and returns its validated
// export. The package must declare `var Schema = schema.New(...)`.
func (p *Project) LoadExport(schemaDir string) (*schema.Export, error) {
	imp, err := p.ImportPath(schemaDir)
	if err != nil {
		return nil, err
	}
	var src bytes.Buffer
	if err := exportSrc.Execute(&src, struct{ Import string }{imp}); err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	if err := p.Run(src.String(), nil, &stdout, &stderr); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			// the schema itself is invalid; its message is what matters
			return nil, fmt.Errorf("invalid schema:\n%s", indent(cleanRunOutput(stderr.String())))
		}
		return nil, fmt.Errorf("could not load the schema package %s:\n%s%s", imp, indent(cleanRunOutput(stderr.String())), hint(stderr.String()))
	}
	var exp schema.Export
	if err := json.Unmarshal(stdout.Bytes(), &exp); err != nil {
		return nil, fmt.Errorf("unexpected output while loading the schema: %w\n%s", err, stdout.String())
	}
	return &exp, nil
}

// cleanRunOutput drops the "exit status" line go run appends.
func cleanRunOutput(s string) string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.HasPrefix(l, "exit status ") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

func hint(stderr string) string {
	switch {
	case strings.Contains(stderr, "no required module provides package github.com/tobibamidele/lathe"),
		strings.Contains(stderr, "no required module provides package github.com/tobibamidele/lathe/"):
		return "\n\nhint: add lathe to your module first:\n  go get github.com/tobibamidele/lathe@latest"
	case strings.Contains(stderr, "undefined: schemapkg.Schema"):
		return "\n\nhint: the schema package must declare an exported variable:\n  var Schema = schema.New(schema.Config{...}, ...tables)"
	}
	return ""
}

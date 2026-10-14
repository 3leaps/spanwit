package reportmodel_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const module = "github.com/3leaps/spanwit/"

// goList returns the import paths printed by `go list` for args.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// The report model is a leaf: the loaders and types a read-only viewer uses
// must not reach process spawn, HTTP, a remote schema loader, or any
// collector or delete-path package.
func TestReportModelDependenciesStayLeaf(t *testing.T) {
	banned := map[string]bool{
		"os/exec":                               true,
		"net/http":                              true,
		module + "internal/engine":              true,
		module + "internal/inventory":           true,
		module + "internal/capacity":            true,
		module + "internal/space":               true,
		module + "internal/config":              true,
		module + "internal/cmd":                 true,
		module + "internal/observe":             true,
		module + "internal/filepolicy":          true,
		module + "internal/catalog":             true,
		module + "internal/corpus":              true,
		module + "internal/runtime":             true,
		module + "internal/coverageattestation": true,
	}
	for _, dep := range goList(t, "-deps", "./internal/reportmodel") {
		if banned[dep] {
			t.Errorf("internal/reportmodel depends on %s", dep)
		}
		// Importing a schema httploader registers remote $ref fetching for
		// every validator in the process.
		if strings.HasSuffix(dep, "/httploader") {
			t.Errorf("internal/reportmodel depends on %s", dep)
		}
	}
}

// planspec is shared with the delete engine: its direct imports are a closed
// list, so neither a spanwit package nor a third-party or network package can
// enter it.
func TestPlanSpecImportsOnlyStrings(t *testing.T) {
	for _, imp := range goList(t, "-f", "{{join .Imports \" \"}}", "./internal/planspec") {
		if imp != "strings" {
			t.Errorf("internal/planspec imports %s", imp)
		}
	}
}

// The delete path never depends on the report model, so the leaf cannot grow
// back into it.
func TestDeletePathDoesNotImportReportModel(t *testing.T) {
	for _, pkg := range []string{"./internal/engine", "./internal/config"} {
		for _, dep := range goList(t, "-deps", pkg) {
			if dep == module+"internal/reportmodel" {
				t.Errorf("%s depends on internal/reportmodel", pkg)
			}
		}
	}
}

func TestEnvelopeStaysLeaf(t *testing.T) {
	for _, dep := range goList(t, "-deps", "./internal/envelope") {
		if strings.HasPrefix(dep, module) && dep != module+"internal/envelope" {
			t.Errorf("internal/envelope depends on %s", dep)
		}
	}
}

// bannedSymbol reports whether a resolved package-level object is one the
// report model sources may not reference.
func bannedSymbol(pkg, name string) bool {
	switch pkg {
	case "os/exec":
		return true
	case "net":
		return strings.HasPrefix(name, "Dial") || strings.HasPrefix(name, "Listen")
	case "os":
		return strings.HasPrefix(name, "Remove")
	}
	return false
}

// Symbol check on the report model's own sources, resolved through the type
// checker rather than text search: no process spawn, socket or delete call.
// Every resolved object counts, including struct fields and methods (a
// net.Resolver's Dial field, an os.File method), not only package-level names.
func TestReportModelSourcesReferenceNoSpawnSocketOrDelete(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "reportmodel")
	fset := token.NewFileSet()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no report model sources found")
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check(module+"internal/reportmodel", fset, files, info); err != nil {
		t.Fatalf("type-check report model: %v", err)
	}
	checked := 0
	for id, obj := range info.Uses {
		if obj.Pkg() == nil {
			continue
		}
		checked++
		if bannedSymbol(obj.Pkg().Path(), obj.Name()) {
			t.Errorf("%s: reference to %s.%s", fset.Position(id.Pos()), obj.Pkg().Path(), obj.Name())
		}
	}
	if checked == 0 {
		t.Fatal("symbol check resolved no references")
	}
}

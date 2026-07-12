package errcat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const errcatImportPath = "github.com/fprl/ship/internal/errcat"

func TestCatalogueCompleteness(t *testing.T) {
	root := repoRoot(t)
	constants := catalogueConstants(t, filepath.Join(root, "internal", "errcat", "catalogue.go"))
	catalogueCodes := map[Code]bool{}
	for _, entry := range Catalogue() {
		catalogueCodes[entry.Code] = true
	}
	for name, code := range constants {
		if !catalogueCodes[code] {
			t.Fatalf("%s = %q is not in the catalogue", name, code)
		}
	}
	for code := range catalogueCodes {
		found := false
		for _, constantCode := range constants {
			if constantCode == code {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("catalogue code %q has no Code* constant", code)
		}
	}

	used, scanned := errcatCodeUses(t, root, constants)
	for _, rel := range []string{"cmd/hostinstall/install.go", "cmd/hostinstall/helper_binary.go"} {
		if !scanned[rel] {
			t.Fatalf("%s was not scanned for errcat code uses", rel)
		}
	}
	for code := range catalogueCodes {
		if !used[code] {
			t.Fatalf("catalogue code %q is not constructed or referenced by production code", code)
		}
	}
	for code := range used {
		if !catalogueCodes[code] {
			t.Fatalf("production code references uncatalogued code %q", code)
		}
	}
}

func TestShareOnProductionRender(t *testing.T) {
	err := New(CodeShareOnProduction, Fields{"branch": `"main"`})
	if err.Message() != "share command refused on Production" || err.Cause() != "branch \"main\" maps to Production; share links are for Preview branches only" || err.Remediation() != "git checkout <preview-branch>" {
		t.Fatalf("share_on_production render = %+v", err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func catalogueConstants(t *testing.T, path string) map[string]Code {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Code{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			for i, name := range valueSpec.Names {
				if !strings.HasPrefix(name.Name, "Code") || i >= len(valueSpec.Values) {
					continue
				}
				lit, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out[name.Name] = Code(value)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no Code* constants found")
	}
	return out
}

func errcatCodeUses(t *testing.T, root string, constants map[string]Code) (map[Code]bool, map[string]bool) {
	t.Helper()
	used := map[Code]bool{}
	scanned := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch filepath.Base(path) {
			case ".git", "vendor":
				return filepath.SkipDir
			case "errcat":
				if filepath.Base(filepath.Dir(path)) == "internal" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := errcatAliases(file)
		if len(aliases) == 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		scanned[filepath.ToSlash(rel)] = true
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				ident, ok := n.X.(*ast.Ident)
				if !ok || !aliases[ident.Name] || n.Sel.Name == "Code" || !strings.HasPrefix(n.Sel.Name, "Code") {
					return true
				}
				code, ok := constants[n.Sel.Name]
				if !ok {
					t.Fatalf("%s references unknown errcat constant %s", path, n.Sel.Name)
				}
				used[code] = true
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "New" {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || !aliases[ident.Name] || len(n.Args) == 0 {
					return true
				}
				first, ok := n.Args[0].(*ast.SelectorExpr)
				if !ok {
					t.Fatalf("%s calls errcat.New without a Code* selector", path)
				}
				firstIdent, ok := first.X.(*ast.Ident)
				if !ok || !aliases[firstIdent.Name] || !strings.HasPrefix(first.Sel.Name, "Code") {
					t.Fatalf("%s calls errcat.New without an errcat.Code* selector", path)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return used, scanned
}

func errcatAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcatImportPath {
			continue
		}
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
			aliases[imp.Name.Name] = true
			continue
		}
		aliases["errcat"] = true
	}
	return aliases
}

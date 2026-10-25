package settings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Settings S1 §3.1-§3.2: product code reaches the environment only through
// internal/envread, and the fleet file's directory only through
// internal/localstore. These scans fail on any other reference, called or
// not, however it is spelled; the fixtures under testdata prove each form
// fails.

// rawEnv are the references the environment scan refuses, by import path.
var rawEnv = map[string]map[string]bool{
	"os":                       {"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true, "Setenv": true, "Unsetenv": true, "Clearenv": true},
	"syscall":                  {"Getenv": true, "Environ": true, "Setenv": true, "Unsetenv": true, "Clearenv": true},
	"golang.org/x/sys/unix":    {"Getenv": true, "Environ": true, "Setenv": true, "Unsetenv": true, "Clearenv": true},
	"golang.org/x/sys/windows": {"Getenv": true, "Environ": true, "Setenv": true, "Unsetenv": true, "Clearenv": true},
}

// fleetPathNames are how product code names the fleet file's path; a
// filepath.Dir or path.Dir of an expression naming one is refused.
var fleetPathNames = []string{"configpath", "fleetpath", "fleetfile", "cfgpath"}

// storeNames are the stores localstore declares: a literal naming one is
// refused elsewhere, so no path to one is built by hand.
var storeNames = map[string]bool{"window.json": true, "claude-builds.json": true, "git-cache": true}

// productFiles are the files the scans cover: internal/, cmd/klyx's own
// directory and cmd/klyxctl, without tests or testdata.
func productFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	var out []string
	add := func(dir string, recurse bool) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || (!recurse && p != filepath.Join(root, dir)) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("internal", true)
	add(filepath.Join("cmd", "klyx"), false)
	add(filepath.Join("cmd", "klyxctl"), true)
	sort.Strings(out)
	if len(out) < 100 {
		t.Fatalf("only %d product files found", len(out))
	}
	return out
}

func parse(t *testing.T, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// imports maps each import's local name to its path; a dot import is "."
// mapped to its path list.
func imports(f *ast.File) (named map[string]string, dot []string) {
	named = map[string]string{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		switch name {
		case ".":
			dot = append(dot, p)
		case "_":
		default:
			named[name] = p
		}
	}
	return named, dot
}

// envRefs are a file's raw environment references.
func envRefs(f *ast.File) []string {
	named, dot := imports(f)
	var out []string
	for _, p := range dot {
		if rawEnv[p] != nil {
			out = append(out, "a dot import of "+p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok {
			if banned := rawEnv[named[x.Name]]; banned[sel.Sel.Name] {
				out = append(out, named[x.Name]+"."+sel.Sel.Name)
			}
		}
		return true
	})
	return out
}

// dirRefs are a file's directory-of-the-fleet-path references and store
// literals.
func dirRefs(f *ast.File) []string {
	named, _ := imports(f)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if v, err := strconv.Unquote(n.Value); err == nil && storeNames[v] {
					out = append(out, "the store name "+n.Value)
				}
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Dir" || len(n.Args) != 1 {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || (named[x.Name] != "path/filepath" && named[x.Name] != "path") {
				return true
			}
			if namesFleetPath(n.Args[0]) {
				out = append(out, named[x.Name]+".Dir of the fleet path")
			}
		}
		return true
	})
	return out
}

func namesFleetPath(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		var name string
		switch n := n.(type) {
		case *ast.Ident:
			name = n.Name
		case *ast.SelectorExpr:
			name = n.Sel.Name
		}
		for _, f := range fleetPathNames {
			if strings.Contains(strings.ToLower(name), f) {
				found = true
			}
		}
		return !found
	})
	return found
}

func TestProductCodeReadsTheEnvironmentOnlyThroughEnvread(t *testing.T) {
	facade := filepath.Join("..", "..", "internal", "envread") + string(filepath.Separator)
	for _, p := range productFiles(t) {
		if strings.HasPrefix(p, facade) {
			continue
		}
		for _, r := range envRefs(parse(t, p)) {
			t.Errorf("%s: %s outside internal/envread", p, r)
		}
	}
}

func TestProductCodeFindsStoresOnlyThroughLocalstore(t *testing.T) {
	facade := filepath.Join("..", "..", "internal", "localstore") + string(filepath.Separator)
	for _, p := range productFiles(t) {
		if strings.HasPrefix(p, facade) {
			continue
		}
		for _, r := range dirRefs(parse(t, p)) {
			t.Errorf("%s: %s outside internal/localstore", p, r)
		}
	}
}

// Every fixture but the clean one fails its scan.
func TestTheScansRefuseEveryFixture(t *testing.T) {
	for dir, scan := range map[string]func(*ast.File) []string{"envscan": envRefs, "dirscan": dirRefs} {
		entries, err := os.ReadDir(filepath.Join("testdata", dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got := scan(parse(t, filepath.Join("testdata", dir, e.Name())))
			if e.Name() == "clean.go" {
				if len(got) != 0 {
					t.Errorf("%s/clean.go: %v", dir, got)
				}
				continue
			}
			if len(got) == 0 {
				t.Errorf("%s/%s passed the scan", dir, e.Name())
			}
		}
		if len(entries) < 4 {
			t.Fatalf("%s: %d fixtures", dir, len(entries))
		}
	}
}

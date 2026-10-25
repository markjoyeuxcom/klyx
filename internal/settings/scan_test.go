package settings

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// The native-store boundary does not depend on how a path is named (Codex's
// S1 implementation review, R4): every derivation of a parent directory in
// product code - a Dir or Split call, a ".." path segment - and every
// reference to the fleet file's variable sits on this reviewed list with
// its reason. A new one fails until it is reviewed: if it is the fleet
// file's directory, the store belongs in localstore.
var reviewedSites = map[string]string{
	"dir internal/ai/claudecli.go claudeCLIProvider.callDir":                   "walks up from the Claude Code executable to its install root",
	"dir internal/ai/claudecli_builds.go claudeBuildStore.replaceLocked":       "the builds file's own directory, for its temp file; the file is localstore's ClaudeBuilds",
	"dir internal/config/append.go AppendClusters":                             "the fleet writers' resolved target (slice 0's write policy), not a store root",
	"dir internal/config/append.go fleetTemp":                                  "the fleet writers' resolved target, for the temp file beside it",
	"dir internal/evalrun/preflight.go Preflight.Run":                          "the eval harness checks the lab's own fleet file lies in the lab",
	"dir internal/evalrun/scenario.go loadScenario":                            "the eval harness: a scenario's own directory",
	"dir internal/gitcli/cache.go Runner.InitRepo":                             "inside the Git cache",
	"dir internal/gitcli/cache.go WriteFileDurable":                            "a file's own directory, synced after a write inside the Git cache",
	"dir internal/gitcli/commands.go verifyPack":                               "a pack's own directory inside a repository",
	"dir internal/winstate/winstate.go Save":                                   "the window file's own directory; the file is localstore's Window",
	"dotdot cmd/klyx/eval.go labEval.runA3":                                    "the eval harness's lab script beside its scenarios",
	"dotdot internal/cachediff/child.go filepathClean":                         "refuses a path that climbs out",
	"dotdot internal/cachediff/child.go readInput":                             "refuses a path that climbs out",
	"dotdot internal/cachediff/scan.go references":                             "refuses a reference that climbs out",
	"dotdot internal/config/endpoints.go validateEndpoint":                     "refuses an endpoint path that climbs out",
	"dotdot internal/crd/messages.go validRefName":                             "refuses a name with a path segment",
	"dotdot internal/evalrun/preflight.go inDir":                               "the eval harness: a path that climbs out of the lab",
	"dotdot internal/gitcli/commands.go CleanPath":                             "refuses a path that climbs out",
	"dotdot internal/gitcli/refs.go checkRefName":                              "refuses a ref name with a path segment",
	"dotdot internal/identity/values.go valueOf":                               "refuses a path segment in a value",
	"fleet cmd/klyx/main.go configPath":                                        "the one place the app takes the fleet file's path",
	"fleet cmd/klyx/smoke.go init":                                             "the smoke harness requires a lab fleet file",
	"fleet cmd/klyx/eval.go init":                                              "the eval harness requires a lab fleet file",
	"fleet internal/appbridge/settings_service.go SettingsService.Diagnostics": "says whether KLYX_CONFIG set the path; never reads beside it",
}

var dotdotSegment = regexp.MustCompile(`(^|[/\\])\.\.([/\\]|$)`)

// dirSites are a file's parent-directory derivations, fleet-variable
// references and store-name literals, each as "kind file function".
func dirSites(f *ast.File, file string) []string {
	named, _ := imports(f)
	var out []string
	for _, d := range f.Decls {
		fn := "<top>"
		if fd, ok := d.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				fn = recvName(fd.Recv.List[0].Type) + "." + fn
			}
		}
		site := func(kind string) { out = append(out, kind+" "+file+" "+fn) }
		ast.Inspect(d, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				if storeNames[v] {
					out = append(out, "store "+file+" "+fn+" "+n.Value)
				}
				if dotdotSegment.MatchString(v) {
					site("dotdot")
				}
			case *ast.SelectorExpr:
				x, ok := n.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case (named[x.Name] == "path/filepath" || named[x.Name] == "path") && (n.Sel.Name == "Dir" || n.Sel.Name == "Split"):
					site("dir") // called or not: a function value derives a parent too
				case named[x.Name] == "github.com/moomora/klyx/internal/envread" && n.Sel.Name == "KlyxConfig":
					site("fleet")
				}
			}
			return true
		})
	}
	return out
}

func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	}
	return "?"
}

// dirRefs are a file's sites that are not on the reviewed list.
func dirRefs(f *ast.File, file string) []string {
	var out []string
	for _, s := range dirSites(f, file) {
		if _, ok := reviewedSites[s]; !ok {
			out = append(out, s)
		}
	}
	return out
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
		rel := filepath.ToSlash(strings.TrimPrefix(p, filepath.Join("..", "..")+string(filepath.Separator)))
		for _, r := range dirRefs(parse(t, p), rel) {
			t.Errorf("%s: not reviewed (a parent directory, the fleet file's variable or a store name outside internal/localstore): add it to reviewedSites with its reason, or keep the store in localstore", r)
		}
	}
}

// Every reviewed site still exists, so the list never grows stale.
func TestEveryReviewedSiteExists(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range productFiles(t) {
		rel := filepath.ToSlash(strings.TrimPrefix(p, filepath.Join("..", "..")+string(filepath.Separator)))
		for _, s := range dirSites(parse(t, p), rel) {
			seen[s] = true
		}
	}
	for s := range reviewedSites {
		if !seen[s] {
			t.Errorf("reviewed site %q is gone: remove it", s)
		}
	}
}

// Every fixture but the clean one fails its scan.
func TestTheScansRefuseEveryFixture(t *testing.T) {
	dirScan := func(f *ast.File) []string { return dirRefs(f, "testdata/dirscan/fixture.go") }
	for dir, scan := range map[string]func(*ast.File) []string{"envscan": envRefs, "dirscan": dirScan} {
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

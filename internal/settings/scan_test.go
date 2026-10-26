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
	f, _, _ := parseSrc(t, path, nil)
	return f
}

// parseSrc parses a file, or src under its name, keeping what a site's text
// is read from.
func parseSrc(t *testing.T, path string, src []byte) (*ast.File, *token.FileSet, []byte) {
	t.Helper()
	if src == nil {
		var err error
		if src, err = os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f, fset, src
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
	"fleet cmd/klyx/eval.go init envread.KlyxConfig":                                              "the eval harness requires a lab fleet file",
	"dotdot cmd/klyx/eval.go labEval.runA3 \"..\"":                                                "the eval harness's lab script beside its scenarios",
	"fleet cmd/klyx/main.go configPath envread.KlyxConfig":                                        "the one place the app takes the fleet file's path",
	"fleet cmd/klyx/smoke.go init envread.KlyxConfig":                                             "the smoke harness requires a lab fleet file",
	"dir internal/ai/claudecli.go claudeCLIProvider.callDir filepath.Dir(dir)":                    "walks up from the Claude Code executable to its install root",
	"dir internal/ai/claudecli.go claudeCLIProvider.callDir filepath.Dir(dir)#2":                  "walks up from the Claude Code executable to its install root",
	"dir internal/ai/claudecli_builds.go claudeBuildStore.replaceLocked filepath.Dir(s.path)":     "the builds file's own directory, for its temp file; the file is localstore's ClaudeBuilds",
	"dir internal/ai/claudecli_builds.go claudeBuildStore.replaceLocked filepath.Dir(s.path)#2":   "the builds file's own directory, for its temp file; the file is localstore's ClaudeBuilds",
	"fleet internal/appbridge/settings_service.go SettingsService.Diagnostics envread.KlyxConfig": "says whether KLYX_CONFIG set the path; never reads beside it",
	"dotdot internal/cachediff/child.go readInput \"..\"":                                         "refuses a path that climbs out",
	"dotdot internal/cachediff/child.go readInput \"../\"":                                        "refuses a path that climbs out",
	"dotdot internal/cachediff/child.go filepathClean \"..\"":                                     "refuses a path that climbs out",
	"dotdot internal/cachediff/child.go filepathClean \"../\"":                                    "refuses a path that climbs out",
	"dotdot internal/cachediff/scan.go references \"..\"":                                         "refuses a reference that climbs out",
	"dotdot internal/cachediff/scan.go references \"../\"":                                        "refuses a reference that climbs out",
	"dir internal/config/append.go fleetTemp filepath.Dir(target)":                                "the fleet writers' resolved target, for the temp file beside it",
	"dir internal/config/append.go AppendClusters filepath.Dir(target)":                           "the fleet writers' resolved target (slice 0's write policy), not a store root",
	"dotdot internal/config/endpoints.go validateEndpoint \"/../\"":                               "refuses an endpoint path that climbs out",
	"dotdot internal/crd/messages.go validRefName \"..\"":                                         "refuses a name with a path segment",
	"dir internal/evalrun/preflight.go Preflight.Run filepath.Dir(abs)":                           "the eval harness checks the lab's own fleet file lies in the lab",
	"dotdot internal/evalrun/preflight.go inDir \"..\"":                                           "the eval harness: a path that climbs out of the lab",
	"dir internal/evalrun/scenario.go loadScenario filepath.Dir(path)":                            "the eval harness: a scenario's own directory",
	"dir internal/gitcli/cache.go Runner.InitRepo filepath.Dir(gitDir)":                           "inside the Git cache",
	"dir internal/gitcli/cache.go WriteFileDurable filepath.Dir(path)":                            "a file's own directory, synced after a write inside the Git cache",
	"dotdot internal/gitcli/commands.go CleanPath \"..\"":                                         "refuses a path that climbs out",
	"dir internal/gitcli/commands.go verifyPack filepath.Dir(idx)":                                "a pack's own directory inside a repository",
	"dotdot internal/gitcli/refs.go checkRefName \"..\"":                                          "refuses a ref name with a path segment",
	"dotdot internal/identity/values.go valueOf \"..\"":                                           "refuses a path segment in a value",
	"dir internal/winstate/winstate.go Save filepath.Dir(path)":                                   "the window file's own directory; the file is localstore's Window",
}

var dotdotSegment = regexp.MustCompile(`(^|[/\\])\.\.([/\\]|$)`)

// dirPackages are the packages whose parent-directory functions are sites,
// and envread, whose KlyxConfig is: a dot import of any of them is refused,
// since its functions would be unqualified (Codex's re-review, RR1).
var dirPackages = map[string]bool{"path/filepath": true, "path": true, "github.com/moomora/klyx/internal/envread": true}

// dirSites are a file's parent-directory derivations, fleet-variable
// references, store-name literals and refused dot imports. Each is one
// occurrence: "kind file function text", the text being the occurrence's
// own source (a call with its argument, a literal), numbered "#2" on when a
// function repeats it. A reviewed site therefore approves that occurrence
// only: another derivation in the same function is a new site (RR1).
func dirSites(f *ast.File, fset *token.FileSet, src []byte, file string) []string {
	named, dot := imports(f)
	var out []string
	for _, p := range dot {
		if dirPackages[p] {
			out = append(out, "dotimport "+file+" "+p)
		}
	}
	text := func(n ast.Node) string {
		return strings.Join(strings.Fields(string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])), " ")
	}
	seen := map[string]int{}
	for _, d := range f.Decls {
		fn := "<top>"
		if fd, ok := d.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				fn = recvName(fd.Recv.List[0].Type) + "." + fn
			}
		}
		site := func(kind string, n ast.Node) {
			key := kind + " " + file + " " + fn + " " + text(n)
			seen[key]++
			if c := seen[key]; c > 1 {
				key += "#" + strconv.Itoa(c)
			}
			out = append(out, key)
		}
		var stack []ast.Node
		ast.Inspect(d, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			parent := ast.Node(nil)
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, n)
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
					site("dotdot", n)
				}
			case *ast.SelectorExpr:
				x, ok := n.X.(*ast.Ident)
				if !ok {
					return true
				}
				// the call, when the selector is called; the bare function
				// value otherwise
				var at ast.Node = n
				if c, ok := parent.(*ast.CallExpr); ok && c.Fun == n {
					at = c
				}
				switch {
				case (named[x.Name] == "path/filepath" || named[x.Name] == "path") && (n.Sel.Name == "Dir" || n.Sel.Name == "Split"):
					site("dir", at)
				case named[x.Name] == "github.com/moomora/klyx/internal/envread" && n.Sel.Name == "KlyxConfig":
					site("fleet", n)
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
func dirRefs(f *ast.File, fset *token.FileSet, src []byte, file string) []string {
	var out []string
	for _, s := range dirSites(f, fset, src, file) {
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
		f, fset, src := parseSrc(t, p, nil)
		for _, r := range dirRefs(f, fset, src, rel) {
			t.Errorf("%s: not reviewed (a parent directory, the fleet file's variable or a store name outside internal/localstore): add it to reviewedSites with its reason, or keep the store in localstore", r)
		}
	}
}

// Every reviewed site still exists, so the list never grows stale.
func TestEveryReviewedSiteExists(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range productFiles(t) {
		rel := filepath.ToSlash(strings.TrimPrefix(p, filepath.Join("..", "..")+string(filepath.Separator)))
		f, fset, src := parseSrc(t, p, nil)
		for _, s := range dirSites(f, fset, src, rel) {
			seen[s] = true
		}
	}
	for s := range reviewedSites {
		if !seen[s] {
			t.Errorf("reviewed site %q is gone: remove it", s)
		}
	}
}

// Codex's re-review, RR1: a reviewed site approves that occurrence only. A
// new derivation in a reviewed function - with new text, or the same text
// again - is a new site, in the real file.
func TestANewDerivationInAReviewedFunctionIsNotReviewed(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "config", "append.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head := regexp.MustCompile(`(?m)^func AppendClusters\([^\n]*\{\n`)
	if !head.Match(src) {
		t.Fatal("AppendClusters moved: update this test")
	}
	for name, line := range map[string]string{
		"new text":  "\t_ = os.WriteFile(filepath.Join(filepath.Dir(path), \"new-preference.json\"), []byte(\"{}\"), 0o600)\n",
		"same text": "\t_ = filepath.Dir(target)\n",
	} {
		mutated := head.ReplaceAllFunc(src, func(m []byte) []byte { return append(append([]byte{}, m...), line...) })
		f, fset, msrc := parseSrc(t, path, mutated)
		if got := dirRefs(f, fset, msrc, "internal/config/append.go"); len(got) != 1 {
			t.Errorf("%s: %v", name, got)
		}
	}
	f, fset, osrc := parseSrc(t, path, nil)
	if got := dirRefs(f, fset, osrc, "internal/config/append.go"); len(got) != 0 {
		t.Fatalf("the file as it is: %v", got)
	}
}

// Every fixture but the clean one fails its scan.
func TestTheScansRefuseEveryFixture(t *testing.T) {
	for dir, scan := range map[string]func(string) []string{
		"envscan": func(p string) []string { return envRefs(parse(t, p)) },
		"dirscan": func(p string) []string {
			f, fset, src := parseSrc(t, p, nil)
			return dirRefs(f, fset, src, "testdata/dirscan/fixture.go")
		},
	} {
		entries, err := os.ReadDir(filepath.Join("testdata", dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got := scan(filepath.Join("testdata", dir, e.Name()))
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

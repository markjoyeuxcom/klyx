// Package localstore is the only holder of the fleet file's directory
// (Settings S1 §3.2). Every file or directory Klyx keeps beside the fleet
// file comes from Root.Path, with an opaque Store handle declared here, so a
// store Klyx keeps is always declared and described in Settings. A scan in
// internal/settings fails on any filepath.Dir of the fleet path elsewhere.
//
// The root is the fleet file's lexical directory, as before, so KLYX_CONFIG
// still relocates the adjacent files. It is not the fleet writers' resolved
// symlink target (internal/config), and nothing here writes.
package localstore

import (
	"errors"
	"path/filepath"
	"strings"
)

// Store is a declared store. Its field is unexported, so outside this
// package only the zero Store can be made, and Path refuses it.
type Store struct{ s *store }

type store struct {
	name string
	kind string // file or dir
}

var (
	stores = map[*store]bool{}
	order  []*store
)

func declare(name, kind string) Store {
	s := &store{name: name, kind: kind}
	stores[s] = true
	order = append(order, s)
	return Store{s}
}

// The stores.
var (
	Window       = declare("window.json", "file")        // the window's size and place (winstate)
	ClaudeBuilds = declare("claude-builds.json", "file") // the Claude Code builds Test admitted
	GitCache     = declare("git-cache", "dir")           // the bare partial clones and the generated git config
)

var (
	// ErrUndeclared is a zero or undeclared Store, refused before a path is
	// formed.
	ErrUndeclared = errors.New("localstore: an undeclared store")
	// ErrNoRoot is a Root made from no fleet path.
	ErrNoRoot = errors.New("localstore: no fleet file to keep stores beside")
)

// Root is the directory beside one fleet file. main makes the app's once, at
// startup; a test makes its own beside a temporary fleet file.
type Root struct{ dir string }

// Beside is the root for the fleet file at fleetPath: its lexical directory.
func Beside(fleetPath string) Root {
	if strings.TrimSpace(fleetPath) == "" {
		return Root{}
	}
	return Root{dir: filepath.Dir(fleetPath)}
}

// Path is where the store lives under the root.
func (r Root) Path(s Store) (string, error) {
	if s.s == nil || !stores[s.s] {
		return "", ErrUndeclared
	}
	if r.dir == "" {
		return "", ErrNoRoot
	}
	return filepath.Join(r.dir, s.s.name), nil
}

// Name is the store's file or directory name, "" for an undeclared one.
func (s Store) Name() string {
	if s.s == nil || !stores[s.s] {
		return ""
	}
	return s.s.name
}

// DeclaredStore is one declaration, as Declared lists it.
type DeclaredStore struct {
	Store Store
	Name  string
	Kind  string
}

// Declared is every store, in declaration order, for the registry's
// coverage test.
func Declared() []DeclaredStore {
	out := make([]DeclaredStore, 0, len(order))
	for _, s := range order {
		out = append(out, DeclaredStore{Store: Store{s}, Name: s.name, Kind: s.kind})
	}
	return out
}

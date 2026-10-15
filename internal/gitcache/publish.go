package gitcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moomora/klyx/internal/gitcli"
)

const (
	kindHistory = "history"
	kindContent = "content"
)

// journal records one publication before it begins (parent §7 step 4):
// every file it is about to create in objects/pack, and every ref update
// with the old value read at the start of publication.
type journal struct {
	Kind      string             `json:"kind"`
	Files     []string           `json:"files"`
	Refs      []gitcli.RefUpdate `json:"refs,omitempty"`
	Committed bool               `json:"committed"`
}

// errCrash marks an acquisition the test hook abandoned at a recovery
// point, leaving the disk as it stands.
var errCrash = errors.New("abandoned at a test recovery point")

// at is a recovery point between journaled steps. The test hook may abandon
// the publication there (errCrash) or fail the step (any other error).
func (s *Store) at(step string) error {
	if s.crash != nil {
		return s.crash(step)
	}
	return nil
}

// publish moves the validated packs into the cache and, for a history
// refresh, moves the refs - each step durable before the next: journal,
// files, refs, mark, remove. A failure before the refs commit is undone by
// recovery, which decides from the refs alone.
func (s *Store) publish(ctx context.Context, dir, kind string, packs []pack, refs func(old map[string]string) []gitcli.RefUpdate) error {
	s.pub.Lock()
	defer s.pub.Unlock()
	stgPack := filepath.Join(dir+stagingExt, "objects", "pack")
	dstPack := filepath.Join(dir, "objects", "pack")
	// A pack whose name the cache already holds is the same content: it is
	// neither moved nor journaled, so a rollback can never delete it.
	var files []string
	var moving []pack
	for _, p := range packs {
		if _, err := os.Stat(filepath.Join(dstPack, p.files[len(p.files)-1])); err == nil {
			continue
		}
		moving = append(moving, p)
		files = append(files, p.files...)
	}
	var updates []gitcli.RefUpdate
	if refs != nil {
		cur, err := s.run.Repo(dir).Refs(ctx)
		if err != nil {
			return err
		}
		old := map[string]string{}
		for _, v := range cur {
			old[v.Name] = v.Value
		}
		updates = refs(old)
		if len(updates) == 0 {
			return nil // history unchanged: nothing to publish
		}
	}
	if len(files) == 0 && len(updates) == 0 {
		return nil
	}
	j := journal{Kind: kind, Files: files, Refs: updates}
	// From the journal on, every failure goes to recovery at once - one
	// decision path, from the refs - keeping its own error. Only a test's
	// abandoned publication (errCrash) is left for the next start.
	fail := func(err error) error {
		if errors.Is(err, errCrash) {
			return err
		}
		return s.undo(ctx, dir, err)
	}
	if err := writeJournal(dir, j); err != nil {
		return fail(err)
	}
	if err := s.at("journal"); err != nil {
		return fail(err)
	}
	if err := s.moveFiles(stgPack, dstPack, moving); err != nil {
		return fail(err)
	}
	if len(updates) > 0 {
		if err := s.run.Repo(dir).UpdateRefs(ctx, updates); err != nil {
			return fail(err)
		}
		if err := s.at("refs"); err != nil {
			return fail(err)
		}
	}
	j.Committed = true
	if err := writeJournal(dir, j); err != nil {
		return fail(err)
	}
	if err := s.at("mark"); err != nil {
		return fail(err)
	}
	err := s.at("remove")
	if err == nil {
		err = removeJournal(dir)
	}
	if err != nil {
		return fail(err)
	}
	return nil
}

// undo hands a failed publication to recovery, which reads the refs.
func (s *Store) undo(ctx context.Context, dir string, cause error) error {
	if err := s.recoverDir(ctx, dir); err != nil {
		return fmt.Errorf("%w; recovery: %v", cause, err)
	}
	return cause
}

// moveFiles moves each pack's files, .idx last so git sees a pack only once
// it is whole; each file is fsynced before its move and the directory after.
func (s *Store) moveFiles(src, dst string, packs []pack) error {
	for i, p := range packs {
		last := len(p.files) - 1
		for k, name := range p.files {
			if k == last {
				if err := gitcli.SyncDir(dst); err != nil {
					return err
				}
			}
			if err := syncFile(filepath.Join(src, name)); err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
				return err
			}
		}
		if err := gitcli.SyncDir(dst); err != nil {
			return err
		}
		if i == 0 {
			if err := s.at("files-partial"); err != nil {
				return err
			}
		}
	}
	return s.at("files")
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// recoverAll recovers every cache with a journal left behind. The caller
// holds s.pub.
func (s *Store) recoverAll(ctx context.Context) error {
	entries, err := os.ReadDir(s.base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		dir := filepath.Join(s.base, e.Name())
		if _, err := os.Stat(filepath.Join(dir, journalName)); err != nil {
			continue
		}
		if err := s.recoverDir(ctx, dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// recoverDir finishes or undoes the journal left in dir, deciding by what
// the refs show now, never by the mark alone (parent §7 step 5). The caller
// holds s.pub.
func (s *Store) recoverDir(ctx context.Context, dir string) error {
	if needsRepair(dir) {
		return ErrNeedsRepair
	}
	j, ok, err := readJournal(dir)
	if err != nil {
		return s.markRepair(dir, err.Error())
	}
	if !ok {
		return nil
	}
	packDir := filepath.Join(dir, "objects", "pack")
	allPresent := true
	for _, f := range j.Files {
		if _, err := os.Stat(filepath.Join(packDir, f)); err != nil {
			allPresent = false
		}
	}
	if j.Kind == kindContent {
		// The new blobs are never ref targets: the cache stays a valid
		// partial clone either way.
		if j.Committed && allPresent {
			return removeJournal(dir)
		}
		return rollBack(dir, j)
	}
	cur, err := s.run.Repo(dir).Refs(ctx)
	if err != nil {
		return err // git did not run: decide nothing, try again next time
	}
	now := map[string]string{}
	for _, v := range cur {
		now[v.Name] = v.Value
	}
	var atNew, atOld []gitcli.RefUpdate
	for _, u := range j.Refs {
		switch now[u.Name] {
		case u.New:
			atNew = append(atNew, u)
		case u.Old:
			atOld = append(atOld, u)
		default:
			return s.markRepair(dir, fmt.Sprintf("%s is at %s, neither the old %s nor the new %s",
				u.Name, orNone(now[u.Name]), orNone(u.Old), orNone(u.New)))
		}
	}
	switch {
	case len(atOld) == 0: // every ref at new: the transaction committed
		return removeJournal(dir)
	case len(atNew) == 0 && !j.Committed: // it never committed
		return rollBack(dir, j)
	case len(atNew) == 0: // marked committed, yet every ref is back at old
		return s.markRepair(dir, "the publication was marked committed but every ref is at its old value")
	case allPresent: // mixed, files whole: move the rest forward
		if err := s.run.Repo(dir).UpdateRefs(ctx, atOld); err != nil {
			return s.markRepair(dir, "moving the remaining refs forward failed: "+err.Error())
		}
		return removeJournal(dir)
	default: // mixed, files missing: restore every ref, then roll back
		var back []gitcli.RefUpdate
		for _, u := range atNew {
			back = append(back, gitcli.RefUpdate{Name: u.Name, New: u.Old, Old: u.New})
		}
		if err := s.run.Repo(dir).UpdateRefs(ctx, back); err != nil {
			return s.markRepair(dir, "restoring the refs failed: "+err.Error())
		}
		return rollBack(dir, j)
	}
}

func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}

// rollBack deletes exactly the journaled files - each pack's .idx first,
// so git never sees a pack without its data - then the journal.
func rollBack(dir string, j journal) error {
	packDir := filepath.Join(dir, "objects", "pack")
	var idx, rest []string
	for _, f := range j.Files {
		if strings.HasSuffix(f, ".idx") {
			idx = append(idx, f)
		} else {
			rest = append(rest, f)
		}
	}
	for _, group := range [][]string{idx, rest} {
		for _, f := range group {
			if err := os.Remove(filepath.Join(packDir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := gitcli.SyncDir(packDir); err != nil {
			return err
		}
	}
	return removeJournal(dir)
}

// markRepair deletes nothing: it records why, and the cache is not used
// again until Clear rebuilds it.
func (s *Store) markRepair(dir, why string) error {
	if err := gitcli.WriteFileDurable(filepath.Join(dir, repairName), []byte(why+"\n"), 0o600); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrNeedsRepair, why)
}

func writeJournal(dir string, j journal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return gitcli.WriteFileDurable(filepath.Join(dir, journalName), b, 0o600)
}

func removeJournal(dir string) error {
	if err := os.Remove(filepath.Join(dir, journalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return gitcli.SyncDir(dir)
}

// readJournal reads and validates a journal: its files are pack files by
// name only, its refs Klyx refs with full ids - so a damaged journal can
// never name anything else to delete or move.
func readJournal(dir string) (journal, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, journalName))
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false, nil
	}
	if err != nil {
		return journal{}, false, err
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		return journal{}, false, fmt.Errorf("the publication journal is unreadable: %v", err)
	}
	if j.Kind != kindHistory && j.Kind != kindContent {
		return journal{}, false, fmt.Errorf("the publication journal has kind %q", j.Kind)
	}
	for _, f := range j.Files {
		if !packFile.MatchString(f) {
			return journal{}, false, fmt.Errorf("the publication journal names %q", f)
		}
	}
	for _, u := range j.Refs {
		if !gitcli.IsKlyxRef(u.Name) || (u.New != "" && !gitcli.IsSHA(u.New)) || (u.Old != "" && !gitcli.IsSHA(u.Old)) ||
			u.New == u.Old || j.Kind == kindContent {
			return journal{}, false, fmt.Errorf("the publication journal has a bad ref update for %q", u.Name)
		}
	}
	return j, true, nil
}

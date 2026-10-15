package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Repo runs the typed commands against one repository: a cache, or a
// staging repo during an acquisition.
type Repo struct {
	r   Runner
	dir string
}

// Repo binds gitDir, which must be an absolute path Klyx created.
func (r Runner) Repo(gitDir string) Repo { return Repo{r: r, dir: gitDir} }

// Dir is the repository's GIT_DIR.
func (p Repo) Dir() string { return p.dir }

// FetchHistory runs a history refresh into this (staging) repo.
func (p Repo) FetchHistory(ctx context.Context, refs []Ref) error {
	c, err := fetchHistory(p.dir, refs)
	if err == nil {
		_, err = p.r.run(ctx, c)
	}
	return err
}

// FetchContent runs a content acquisition of exactly ids into this
// (staging) repo.
func (p Repo) FetchContent(ctx context.Context, ids []string) error {
	c, err := fetchContent(p.dir, ids)
	if err == nil {
		_, err = p.r.run(ctx, c)
	}
	return err
}

// TreeEntry is one ls-tree or diff-tree endpoint.
type TreeEntry struct {
	Mode, Type, ID, Path string
}

// LsTree lists the blobs (and gitlinks) under paths at rev.
func (p Repo) LsTree(ctx context.Context, rev Rev, paths []string) ([]TreeEntry, error) {
	c, err := lsTree(p.dir, rev, paths)
	if err != nil {
		return nil, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("ls-tree output passed %d bytes", outputMax)
	}
	return parseLsTree(res.Stdout)
}

// parseLsTree reads `<mode> SP <type> SP <id> TAB <path> NUL` records.
func parseLsTree(out []byte) ([]TreeEntry, error) {
	var es []TreeEntry
	for _, rec := range splitNul(out) {
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 || !validSHA(f[2]) {
			return nil, fmt.Errorf("unexpected ls-tree record %q", truncate(string(rec), 80))
		}
		es = append(es, TreeEntry{Mode: f[0], Type: f[1], ID: f[2], Path: string(path)})
	}
	return es, nil
}

// TreeChange is one changed path between two trees, with both endpoints.
type TreeChange struct {
	OldMode, NewMode, OldID, NewID string
	Status                         string
	Path                           string
}

// DiffTree lists the changed paths between a and b under paths.
func (p Repo) DiffTree(ctx context.Context, a, b Rev, paths []string) ([]TreeChange, error) {
	c, err := diffTree(p.dir, a, b, paths)
	if err != nil {
		return nil, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("diff-tree output passed %d bytes", outputMax)
	}
	return parseDiffTree(res.Stdout)
}

// parseDiffTree reads `:<old mode> <new mode> <old id> <new id> <status>
// NUL <path> NUL` records (-z, no renames: one path each).
func parseDiffTree(out []byte) ([]TreeChange, error) {
	recs := splitNul(out)
	var cs []TreeChange
	for i := 0; i < len(recs); i += 2 {
		f := strings.Fields(strings.TrimPrefix(string(recs[i]), ":"))
		if !bytes.HasPrefix(recs[i], []byte(":")) || len(f) != 5 || !validSHA(f[2]) || !validSHA(f[3]) || i+1 >= len(recs) {
			return nil, fmt.Errorf("unexpected diff-tree record %q", truncate(string(recs[i]), 80))
		}
		cs = append(cs, TreeChange{OldMode: f[0], NewMode: f[1], OldID: f[2], NewID: f[3], Status: f[4], Path: string(recs[i+1])})
	}
	return cs, nil
}

// Missing reports which of ids this repo does not have.
func (p Repo) Missing(ctx context.Context, ids []string) ([]string, error) {
	c, err := catFileCheck(p.dir, ids)
	if err != nil {
		return nil, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("cat-file output passed %d bytes", outputMax)
	}
	return parseBatchCheck(res.Stdout, ids)
}

// parseBatchCheck reads one line per asked id, in order: `<id> <type>
// <size>` or `<id> missing`.
func parseBatchCheck(out []byte, ids []string) ([]string, error) {
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, fmt.Errorf("cat-file answered %d of %d ids", len(lines), len(ids))
	}
	var missing []string
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != ids[i] {
			return nil, fmt.Errorf("unexpected cat-file line %q", truncate(l, 80))
		}
		if f[1] == "missing" {
			missing = append(missing, ids[i])
		}
	}
	return missing, nil
}

// PackObjects is what one pack holds, by type, with its blob ids up to
// MaxContentIDs+1.
type PackObjects struct {
	Commits, Trees, Tags, Blobs int
	BlobIDs                     []string
}

// VerifyPack validates one pack this acquisition created and enumerates
// its objects. The listing streams through a line parser, so a large
// history pack is never held in memory.
func (p Repo) VerifyPack(ctx context.Context, idx string) (PackObjects, error) {
	c, err := verifyPack(p.dir, idx)
	if err != nil {
		return PackObjects{}, err
	}
	sc := &packScan{}
	c.stdout = sc
	_, err = p.r.run(ctx, c)
	if sc.err != nil {
		return PackObjects{}, sc.err // the scan's refusal, not the broken pipe it caused
	}
	if err != nil {
		return PackObjects{}, err
	}
	sc.flush()
	if sc.err != nil {
		return PackObjects{}, sc.err
	}
	return sc.objs, nil
}

// packScan parses verify-pack -v lines as they arrive: `<id> <type> <size>
// <size in pack> <offset> [<depth> <base id>]`. Summary lines are skipped.
type packScan struct {
	objs PackObjects
	line []byte
	err  error
}

const maxPackLine = 512

func (s *packScan) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 && s.err == nil {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			s.line = append(s.line, b...)
			if len(s.line) > maxPackLine {
				s.err = fmt.Errorf("verify-pack line longer than %d bytes", maxPackLine)
			}
			break
		}
		s.line = append(s.line, b[:i]...)
		s.take(s.line)
		s.line = s.line[:0]
		b = b[i+1:]
	}
	return n, s.err
}

func (s *packScan) flush() {
	if len(s.line) > 0 {
		s.take(s.line)
		s.line = nil
	}
}

func (s *packScan) take(line []byte) {
	f := strings.Fields(string(line))
	if len(f) < 5 || !validSHA(f[0]) {
		return // "non delta: …", "chain length …", "<path>: ok"
	}
	switch f[1] {
	case "commit":
		s.objs.Commits++
	case "tree":
		s.objs.Trees++
	case "tag":
		s.objs.Tags++
	case "blob":
		s.objs.Blobs++
		if len(s.objs.BlobIDs) <= MaxContentIDs {
			s.objs.BlobIDs = append(s.objs.BlobIDs, f[0])
		}
	default:
		s.err = fmt.Errorf("verify-pack listed an object of type %q", truncate(f[1], 20))
	}
}

// ErrContentMissing: a blob the read needs is not in the cache.
var ErrContentMissing = errors.New("content incomplete")

// Blobs reads exactly the listed blobs, in order, handing each to fn as it
// completes. The total content is capped at max bytes; a missing object, an
// object that is not a blob, or a reply for another id stops the read.
func (p Repo) Blobs(ctx context.Context, ids []string, max int64, fn func(id string, data []byte) error) error {
	c, err := catFileBatch(p.dir, ids)
	if err != nil {
		return err
	}
	sc := &blobScan{want: ids, left: max, max: max, fn: fn}
	c.stdout = sc
	_, err = p.r.run(ctx, c)
	if sc.err != nil {
		return sc.err // the scan's refusal, not the broken pipe it caused
	}
	if err != nil {
		return err
	}
	if sc.next != len(ids) || sc.body != nil {
		return fmt.Errorf("cat-file answered %d of %d blobs", sc.next, len(ids))
	}
	return nil
}

// blobScan parses cat-file --batch as it arrives: "<id> blob <size>\n",
// size bytes, "\n" - or "<id> missing\n".
type blobScan struct {
	want      []string
	next      int
	left, max int64
	fn        func(string, []byte) error
	head      []byte
	body      []byte // the current object's content so far; nil between objects
	size      int
	err       error
}

func (s *blobScan) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 && s.err == nil {
		if s.body == nil { // a header
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				s.head = append(s.head, b...)
				if len(s.head) > 256 {
					s.err = fmt.Errorf("cat-file header longer than 256 bytes")
				}
				break
			}
			s.head = append(s.head, b[:i]...)
			b = b[i+1:]
			s.header(string(s.head))
			s.head = s.head[:0]
			continue
		}
		need := s.size + 1 - len(s.body) // the content and its trailing newline
		take := min(need, len(b))
		s.body = append(s.body, b[:take]...)
		b = b[take:]
		if len(s.body) == s.size+1 {
			if s.body[s.size] != '\n' {
				s.err = fmt.Errorf("cat-file content for %s is not terminated", s.want[s.next])
				break
			}
			if err := s.fn(s.want[s.next], s.body[:s.size]); err != nil {
				s.err = err
				break
			}
			s.body, s.next = nil, s.next+1
		}
	}
	return n, s.err
}

func (s *blobScan) header(h string) {
	if s.next >= len(s.want) {
		s.err = fmt.Errorf("cat-file answered more than was asked")
		return
	}
	id := s.want[s.next]
	f := strings.Fields(h)
	switch {
	case len(f) == 2 && f[0] == id && f[1] == "missing":
		s.err = fmt.Errorf("%w: blob %s is not in the cache", ErrContentMissing, id[:12])
	case len(f) != 3 || f[0] != id:
		s.err = fmt.Errorf("unexpected cat-file header %q", truncate(h, 80))
	case f[1] != "blob":
		s.err = fmt.Errorf("%s is a %s, not a blob", id[:12], truncate(f[1], 20))
	default:
		size, err := strconv.Atoi(f[2])
		switch {
		case err != nil || size < 0:
			s.err = fmt.Errorf("unexpected cat-file size %q", truncate(f[2], 20))
		case int64(size) > s.left:
			s.err = fmt.Errorf("the content passes its %d KiB cap", s.max>>10)
		default:
			s.size, s.left, s.body = size, s.left-int64(size), make([]byte, 0, size+1)
		}
	}
}

// UpdateRefs applies updates in one transaction.
func (p Repo) UpdateRefs(ctx context.Context, updates []RefUpdate) error {
	c, err := updateRefs(p.dir, updates)
	if err == nil {
		_, err = p.r.run(ctx, c)
	}
	return err
}

// RefValue is one Klyx ref: its value and, for an annotated tag, the
// commit it peels to ("" otherwise).
type RefValue struct {
	Name, Value, Peeled string
}

// Commit is the commit the ref names.
func (v RefValue) Commit() string {
	if v.Peeled != "" {
		return v.Peeled
	}
	return v.Value
}

// Refs reads every Klyx ref in this repo.
func (p Repo) Refs(ctx context.Context) ([]RefValue, error) {
	res, err := p.r.run(ctx, forEachRef(p.dir))
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("for-each-ref output passed %d bytes", outputMax)
	}
	return parseRefs(res.Stdout)
}

func parseRefs(out []byte) ([]RefValue, error) {
	var vs []RefValue
	for _, l := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if l == "" {
			continue
		}
		f := strings.Split(l, "\x00")
		if len(f) != 3 || checkKlyxRef(f[0]) != nil || !validSHA(f[1]) || (f[2] != "" && !validSHA(f[2])) {
			return nil, fmt.Errorf("unexpected for-each-ref line %q", truncate(l, 80))
		}
		vs = append(vs, RefValue{Name: f[0], Value: f[1], Peeled: f[2]})
	}
	return vs, nil
}

// Commit is one log entry. Author and Subject are untrusted text: valid
// UTF-8, control characters replaced, capped (parent §7).
type Commit struct {
	SHA       string
	Author    string
	Time      time.Time // authored
	Committed time.Time // the order git's own date order follows
	Subject   string
}

// Short is the abbreviated id the UI shows.
func (c Commit) Short() string { return c.SHA[:7] }

const (
	maxSubject = 200
	maxAuthor  = 200
)

// Log lists up to max commits in include and not in exclude, newest first.
func (p Repo) Log(ctx context.Context, include, exclude []Rev, max int) ([]Commit, error) {
	c, err := logCommits(p.dir, include, exclude, max)
	if err != nil {
		return nil, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("log output passed %d bytes", outputMax)
	}
	return parseLog(res.Stdout)
}

func parseLog(out []byte) ([]Commit, error) {
	var cs []Commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) != 5 || !validSHA(f[0]) {
			return nil, fmt.Errorf("unexpected log record %q", truncate(rec, 80))
		}
		at, err1 := strconv.ParseInt(f[2], 10, 64)
		ct, err2 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("unexpected log time %q / %q", truncate(f[2], 40), truncate(f[3], 40))
		}
		cs = append(cs, Commit{SHA: f[0], Author: plainText(f[1], maxAuthor), Time: time.Unix(at, 0).UTC(),
			Committed: time.Unix(ct, 0).UTC(), Subject: plainText(f[4], maxSubject)})
	}
	return cs, nil
}

// plainText makes untrusted text safe to show as plain text: valid UTF-8,
// control characters as spaces, at most max characters.
func plainText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

// Count counts commits in include and not in exclude.
func (p Repo) Count(ctx context.Context, include, exclude []Rev) (int, error) {
	c, err := revListCount(p.dir, include, exclude)
	if err != nil {
		return 0, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		return 0, fmt.Errorf("unexpected rev-list count %q", truncate(string(res.Stdout), 40))
	}
	return n, nil
}

// MergeBase is a and b's best common ancestor; ok is false when their
// histories are unrelated.
func (p Repo) MergeBase(ctx context.Context, a, b Rev) (sha string, ok bool, err error) {
	c, err := mergeBase(p.dir, a, b)
	if err != nil {
		return "", false, err
	}
	res, err := p.r.run(ctx, c, 1)
	if err != nil {
		return "", false, err
	}
	if res.ExitCode == 1 {
		return "", false, nil
	}
	sha = strings.TrimSpace(string(res.Stdout))
	if !validSHA(sha) {
		return "", false, fmt.Errorf("unexpected merge-base output %q", truncate(sha, 80))
	}
	return sha, true, nil
}

// IsAncestor reports whether a is an ancestor of b (or equal to it).
func (p Repo) IsAncestor(ctx context.Context, a, b Rev) (bool, error) {
	c, err := isAncestor(p.dir, a, b)
	if err != nil {
		return false, err
	}
	res, err := p.r.run(ctx, c, 1)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// NameStatus is one changed path. Path is untrusted text.
type NameStatus struct {
	Status string
	Path   string
}

// DiffNames lists the changed paths between a and b under paths.
func (p Repo) DiffNames(ctx context.Context, a, b Rev, paths []string) ([]NameStatus, error) {
	c, err := diffNameStatus(p.dir, a, b, paths)
	if err != nil {
		return nil, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.StdoutOver {
		return nil, fmt.Errorf("diff output passed %d bytes", outputMax)
	}
	return parseNameStatus(res.Stdout)
}

func parseNameStatus(out []byte) ([]NameStatus, error) {
	recs := splitNul(out)
	if len(recs)%2 != 0 {
		return nil, fmt.Errorf("unexpected name-status output")
	}
	var ns []NameStatus
	for i := 0; i < len(recs); i += 2 {
		st := string(recs[i])
		if st == "" || len(st) > 4 {
			return nil, fmt.Errorf("unexpected name-status %q", truncate(st, 20))
		}
		ns = append(ns, NameStatus{Status: st, Path: string(recs[i+1])})
	}
	return ns, nil
}

// Diff is a content diff between a and b under paths; truncated is true
// when it passed the output cap.
func (p Repo) Diff(ctx context.Context, a, b Rev, paths []string) (text string, truncated bool, err error) {
	c, err := diffContent(p.dir, a, b, paths)
	if err != nil {
		return "", false, err
	}
	res, err := p.r.run(ctx, c)
	if err != nil {
		return "", false, err
	}
	return string(res.Stdout), res.StdoutOver, nil
}

// splitNul splits NUL-terminated records.
func splitNul(out []byte) [][]byte {
	out = bytes.TrimSuffix(out, []byte{0})
	if len(out) == 0 {
		return nil
	}
	return bytes.Split(out, []byte{0})
}

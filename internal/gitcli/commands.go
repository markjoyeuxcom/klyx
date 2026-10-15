package gitcli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// The typed constructors: one per allowed command of parent §7, each
// building its own argv from validated inputs (slice 6 note §3.4). A
// revision is a Rev (a full object id or a tracked ref) placed after
// --end-of-options; a path is CleanPath's and comes after "--" - except for
// ls-tree and archive, which read a "--" after --end-of-options as a path,
// so their paths follow the revision directly.

// Rev is a revision a read may name: a full object id, or a tracked ref as
// published in the cache.
type Rev struct{ s string }

func (r Rev) String() string { return r.s }

// SHA is a full object id as a Rev.
func SHA(s string) (Rev, error) {
	if !validSHA(s) {
		return Rev{}, fmt.Errorf("%q is not a full object id", truncate(s, 80))
	}
	return Rev{s}, nil
}

// Tracked names the ref as published in the cache.
func (r Ref) Tracked() Rev { return Rev{r.Published()} }

func revArgs(include, exclude []Rev) ([]string, error) {
	if len(include) == 0 {
		return nil, fmt.Errorf("no revision")
	}
	var out []string
	for _, r := range include {
		if r.s == "" {
			return nil, fmt.Errorf("empty revision")
		}
		out = append(out, r.s)
	}
	for _, r := range exclude {
		if r.s == "" {
			return nil, fmt.Errorf("empty revision")
		}
		out = append(out, "^"+r.s)
	}
	return out, nil
}

// CleanPath validates a repo-relative path: no leading '/', no ".."
// segment, no leading '-', no backslash or control character. "./" and "."
// segments are dropped; "" is the repository root.
func CleanPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q is absolute", truncate(p, 80))
	}
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
		return "", fmt.Errorf("path %q has a backslash or control character", truncate(p, 80))
	}
	var segs []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("path %q leaves the repository", truncate(p, 80))
		}
		segs = append(segs, seg)
	}
	out := strings.Join(segs, "/")
	if strings.HasPrefix(out, "-") {
		return "", fmt.Errorf("path %q starts with '-'", truncate(p, 80))
	}
	return out, nil
}

// pathArgs cleans paths; the root among them means the whole tree, so no
// path is passed.
func pathArgs(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		c, err := CleanPath(p)
		if err != nil {
			return nil, err
		}
		if c == "" {
			return nil, nil
		}
		out = append(out, c)
	}
	return out, nil
}

// initBare creates a bare repository at dir with no template: no hooks,
// no description, no info/exclude.
func initBare(dir string) (cmd, error) {
	if !filepath.IsAbs(dir) {
		return cmd{}, fmt.Errorf("repository directory %q is not absolute", dir)
	}
	return cmd{args: []string{"init", "--bare", "--quiet", "--template=", "--end-of-options", dir}, dir: dir}, nil
}

// fetchHistory is a history refresh into a staging repo: commits and trees
// for the tracked refs, one explicit non-pattern refspec each, atomically.
func fetchHistory(stagingDir string, refs []Ref) (cmd, error) {
	if len(refs) == 0 {
		return cmd{}, fmt.Errorf("no tracked ref to fetch")
	}
	args := []string{"fetch", "--atomic", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no",
		"--filter=blob:none", "--quiet", "--end-of-options", "origin"}
	seen := map[string]bool{}
	for _, r := range refs {
		if r.full == "" && r.sha == "" {
			return cmd{}, fmt.Errorf("empty tracked ref")
		}
		if spec := r.refspec(); !seen[spec] {
			seen[spec] = true
			args = append(args, spec)
		}
	}
	return cmd{args: args, gitDir: stagingDir, fetch: true}, nil
}

// MaxContentIDs bounds one content acquisition (parent §7).
const MaxContentIDs = 5000

// fetchContent is a content acquisition into a staging repo: exactly the
// named blob ids, given on stdin, as git's own lazy fetch asks for them.
func fetchContent(stagingDir string, ids []string) (cmd, error) {
	in, err := idLines(ids)
	if err != nil {
		return cmd{}, err
	}
	return cmd{args: []string{"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--no-tags", "--no-write-fetch-head",
		"--recurse-submodules=no", "--filter=blob:none", "--quiet", "--stdin", "--end-of-options", "origin"},
		stdin: in, gitDir: stagingDir, fetch: true}, nil
}

func idLines(ids []string) ([]byte, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("no object id")
	}
	if len(ids) > MaxContentIDs {
		return nil, fmt.Errorf("%d object ids is over the %d limit", len(ids), MaxContentIDs)
	}
	var b strings.Builder
	for _, id := range ids {
		if !validSHA(id) {
			return nil, fmt.Errorf("%q is not a full object id", truncate(id, 80))
		}
		b.WriteString(id)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// lsTree lists a snapshot's blobs from trees only: no history walk, no
// blob read (no -l).
func lsTree(gitDir string, rev Rev, paths []string) (cmd, error) {
	ps, err := pathArgs(paths)
	if err != nil {
		return cmd{}, err
	}
	rs, err := revArgs([]Rev{rev}, nil)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"ls-tree", "-r", "-z", "--full-tree", "--end-of-options"}, rs...)
	return cmd{args: append(args, ps...), gitDir: gitDir}, nil
}

// diffTree lists a content diff's endpoint blobs: trees only, no renames.
func diffTree(gitDir string, a, b Rev, paths []string) (cmd, error) {
	ps, err := pathArgs(paths)
	if err != nil {
		return cmd{}, err
	}
	rs, err := revArgs([]Rev{a, b}, nil)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"diff-tree", "-r", "--no-renames", "--no-ext-diff", "--no-textconv", "-z", "--end-of-options"}, rs...)
	return cmd{args: append(append(args, "--"), ps...), gitDir: gitDir}, nil
}

// catFileCheck reports which listed ids are present (the environment holds
// GIT_NO_LAZY_FETCH, so a missing one is reported, never fetched).
func catFileCheck(gitDir string, ids []string) (cmd, error) {
	in, err := idLines(ids)
	if err != nil {
		return cmd{}, err
	}
	return cmd{args: []string{"cat-file", "--batch-check"}, stdin: in, gitDir: gitDir}, nil
}

// verifyPack validates one pack the acquisition created: its index must sit
// in the staging repo's own objects/pack, never in an alternate.
func verifyPack(stagingDir, idx string) (cmd, error) {
	packDir := filepath.Join(stagingDir, "objects", "pack")
	if !filepath.IsAbs(idx) || filepath.Dir(idx) != packDir || !strings.HasPrefix(filepath.Base(idx), "pack-") ||
		!strings.HasSuffix(idx, ".idx") {
		return cmd{}, fmt.Errorf("%q is not a pack index in %s", idx, packDir)
	}
	return cmd{args: []string{"verify-pack", "-v", "--", idx}, gitDir: stagingDir}, nil
}

// RefUpdate is one ref change in a publication or a recovery: New "" deletes
// the ref (it must be at Old); Old "" requires that it does not exist.
type RefUpdate struct {
	Name     string
	New, Old string
}

// updateRefs applies updates in one transaction, each naming its expected
// old value, to refs under refs/klyx/ only.
func updateRefs(gitDir string, updates []RefUpdate) (cmd, error) {
	if len(updates) == 0 {
		return cmd{}, fmt.Errorf("no ref update")
	}
	var b strings.Builder
	for _, u := range updates {
		if err := checkKlyxRef(u.Name); err != nil {
			return cmd{}, err
		}
		if u.New == "" && u.Old == "" {
			return cmd{}, fmt.Errorf("update of %s names neither value", u.Name)
		}
		for _, v := range []string{u.New, u.Old} {
			if v != "" && !validSHA(v) {
				return cmd{}, fmt.Errorf("%q is not a full object id", truncate(v, 80))
			}
		}
		switch {
		case u.New == "":
			fmt.Fprintf(&b, "delete %s %s\n", u.Name, u.Old)
		case u.Old == "":
			fmt.Fprintf(&b, "update %s %s %s\n", u.Name, u.New, strings.Repeat("0", len(u.New)))
		default:
			fmt.Fprintf(&b, "update %s %s %s\n", u.Name, u.New, u.Old)
		}
	}
	return cmd{args: []string{"update-ref", "--stdin"}, stdin: []byte(b.String()), gitDir: gitDir}, nil
}

// IsKlyxRef reports whether name is a staging or tracked ref Klyx itself
// names.
func IsKlyxRef(name string) bool { return checkKlyxRef(name) == nil }

// checkKlyxRef accepts only refs Klyx itself names: a staging or tracked
// ref of a validated branch, tag or object id.
func checkKlyxRef(name string) error {
	for _, ns := range []string{"refs/klyx/staging/", "refs/klyx/tracked/"} {
		rest, ok := strings.CutPrefix(name, ns)
		if !ok {
			continue
		}
		if sha, ok := strings.CutPrefix(rest, "commit/"); ok {
			if validSHA(sha) {
				return nil
			}
			break
		}
		if _, err := fullRef(rest); err == nil {
			return nil
		}
		break
	}
	return fmt.Errorf("%q is not a Klyx ref", truncate(name, 120))
}

// forEachRef reads every Klyx ref with its value and, for an annotated tag,
// the commit it peels to: the old values a publication names, and T's tip.
func forEachRef(gitDir string) cmd {
	return cmd{args: []string{"for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)", "--end-of-options", "refs/klyx/"},
		gitDir: gitDir}
}

// MaxLogCommits bounds a log read (parent §7: 20 commits per view).
const MaxLogCommits = 20

// logFormat: full id, author name, author time, subject - unit- and
// record-separated, so no field can end a record.
const logFormat = "--format=%H%x1f%an%x1f%at%x1f%s%x1e"

// logCommits lists commits in include and not in exclude, newest first.
func logCommits(gitDir string, include, exclude []Rev, max int) (cmd, error) {
	if max < 1 || max > MaxLogCommits {
		return cmd{}, fmt.Errorf("a log reads 1 to %d commits, not %d", MaxLogCommits, max)
	}
	rs, err := revArgs(include, exclude)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"log", "--no-renames", "--no-mailmap", "--no-notes", "--no-show-signature", "--no-color",
		"--max-count=" + strconv.Itoa(max), logFormat, "--end-of-options"}, rs...)
	return cmd{args: append(args, "--"), gitDir: gitDir}, nil
}

// revListCount counts commits in include and not in exclude.
func revListCount(gitDir string, include, exclude []Rev) (cmd, error) {
	rs, err := revArgs(include, exclude)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"rev-list", "--count", "--end-of-options"}, rs...)
	return cmd{args: append(args, "--"), gitDir: gitDir}, nil
}

// mergeBase finds the best common ancestor (exit 1: none).
func mergeBase(gitDir string, a, b Rev) (cmd, error) {
	rs, err := revArgs([]Rev{a, b}, nil)
	if err != nil {
		return cmd{}, err
	}
	return cmd{args: append([]string{"merge-base", "--end-of-options"}, rs...), gitDir: gitDir}, nil
}

// isAncestor asks whether a is an ancestor of b (exit 0 yes, 1 no).
func isAncestor(gitDir string, a, b Rev) (cmd, error) {
	rs, err := revArgs([]Rev{a, b}, nil)
	if err != nil {
		return cmd{}, err
	}
	return cmd{args: append([]string{"merge-base", "--is-ancestor", "--end-of-options"}, rs...), gitDir: gitDir}, nil
}

// diffNameStatus lists changed paths between two revisions: trees only.
func diffNameStatus(gitDir string, a, b Rev, paths []string) (cmd, error) {
	ps, err := pathArgs(paths)
	if err != nil {
		return cmd{}, err
	}
	rs, err := revArgs([]Rev{a, b}, nil)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"diff", "--name-status", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "-z",
		"--end-of-options"}, rs...)
	return cmd{args: append(append(args, "--"), ps...), gitDir: gitDir}, nil
}

// diffContent is a content diff, run only after its blobs were acquired:
// a blob still missing fails it (GIT_NO_LAZY_FETCH), never downloads.
func diffContent(gitDir string, a, b Rev, paths []string) (cmd, error) {
	ps, err := pathArgs(paths)
	if err != nil {
		return cmd{}, err
	}
	rs, err := revArgs([]Rev{a, b}, nil)
	if err != nil {
		return cmd{}, err
	}
	args := append([]string{"diff", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--end-of-options"}, rs...)
	return cmd{args: append(append(args, "--"), ps...), gitDir: gitDir}, nil
}

// archiveTar exports one subtree at rev to a tar file Klyx names, run only
// after its blobs were acquired. The subtree is the tree-ish <rev>:<path>
// with --prefix=<path>/, never a pathspec: in a partial clone, `git archive
// <rev> <path>` prefetches every blob of the whole tree whatever the
// pathspec (probed on 2.56), so a pathspec would read outside what was
// acquired. Output goes to the file, never through the captured stdout.
func archiveTar(gitDir string, rev Rev, path, output string) (cmd, error) {
	if !filepath.IsAbs(output) || strings.ContainsFunc(output, func(r rune) bool { return r < 0x20 }) {
		return cmd{}, fmt.Errorf("archive output %q is not an absolute path", output)
	}
	p, err := CleanPath(path)
	if err != nil {
		return cmd{}, err
	}
	rs, err := revArgs([]Rev{rev}, nil)
	if err != nil {
		return cmd{}, err
	}
	args := []string{"archive", "--format=tar", "--output=" + output}
	treeish := rs[0]
	if p != "" {
		args = append(args, "--prefix="+p+"/")
		treeish += ":" + p
	}
	return cmd{args: append(args, "--end-of-options", treeish), gitDir: gitDir}, nil
}

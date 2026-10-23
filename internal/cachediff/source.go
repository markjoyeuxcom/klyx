package cachediff

import (
	"context"

	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/readfence"
)

// Store is the Git cache as the cache diff reads it (internal/gitcache).
type Store interface {
	Read(url string, fn func(gitcli.Repo) error) error
	AcquireIDs(ctx context.Context, url string, ids []string) error
}

// CacheSource reads one revision of an approved repository's cache: trees
// from the cache itself, content acquired by exact blob id first.
type CacheSource struct {
	Store Store
	URL   string
	Rev   gitcli.Rev
}

// List and Read check the context's read fence before each read and each
// acquisition, which may fetch missing blobs: Compute makes many of them,
// round by round (AI A1 re-review, finding 1). A context without a fence
// reads as before.
func (c CacheSource) List(ctx context.Context, p string) ([]gitcli.TreeEntry, error) {
	if err := readfence.Check(ctx); err != nil {
		return nil, err
	}
	var es []gitcli.TreeEntry
	err := c.Store.Read(c.URL, func(r gitcli.Repo) error {
		var err error
		es, err = r.LsTree(ctx, c.Rev, []string{p})
		return err
	})
	return es, err
}

func (c CacheSource) Read(ctx context.Context, files []gitcli.TreeEntry) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(files) == 0 {
		return out, nil
	}
	byID := map[string][]string{}
	var ids []string
	for _, f := range files {
		if len(byID[f.ID]) == 0 {
			ids = append(ids, f.ID)
		}
		byID[f.ID] = append(byID[f.ID], f.Path)
	}
	if err := readfence.Check(ctx); err != nil {
		return nil, err
	}
	if err := c.Store.AcquireIDs(ctx, c.URL, ids); err != nil {
		return nil, err
	}
	if err := readfence.Check(ctx); err != nil {
		return nil, err
	}
	err := c.Store.Read(c.URL, func(r gitcli.Repo) error {
		return r.Blobs(ctx, ids, MaxBytes, func(id string, data []byte) error {
			for _, p := range byID[id] {
				out[p] = data
			}
			return nil
		})
	})
	return out, err
}

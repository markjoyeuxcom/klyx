package config

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// gitRepoYAML is one git.repos entry as Klyx writes it, in a fixed order.
type gitRepoYAML struct {
	URL         string   `yaml:"url"`
	Web         string   `yaml:"web,omitempty"`
	RemoteBases bool     `yaml:"remoteBases,omitempty"`
	Matches     []string `yaml:"matches,omitempty"`
}

type gitYAML struct {
	Repos []gitRepoYAML `yaml:"repos"`
}

// SaveGit replaces the fleet file's top-level git: block - the only writer
// of git.repos (§14.5: nothing else ever adds to it). It writes the entries
// it is given, as given: an entry the loader ignores stays as the owner
// wrote it, and the caller (Settings > Git) validates every entry it adds.
// Comments inside the git block are not kept.
func SaveGit(path string, g GitConfig) error {
	block, err := renderGitBlock(g)
	if err != nil {
		return err
	}
	_, err = saveTopLevelBlock(path, "git", block, "Git settings", "", nil, func(got *Config) error {
		if got.Git == nil || !gitReposEqual(got.Git.Repos, g.Repos) {
			return errors.New("the written git block does not read back as intended (file unchanged)")
		}
		return nil
	})
	return err
}

func renderGitBlock(g GitConfig) (string, error) {
	repos := make([]gitRepoYAML, 0, len(g.Repos))
	for _, r := range g.Repos {
		repos = append(repos, gitRepoYAML(r))
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(gitYAML{Repos: repos}); err != nil {
		return "", fmt.Errorf("render git block: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render git block: %w", err)
	}
	var b strings.Builder
	b.WriteString("git:\n")
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String(), nil
}

func gitReposEqual(a, b []GitRepoConfig) bool {
	norm := func(rs []GitRepoConfig) []GitRepoConfig {
		out := make([]GitRepoConfig, 0, len(rs))
		for _, r := range rs {
			if len(r.Matches) == 0 {
				r.Matches = nil
			}
			out = append(out, r)
		}
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

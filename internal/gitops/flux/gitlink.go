package flux

import (
	"github.com/moomora/klyx/internal/urlscrub"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GitLink is a resolved navigation target for a Flux source. IsDeepLink is true
// when URL is a browsable web URL (GitLab/GitHub); otherwise CopyText holds a
// "<remote> <path>@<revision>" reference for the clipboard.
type GitLink struct {
	URL        string `json:"url"`
	IsDeepLink bool   `json:"isDeepLink"`
	CopyText   string `json:"copyText"`
}

// ResolveGitLink turns a GitRepository remote + a Kustomization path + applied
// revision into a browsable web URL for known hosts, degrading to a copyable
// reference for unrecognised hosts or unparseable remotes.
func ResolveGitLink(remote, path, revision string) GitLink {
	// coverage S0b §3: a remote's user info never reaches a link or a copy
	remote = urlscrub.Field(strings.TrimSpace(remote)).Text
	copyText := remote
	if cp := cleanPath(path); cp != "" {
		copyText += " " + cp
	}
	if revision != "" {
		copyText += "@" + revision
	}

	host, repoPath, ok := normalizeRemote(remote)
	if !ok {
		return GitLink{IsDeepLink: false, CopyText: copyText}
	}
	ref := refFromRevision(revision)

	var url string
	switch {
	case strings.Contains(host, "gitlab"):
		url = "https://" + host + "/" + repoPath + "/-/tree/" + ref
	case strings.Contains(host, "github"):
		url = "https://" + host + "/" + repoPath + "/tree/" + ref
	default:
		return GitLink{IsDeepLink: false, CopyText: copyText}
	}
	if p := cleanPath(path); p != "" {
		url += "/" + p
	}
	return GitLink{URL: url, IsDeepLink: true, CopyText: copyText}
}

// refFromRevision extracts the branch/tag/sha web hosts expect in a tree URL
// from Flux's lastAppliedRevision. Handles both the old `main@sha1:...` form and
// the newer fully-qualified `refs/heads/main@sha1:...` / `refs/tags/v1@...` form,
// stripping the ref-namespace prefix that a web tree endpoint does not accept.
func refFromRevision(rev string) string {
	if rev == "" {
		return "HEAD"
	}
	if i := strings.Index(rev, "@"); i >= 0 {
		rev = rev[:i]
	}
	rev = strings.TrimPrefix(rev, "refs/heads/")
	rev = strings.TrimPrefix(rev, "refs/tags/")
	return rev
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	return strings.Trim(p, "/")
}

// normalizeRemote extracts (host, "org/repo") from https/ssh/scp git remotes,
// stripping any scheme, userinfo, port, and a trailing ".git".
func normalizeRemote(remote string) (host, repoPath string, ok bool) {
	r := strings.TrimSpace(remote)
	if r == "" {
		return "", "", false
	}
	r = strings.TrimSuffix(r, ".git")

	if strings.Contains(r, "://") {
		// scheme://[user@]host[:port]/org/repo
		r = r[strings.Index(r, "://")+3:]
		if at := strings.Index(r, "@"); at >= 0 {
			r = r[at+1:]
		}
		slash := strings.Index(r, "/")
		if slash < 0 {
			return "", "", false
		}
		host, repoPath = r[:slash], strings.Trim(r[slash+1:], "/")
		if c := strings.Index(host, ":"); c >= 0 { // strip port
			host = host[:c]
		}
	} else {
		// scp-style: [user@]host:org/repo
		if at := strings.Index(r, "@"); at >= 0 {
			r = r[at+1:]
		}
		colon := strings.Index(r, ":")
		if colon < 0 {
			return "", "", false
		}
		host, repoPath = r[:colon], strings.Trim(r[colon+1:], "/")
	}
	if host == "" || repoPath == "" {
		return "", "", false
	}
	return host, repoPath, true
}

// KustomizationSource is the source pointer + path + applied revision needed to
// build a Git link. SourceNamespace defaults to the Kustomization's namespace.
type KustomizationSource struct {
	SourceKind      string
	SourceName      string
	SourceNamespace string
	Path            string
	Revision        string
}

// ParseKustomizationSource extracts the source pointer, path, and applied
// revision from a Kustomization unstructured. Pure.
func ParseKustomizationSource(u *unstructured.Unstructured) KustomizationSource {
	var s KustomizationSource
	s.SourceKind, _, _ = unstructured.NestedString(u.Object, "spec", "sourceRef", "kind")
	s.SourceName, _, _ = unstructured.NestedString(u.Object, "spec", "sourceRef", "name")
	s.SourceNamespace, _, _ = unstructured.NestedString(u.Object, "spec", "sourceRef", "namespace")
	if s.SourceNamespace == "" {
		s.SourceNamespace = u.GetNamespace()
	}
	s.Path, _, _ = unstructured.NestedString(u.Object, "spec", "path")
	s.Revision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	return s
}

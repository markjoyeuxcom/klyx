package line

import (
	"testing"

	"github.com/moomora/klyx/internal/gitops/flux"
)

func cov(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

var usable = Availability{Usable: true}

func TestResolveReadTruthTable(t *testing.T) {
	fluxOnly := cov("kustomizations", "live", "helmreleases", "live", "applications", "absent", "gitrepositories", "live", "buckets", "unreadable")
	gitUnit := StationInput{Kustomizations: []Object[flux.Resource]{{Data: flux.Resource{SourceKind: "GitRepository"}}}}
	cases := []struct {
		name string
		in   StationInput
		cov  map[string]string
		av   Availability
		want string
	}{
		{"no GitOps at all", StationInput{}, cov("kustomizations", "absent", "helmreleases", "absent", "applications", "absent"), usable, ReadOK},
		{"Argo only", StationInput{}, cov("kustomizations", "absent", "helmreleases", "absent", "applications", "live"), usable, ReadOK},
		{"Flux only; an unrelated Bucket kind unreadable", gitUnit, fluxOnly, usable, ReadOK},
		{"a needed source kind stale", gitUnit, cov("kustomizations", "live", "helmreleases", "live", "applications", "absent", "gitrepositories", "stale"), usable, ReadPartial},
		{"a unit kind unreadable: every Flux source kind is needed", StationInput{}, cov("kustomizations", "unreadable", "helmreleases", "live", "applications", "absent",
			"gitrepositories", "live", "helmrepositories", "live", "ocirepositories", "live", "buckets", "live", "helmcharts", "live"), usable, ReadPartial},
		{"nothing live: the worst", StationInput{}, cov("kustomizations", "stale", "helmreleases", "unreadable", "applications", "checking"), usable, ReadError},
		{"still checking", StationInput{}, cov("kustomizations", "checking", "helmreleases", "checking", "applications", "checking"), usable, ReadChecking},
		{"connection unusable", StationInput{}, cov("kustomizations", "live", "helmreleases", "live", "applications", "live"), Availability{}, ReadChecking},
		{"gapped", StationInput{}, cov("kustomizations", "live", "helmreleases", "live", "applications", "live"), Availability{Usable: true, Gapped: true}, ReadChecking},
		{"attaching", StationInput{}, cov(), Availability{Usable: true, Attaching: true}, ReadChecking},
		{"a missing kind reads checking", StationInput{}, cov("kustomizations", "live", "helmreleases", "live"), usable, ReadPartial},
	}
	for _, c := range cases {
		in := c.in
		if got := ResolveRead(&in, c.cov, c.av); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

package appbridge

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/fleet"
)

// Settings S3a §6: Detect, per group or per cluster, proposes an
// installation for each cluster it proves one in; it never proves one
// shared. The person reviews and edits a proposal; saving it is an
// ordinary plan.

// DetectScopeDTO is what Detect runs over: a group, or one cluster.
type DetectScopeDTO struct {
	Group   string `json:"group,omitempty"`
	Cluster string `json:"cluster,omitempty"`
}

// DetectClusterDTO is Detect's outcome in one cluster: found, none,
// ambiguous or unreadable.
type DetectClusterDTO struct {
	Cluster string `json:"cluster"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// DetectProposalDTO is one proposed installation, with what its sample
// could and could not show, and the draft its plan starts from.
type DetectProposalDTO struct {
	Host        string          `json:"host"`
	Where       string          `json:"where"`
	SampledAt   string          `json:"sampledAt,omitempty"`
	Sample      string          `json:"sample"`
	MemberLabel string          `json:"memberLabel,omitempty"`
	Candidates  []string        `json:"candidates,omitempty"`
	MembersSeen []string        `json:"membersSeen,omitempty"`
	Notes       []string        `json:"notes"`
	Draft       SourcesDraftDTO `json:"draft"`
}

// DetectDTO is a Detect run's outcome.
type DetectDTO struct {
	Clusters  []DetectClusterDTO  `json:"clusters"`
	Proposals []DetectProposalDTO `json:"proposals"`
	Error     string              `json:"error,omitempty"`
}

// notSeenNote is what a sample's absence of a member never means.
const notSeenNote = "A cluster not seen in this sample may simply have nothing firing: Detect does not say it has no alerts, or that another installation serves it."

// Detect runs over a group or one running cluster, one member at a time
// (S3a §6), only when the person asks.
func (s *SourcesService) Detect(scope DetectScopeDTO) DetectDTO {
	out := DetectDTO{Clusters: []DetectClusterDTO{}, Proposals: []DetectProposalDTO{}}
	if s.d.Detect == nil {
		out.Error = "Detect is not available"
		return out
	}
	running := s.cs.fleetClusters()
	var members, names []string
	for _, cl := range running {
		names = append(names, cl.Name)
		if scope.Cluster != "" && cl.Name == scope.Cluster || scope.Cluster == "" && scope.Group != "" && cl.Group == scope.Group {
			members = append(members, cl.Name)
		}
	}
	if len(members) == 0 {
		out.Error = "no running cluster is in that group or has that name"
		return out
	}
	taken := map[string]bool{}
	if a := s.d.Active(); a != nil {
		for _, src := range a.Sources() {
			taken[src.ID] = true
		}
	}
	for _, m := range members {
		ctx, cancel := context.WithTimeout(context.Background(), detectTimeout)
		smp := s.d.Detect(ctx, m, names)
		cancel()
		row := DetectClusterDTO{Cluster: m}
		switch {
		case smp.Err != "":
			row.Outcome, row.Detail = "unreadable", smp.Err
		case smp.Ambiguous != "":
			row.Outcome, row.Detail = "ambiguous", smp.Ambiguous
		case smp.None:
			row.Outcome, row.Detail = "none", "no Alertmanager was found in "+m
		default:
			row.Outcome, row.Detail = "found", fmt.Sprintf("Service %s/%s:%s, %s", smp.Namespace, smp.Service, smp.Port, smp.Via)
			out.Proposals = append(out.Proposals, proposalOf(smp, taken))
		}
		out.Clusters = append(out.Clusters, row)
	}
	return out
}

// detectTimeout bounds one cluster's discovery and sample.
const detectTimeout = 20 * time.Second

// proposalOf is the installation a sample proposes: hosted by the cluster
// it was proven in, at the Service discovery proved, with a member label
// when exactly one label's values named fleet clusters.
func proposalOf(smp fleet.DetectSample, taken map[string]bool) DetectProposalDTO {
	p := DetectProposalDTO{Host: smp.Cluster, Where: fmt.Sprintf("Service %s/%s:%s in %s", smp.Namespace, smp.Service, smp.Port, smp.Cluster), Notes: []string{}}
	if !smp.At.IsZero() {
		p.SampledAt = smp.At.UTC().Format(time.RFC3339)
	}
	switch {
	case smp.Unread != "":
		p.Sample = "not read: " + smp.Unread
		p.Notes = append(p.Notes, "Without a sample, Detect proposes no member label: choose one, or a match for each subscription.")
	case smp.Truncated:
		p.Sample = fmt.Sprintf("the first %d alerts, at most 1 MiB: the sample was cut at its bounds", smp.Alerts)
	default:
		p.Sample = fmt.Sprintf("%s, at most 500 alerts and 1 MiB", map[bool]string{true: "1 alert", false: strconv.Itoa(smp.Alerts) + " alerts"}[smp.Alerts == 1])
	}
	for label := range smp.Members {
		p.Candidates = append(p.Candidates, label)
	}
	sort.Strings(p.Candidates)
	seen := map[string]bool{}
	switch len(p.Candidates) {
	case 0:
		if smp.Unread == "" {
			p.Notes = append(p.Notes, "No label in the sample named a fleet cluster: choose a member label, or a match for each subscription.")
		}
	case 1:
		p.MemberLabel = p.Candidates[0]
		for _, v := range smp.Members[p.MemberLabel] {
			seen[v] = true
		}
	default:
		p.Notes = append(p.Notes, "Several labels named fleet clusters ("+strings.Join(p.Candidates, ", ")+"): choose the member label.")
		for _, vs := range smp.Members {
			for _, v := range vs {
				seen[v] = true
			}
		}
	}
	for v := range seen {
		p.MembersSeen = append(p.MembersSeen, v)
	}
	sort.Strings(p.MembersSeen)
	p.Notes = append(p.Notes, notSeenNote)
	draft := SourceDraftDTO{ID: freeID("am-"+smp.Cluster, taken), Host: smp.Cluster, MemberLabel: p.MemberLabel,
		ServiceRef: &ServiceRefDTO{Namespace: smp.Namespace, Name: smp.Service, Port: smp.Port, Path: smp.Path}}
	if p.MemberLabel != "" && len(p.MembersSeen) > 0 {
		draft.ServesClusters = append([]string(nil), p.MembersSeen...)
	} else {
		draft.ServesClusters = []string{smp.Cluster}
	}
	taken[draft.ID] = true
	p.Draft = SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: draft}}
	return p
}

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// freeID is an installation id from a name: a DNS label, unused among
// taken, with a numeric suffix when it is.
func freeID(name string, taken map[string]bool) string {
	id := strings.Trim(notLabel.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if id == "" {
		id = "am"
	}
	if len(id) > 56 {
		id = strings.TrimRight(id[:56], "-")
	}
	if !taken[id] {
		return id
	}
	for i := 2; ; i++ {
		if c := id + "-" + strconv.Itoa(i); !taken[c] {
			return c
		}
	}
}

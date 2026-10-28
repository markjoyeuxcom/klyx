package appbridge

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
)

// Settings S3a §6, S3b §6: Detect, per group or per cluster, proposes an
// installation for each cluster it proves one in; it never proves one
// shared. The person reviews and edits a proposal; saving it is an
// ordinary plan. Alertmanager and metrics are detected; Loki never is.

// DetectScopeDTO is what Detect runs over: a group, or one cluster, for a
// kind (none is Alertmanager).
type DetectScopeDTO struct {
	Kind    string `json:"kind,omitempty"`
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

// The metrics sample's qualifications (S3b §6): label metadata for a
// requested hour, whose bounds are approximate.
const (
	metadataNote      = "This is label metadata for the hour asked for, not proof that a cluster produced metrics in it: Prometheus's metadata bounds are approximate, and can include series without samples."
	metricsUnseenNote = "A cluster not seen in this sample proves no absence: Detect does not say it has no metrics, or that another installation serves it."
)

// Detect runs over a group or one running cluster, one member at a time
// (S3a §6, S3b §6), only when the person asks.
func (s *SourcesService) Detect(scope DetectScopeDTO) DetectDTO {
	out := DetectDTO{Clusters: []DetectClusterDTO{}, Proposals: []DetectProposalDTO{}}
	kind, err := kindOf(scope.Kind)
	switch {
	case err != nil:
		out.Error = err.Error()
		return out
	case kind == config.EndpointLoki:
		out.Error = "Loki is never discovered: write its installation from where it runs"
		return out
	case kind == config.EndpointMetrics && s.d.DetectMetrics == nil, kind == config.EndpointAlertmanager && s.d.Detect == nil:
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
		if kind == config.EndpointMetrics {
			// its own 30 s deadline, the wait for its pool included
			d := s.d.DetectMetrics(context.Background(), m, names)
			row := metricsRow(d)
			if row.Outcome == "found" {
				out.Proposals = append(out.Proposals, metricsProposalOf(d, taken))
			}
			out.Clusters = append(out.Clusters, row)
			continue
		}
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
	p.memberLabel(smp.Members, smp.Unread == "")
	p.Notes = append(p.Notes, notSeenNote)
	p.draft(config.EndpointAlertmanager, freeID("am-"+smp.Cluster, taken), smp.Cluster, &ServiceRefDTO{Namespace: smp.Namespace, Name: smp.Service, Port: smp.Port, Path: smp.Path}, taken)
	return p
}

// memberLabel proposes the member label: the one label whose values named
// fleet clusters, with the clusters seen; several are named for the person
// to choose among, and none is said when a sample was read.
func (p *DetectProposalDTO) memberLabel(members map[string][]string, read bool) {
	for label := range members {
		p.Candidates = append(p.Candidates, label)
	}
	sort.Strings(p.Candidates)
	seen := map[string]bool{}
	switch len(p.Candidates) {
	case 0:
		if read {
			p.Notes = append(p.Notes, "No label in the sample named a fleet cluster: choose a member label, or a match for each subscription.")
		}
	case 1:
		p.MemberLabel = p.Candidates[0]
		for _, v := range members[p.MemberLabel] {
			seen[v] = true
		}
	default:
		p.Notes = append(p.Notes, "Several labels named fleet clusters ("+strings.Join(p.Candidates, ", ")+"): choose the member label.")
		for _, vs := range members {
			for _, v := range vs {
				seen[v] = true
			}
		}
	}
	for v := range seen {
		p.MembersSeen = append(p.MembersSeen, v)
	}
	sort.Strings(p.MembersSeen)
}

// draft is the proposal's installation of a kind, hosted by the cluster
// it was found in, serving the members seen under its member label, or
// that cluster.
func (p *DetectProposalDTO) draft(kind, id, host string, ref *ServiceRefDTO, taken map[string]bool) {
	d := SourceDraftDTO{ID: id, Host: host, MemberLabel: p.MemberLabel, ServiceRef: ref}
	if p.MemberLabel != "" && len(p.MembersSeen) > 0 {
		d.ServesClusters = append([]string(nil), p.MembersSeen...)
	} else {
		d.ServesClusters = []string{host}
	}
	taken[id] = true
	p.Draft = SourcesDraftDTO{Installation: &InstallationEditDTO{Op: installAdd, Source: d}}
	if kind != config.EndpointAlertmanager {
		p.Draft.Kind = kind
	}
}

// metricsRow is Detect for metrics' outcome in one member: one Service
// found, none, several (each named), or a discovery that could not be read
// whole (what it found so far named, never taken as all).
func metricsRow(d fleet.MetricsDetect) DetectClusterDTO {
	row := DetectClusterDTO{Cluster: d.Cluster}
	var found []string
	for _, c := range d.Candidates {
		found = append(found, candidateWords(c))
	}
	switch {
	case d.Err != "":
		row.Outcome, row.Detail = "unreadable", d.Err
	case len(d.Unreadable) > 0:
		row.Outcome, row.Detail = "unreadable", strings.Join(d.Unreadable, "; ")
		if len(found) > 0 {
			row.Detail += "; found so far, which may not be all: " + strings.Join(found, ", ")
		}
	case len(found) == 0:
		row.Outcome, row.Detail = "none", "no Prometheus Service was found in "+d.Cluster
	case len(found) > 1:
		row.Outcome, row.Detail = "ambiguous", "several Prometheus Services: "+strings.Join(found, ", ")+"; write the installation from the one you mean"
	default:
		row.Outcome, row.Detail = "found", found[0]
	}
	return row
}

// candidateWords is a Service Detect found, and how.
func candidateWords(c fleet.MetricsCandidate) string {
	how := []string{}
	if c.ByName {
		how = append(how, "its name")
	}
	if c.BySelector != "" {
		how = append(how, "the selector "+c.BySelector)
	}
	w := fmt.Sprintf("Service %s/%s:%s", c.Namespace, c.Name, c.Port)
	if c.Path != "" {
		w += c.Path
	}
	return w + " (found by " + strings.Join(how, " and ") + ")"
}

// metricsProposalOf is the metrics installation a sample proposes: hosted
// by the cluster it was found in, at the one Service found, with a member
// label when exactly one label's values named fleet clusters, and what the
// sample asked for, cut, failed or left out.
func metricsProposalOf(d fleet.MetricsDetect, taken map[string]bool) DetectProposalDTO {
	c := d.Candidates[0]
	p := DetectProposalDTO{Host: d.Cluster, Where: fmt.Sprintf("Service %s/%s:%s in %s", c.Namespace, c.Name, c.Port, d.Cluster), Notes: []string{}}
	if !d.At.IsZero() {
		p.SampledAt = d.At.UTC().Format(time.RFC3339)
	}
	s := d.Sample
	if s == nil {
		p.Sample = "not read: " + d.Unread
		p.Notes = append(p.Notes, "Without a sample, Detect proposes no member label: choose one, or a match for each subscription.")
	} else {
		p.Sample = fmt.Sprintf("label metadata for the hour to %s: %d label names, the values of %d labels, at most 2,000 values and 1 MiB each", s.End.UTC().Format(time.RFC3339), len(s.Names), len(s.Queried))
		if s.NamesCut {
			p.Notes = append(p.Notes, "The label names were cut at 2,000 values or 1 MiB: the names past them were not examined.")
		}
		if len(s.Cut) > 0 {
			p.Notes = append(p.Notes, "The values of "+strings.Join(s.Cut, ", ")+" were cut at 2,000 or 1 MiB: a fleet cluster's name past them was not examined.")
		}
		if len(s.Failed) > 0 {
			p.Notes = append(p.Notes, "The values of "+strings.Join(s.Failed, ", ")+" could not be read.")
		}
		if s.NotQueried > 0 {
			p.Notes = append(p.Notes, fmt.Sprintf("The values of %d more labels were not asked for: a sample reads eight, the member labels first.", s.NotQueried))
		}
		p.Notes = append(p.Notes, metadataNote)
	}
	p.memberLabel(memberValues(s), s != nil)
	p.Notes = append(p.Notes, metricsUnseenNote)
	ref := &ServiceRefDTO{Namespace: c.Namespace, Name: c.Name, Port: c.Port, Path: c.Path}
	if c.Scheme != "" && c.Scheme != "http" {
		ref.Scheme = c.Scheme
	}
	p.draft(config.EndpointMetrics, freeID("metrics-"+d.Cluster, taken), d.Cluster, ref, taken)
	return p
}

func memberValues(s *metrics.MetadataSample) map[string][]string {
	if s == nil {
		return nil
	}
	return s.Members
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

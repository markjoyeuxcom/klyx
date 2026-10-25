package appbridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	wid "github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Coverage S9's on-demand inspector reads and its verb: the handling
// operator and the authentication evidence (§6), a ScaledJob's Jobs (§6.4),
// who owns a workload's replica count (§7), and Pause/Resume (§8).

// KEDAConn is what the service needs of a connection.
type KEDAConn interface {
	KEDAInspect(ctx context.Context, resource, ns, name, uid string) (fleet.KEDAInspection, error)
	KEDAJobsOf(ctx context.Context, ns, name, uid string) (fleet.KEDAJobs, error)
	ReplicaOwner(ctx context.Context, kind, ns, name string) fleet.ReplicaOwnership
	KEDASetPaused(ctx context.Context, resource, ns, name, uid string, pause bool) error
}

// KEDAService serves the KEDA inspectors and the scale verb's owner line.
type KEDAService struct {
	lookup func(string) (KEDAConn, bool)
}

// NewKEDAService wires the connection lookup.
func NewKEDAService(lookup func(string) (KEDAConn, bool)) *KEDAService {
	return &KEDAService{lookup: lookup}
}

const kedaCallTimeout = 20 * time.Second

// KEDARefCheckDTO is one referenced object's existence (distinct from
// the rows' KEDARefDTO).
type KEDARefCheckDTO struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	State     string `json:"state"`
	Why       string `json:"why,omitempty"`
}

// KEDASubjectDTO is who an authentication's podIdentity authenticates as.
type KEDASubjectDTO struct {
	Provider   string   `json:"provider"`
	Mode       string   `json:"mode"`
	Base       string   `json:"base"`
	SA         string   `json:"serviceAccount,omitempty"`
	RoleFrom   string   `json:"roleFrom,omitempty"`
	Requested  []string `json:"requested"`
	Refusals   []string `json:"refusals"`
	Unresolved []string `json:"unresolved"`
	Notes      []string `json:"notes"`
}

// KEDAAuthCheckDTO is one referenced authentication's evidence.
type KEDAAuthCheckDTO struct {
	Kind    string            `json:"kind"`
	Name    string            `json:"name"`
	State   string            `json:"state"`
	Why     string            `json:"why,omitempty"`
	Visible map[string]string `json:"visible"`
	Refs    []KEDARefCheckDTO `json:"refs"`
	RefsCut bool              `json:"refsCut,omitempty"`
	// RefsOmitted counts the references not listed: not kept by the
	// projection, or dropped to fit the reply.
	RefsOmitted int             `json:"refsOmitted,omitempty"`
	Subject     *KEDASubjectDTO `json:"subject,omitempty"`
}

// KEDAInspectionDTO is the inspector's on-demand evidence for one scaler.
type KEDAInspectionDTO struct {
	UID           string             `json:"uid"`
	Handler       string             `json:"handler,omitempty"`
	None          bool               `json:"none,omitempty"`
	Candidates    []string           `json:"candidates"`
	CandidatesCut bool               `json:"candidatesCut,omitempty"`
	Version       string             `json:"version,omitempty"`
	Disagree      []string           `json:"disagree"`
	Auths         []KEDAAuthCheckDTO `json:"auths"`
	Error         string             `json:"error,omitempty"`
}

func saText(r *wid.SARef) string {
	if r == nil {
		return ""
	}
	return r.Namespace + "/" + r.Name
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Inspect reads one scaler's on-demand evidence, fenced by uid.
func (s *KEDAService) Inspect(cluster, resource, namespace, name, uid string) KEDAInspectionDTO {
	out := KEDAInspectionDTO{UID: uid, Candidates: []string{}, Disagree: []string{}, Auths: []KEDAAuthCheckDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), kedaCallTimeout)
	defer cancel()
	in, err := conn.KEDAInspect(ctx, resource, namespace, name, uid)
	if err != nil {
		out.Error = urlscrub.Text(err.Error()).Text
		return out
	}
	// the reply is bounded as it crosses (S9 §10; implementation review 1,
	// finding 8): the fleet's fit tightened until the DTO fits
	for limit := fleet.KEDAInspectionLimit; ; limit -= 8 << 10 {
		in.Fit(limit)
		dto := inspectionDTO(uid, in)
		if b, err := json.Marshal(dto); err != nil || len(b) <= fleet.KEDAInspectionLimit || limit <= 64<<10 {
			return dto
		}
	}
}

func inspectionDTO(uid string, in fleet.KEDAInspection) KEDAInspectionDTO {
	out := KEDAInspectionDTO{UID: uid, Candidates: orEmpty(in.Candidates), Disagree: orEmpty(in.Disagree), Auths: []KEDAAuthCheckDTO{}}
	out.Handler, out.None, out.CandidatesCut, out.Version = in.Handler, in.None, in.CandidatesCut, in.Version
	for _, a := range in.Auths {
		d := KEDAAuthCheckDTO{Kind: a.Kind, Name: a.Name, State: a.State, Why: a.Why, Visible: a.Visible, Refs: []KEDARefCheckDTO{}, RefsCut: a.RefsCut, RefsOmitted: a.RefsOmitted}
		for _, r := range a.Refs {
			d.Refs = append(d.Refs, KEDARefCheckDTO(r))
		}
		if sub := a.Subject; sub != nil {
			d.Subject = &KEDASubjectDTO{Provider: sub.Provider, Mode: sub.Mode, Base: sub.Base, SA: saText(sub.SA), RoleFrom: saText(sub.RoleFrom),
				Requested: orEmpty(sub.Requested), Refusals: orEmpty(sub.Refusals), Unresolved: orEmpty(sub.Unresolved), Notes: orEmpty(sub.Notes)}
		}
		out.Auths = append(out.Auths, d)
	}
	return out
}

// KEDAJobDTO is one Job a ScaledJob created.
type KEDAJobDTO struct {
	Name      string     `json:"name"`
	Created   *time.Time `json:"created,omitempty"`
	Succeeded int32      `json:"succeeded"`
	Failed    int32      `json:"failed"`
	Active    int32      `json:"active"`
	Complete  bool       `json:"complete"`
	FailedOut bool       `json:"failedOut"`
}

// KEDAJobsDTO is a ScaledJob's Jobs: global newest only when Complete.
type KEDAJobsDTO struct {
	Jobs      []KEDAJobDTO `json:"jobs"`
	Complete  bool         `json:"complete"`
	Read      int          `json:"read"`
	Owned     int          `json:"owned"`
	Remaining *int64       `json:"remaining,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// Jobs reads a ScaledJob's Jobs (paginated, owner-UID fenced).
func (s *KEDAService) Jobs(cluster, namespace, name, uid string) KEDAJobsDTO {
	out := KEDAJobsDTO{Jobs: []KEDAJobDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), kedaCallTimeout)
	defer cancel()
	js, err := conn.KEDAJobsOf(ctx, namespace, name, uid)
	if err != nil {
		out.Error = urlscrub.Text(err.Error()).Text
		return out
	}
	out.Complete, out.Read, out.Owned, out.Remaining = js.Complete, js.Read, js.Owned, js.Remaining
	for _, j := range js.Jobs {
		out.Jobs = append(out.Jobs, KEDAJobDTO{Name: j.Name, Created: timePtrOf(j.Created), Succeeded: j.Succeeded, Failed: j.Failed,
			Active: j.Active, Complete: j.Complete, FailedOut: j.FailedOut})
	}
	return out
}

// ReplicaOwnerDTO is who owns a workload's replica count, per axis
// ("owned", "unowned", "unverified").
type ReplicaOwnerDTO struct {
	Autoscaler    string   `json:"autoscaler"`
	AutoscalerWhy string   `json:"autoscalerWhy,omitempty"`
	Owners        []string `json:"owners"`
	GitOps        string   `json:"gitops"`
	GitOpsOwner   string   `json:"gitopsOwner,omitempty"`
	GitOpsWhy     string   `json:"gitopsWhy,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// ReplicaOwner says who owns a Deployment's or StatefulSet's replica count
// (S9 §7), for the scale verb's warning.
func (s *KEDAService) ReplicaOwner(cluster, kind, namespace, name string) ReplicaOwnerDTO {
	out := ReplicaOwnerDTO{Autoscaler: "unverified", GitOps: "unverified", Owners: []string{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), kedaCallTimeout)
	defer cancel()
	r := conn.ReplicaOwner(ctx, kind, namespace, name)
	out.Autoscaler, out.AutoscalerWhy, out.Owners = r.Autoscaler, r.AutoscalerWhy, orEmpty(r.Owners)
	out.GitOps, out.GitOpsOwner, out.GitOpsWhy = r.GitOps, r.GitOpsOwner, urlscrub.Text(r.GitOpsWhy).Text
	return out
}

// SetPaused pauses or resumes a ScaledObject or ScaledJob through
// autoscaling.keda.sh/paused (S9 §8). On a protected cluster it consumes
// the typed-confirmation grant first. It reports "requested"; only the
// object's status says the pause completed.
func (s *KEDAService) SetPaused(cluster, resource, namespace, name, uid string, pause bool) ActionResultDTO {
	t, err := guardFor(cluster)
	if err != nil {
		return refusedNotSent(err.Error())
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return notConnected(cluster)
	}
	ctx, cancel := context.WithTimeout(t.Write(context.Background()), actionTimeout)
	defer cancel()
	return doKEDASetPaused(ctx, conn, resource, namespace, name, uid, pause)
}

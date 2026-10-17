package appbridge

import (
	"context"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gitops/imageauto"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/urlscrub"
)

// ImageDetailDTO is an image object's on-demand detail (S1b §2): its
// events and, for an automation, the last push's evidence. It is fenced by
// the shown UID; a replacement is refused with its own UID.
type ImageDetailDTO struct {
	UID         string        `json:"uid"`
	Replaced    bool          `json:"replaced,omitempty"`
	Error       string        `json:"error,omitempty"`
	Events      []EventRowDTO `json:"events"`
	EventsError string        `json:"eventsError,omitempty"`
	Push        *ImagePushDTO `json:"push,omitempty"`
}

// ImagePushDTO is an automation's push evidence. Each line names its own
// evidence and compares with today's objects; none proves where the last
// push went.
type ImagePushDTO struct {
	// Branch: from an event bound by UID and hash (state bound, expired,
	// ambiguous or "no push").
	BranchState string `json:"branchState"`
	Branch      string `json:"branch,omitempty"`
	// Target is the push target as configured now.
	Target ImagePushTargetDTO `json:"target"`
	// Source is the GitRepository's artifact against the push.
	SourceState      string `json:"sourceState"`
	SourceLine       string `json:"sourceLine"`
	SourceRevision   string `json:"sourceRevision,omitempty"`
	SourceBranchNote string `json:"sourceBranchNote,omitempty"`
	// Applied are the Kustomizations bound to that GitRepository;
	// AppliedState is the read's completeness.
	Applied      []ImageAppliedDTO `json:"applied"`
	AppliedState string            `json:"appliedState"`
}

// ImagePushTargetDTO is where the next run would push.
type ImagePushTargetDTO struct {
	RepositoryURL string            `json:"repositoryURL,omitempty"`
	CheckoutFrom  string            `json:"checkoutFrom"`
	Checkout      *ImageCheckoutDTO `json:"checkout,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	Error         string            `json:"error,omitempty"`
	Refspec       string            `json:"refspec,omitempty"`
	OptionCount   int               `json:"optionCount,omitempty"`
}

// ImageAppliedDTO is one Kustomization's last applied revision.
type ImageAppliedDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  string `json:"revision,omitempty"`
	Applied   bool   `json:"applied"`
}

// GetImageDetail reads one image object for the inspector, fenced by uid.
func (s *GitOpsService) GetImageDetail(cluster, kind, namespace, name, uid string) ImageDetailDTO {
	if uid == "" {
		return ImageDetailDTO{Events: []EventRowDTO{}, Error: "no identity to read: the inspector names no UID"}
	}
	if !flux.IsImageKind(flux.Kind(kind)) {
		return ImageDetailDTO{Events: []EventRowDTO{}, Error: kind + " is not an image automation kind"}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ImageDetailDTO{Events: []EventRowDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	u, err := conn.FluxObject(ctx, kind, namespace, name)
	if err != nil {
		return ImageDetailDTO{Events: []EventRowDTO{}, Error: urlscrub.Text(err.Error()).Text}
	}
	if got := string(u.GetUID()); got != uid {
		return ImageDetailDTO{UID: got, Replaced: true, Events: []EventRowDTO{}, Error: kind + " " + namespace + "/" + name + " was replaced (its UID changed); nothing about the new object is shown here"}
	}
	d := ImageDetailDTO{UID: uid, Events: []EventRowDTO{}}
	var pushEvents []imageauto.PushEvent
	if evs, err := conn.FluxEvents(ctx, kind, namespace, name); err != nil {
		d.EventsError = urlscrub.Text(err.Error()).Text
	} else {
		for _, e := range evs {
			if e.UID != uid {
				continue
			}
			d.Events = append(d.Events, toEventRowDTO(e))
			pushEvents = append(pushEvents, imageauto.PushEvent{UID: e.UID, Message: e.Message})
		}
	}
	if kind == imageauto.KindAutomation {
		v, err := imageauto.Project(kind)(u)
		if err == nil {
			p := s.pushEvidence(ctx, conn, imageauto.ParseAutomation(v.(*imageauto.Object)), pushEvents, d.EventsError != "")
			d.Push = &p
		}
	}
	return d
}

// pushEvidence joins the automation's last push with its GitRepository and
// the Kustomizations bound to it, read now.
func (s *GitOpsService) pushEvidence(ctx context.Context, conn GitOpsConn, a imageauto.Automation, events []imageauto.PushEvent, eventsFailed bool) ImagePushDTO {
	bound := imageauto.BoundBranch(a, events)
	if eventsFailed && bound.State == imageauto.BranchExpired {
		bound.State = "unreadable" // a failed events read is not an expired event
	}
	out := ImagePushDTO{BranchState: bound.State, Branch: bound.Branch, Applied: []ImageAppliedDTO{}}
	src := imageauto.SourceInput{State: imageauto.JoinNotFound}
	var sourceRef *imageauto.CheckoutRef
	if a.SourceKind == "" || a.SourceKind == string(flux.GitRepositoryKind) {
		g, err := conn.FluxObject(ctx, string(flux.GitRepositoryKind), a.SourceNamespace, a.SourceName)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			src.State = imageauto.JoinUnreadable
		default:
			src.State = imageauto.JoinFound
			src.Revision, _, _ = unstructured.NestedString(g.Object, "status", "artifact", "revision")
			if ref, ok, _ := unstructured.NestedMap(g.Object, "spec", "ref"); ok {
				r := imageauto.CheckoutRef{}
				r.Branch, _, _ = unstructured.NestedString(ref, "branch")
				r.Tag, _, _ = unstructured.NestedString(ref, "tag")
				r.SemVer, _, _ = unstructured.NestedString(ref, "semver")
				r.Name, _, _ = unstructured.NestedString(ref, "name")
				r.Commit, _, _ = unstructured.NestedString(ref, "commit")
				sourceRef, src.Branch = &r, r.Branch
			}
			url, _, _ := unstructured.NestedString(g.Object, "spec", "url")
			out.Target.RepositoryURL = urlscrub.Field(url).Text
		}
	}
	t := imageauto.Target(a, sourceRef)
	out.Target.CheckoutFrom, out.Target.Checkout, out.Target.Branch, out.Target.Error = t.CheckoutFrom, checkoutDTO(t.Checkout), t.Branch, t.Error
	out.Target.Refspec, out.Target.OptionCount = t.Refspec, t.OptionCount
	line := imageauto.Source(a, src, bound)
	out.SourceState, out.SourceLine, out.SourceRevision, out.SourceBranchNote = src.State, line.Kind, line.Revision, line.BranchNote

	// the Kustomizations bound to that GitRepository, from the shared rows
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskFlux, "reconciliations")
	switch {
	case !ok || rows.Part.Coverage != observe.Live:
		out.AppliedState = imageauto.JoinUnreadable
	default:
		out.AppliedState = imageauto.JoinFound
		for _, k := range rows.Part.Kinds {
			if k.Kind == healthsrc.KindKustomizations && k.Truncated {
				out.AppliedState = imageauto.JoinIncomplete
			}
		}
		set, _ := rows.Rows.(healthsrc.RowSet[flux.Resource])
		var in []imageauto.AppliedInput
		for _, r := range set.Rows {
			k := r.Data
			if k.Kind == flux.KustomizationKind && k.SourceKind == string(flux.GitRepositoryKind) && k.SourceNamespace == a.SourceNamespace && k.SourceName == a.SourceName {
				in = append(in, imageauto.AppliedInput{Namespace: k.Namespace, Name: k.Name, Revision: k.Revision})
			}
		}
		for _, l := range imageauto.Applied(a, in) {
			out.Applied = append(out.Applied, ImageAppliedDTO{Namespace: l.Namespace, Name: l.Name, Revision: l.Revision, Applied: l.Applied})
		}
	}
	if !strings.EqualFold(a.SourceKind, "") && a.SourceKind != string(flux.GitRepositoryKind) {
		out.SourceState = "unsupported source kind " + a.SourceKind
	}
	return out
}

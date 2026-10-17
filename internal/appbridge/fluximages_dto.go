package appbridge

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/gitops/imageauto"
	"github.com/moomora/klyx/internal/healthsrc"
)

// Coverage S1b: the Images view's rows, from the Observer's shared "images"
// part, and the inspector's on-demand detail.

// ImageCommonDTO is what every image kind's row carries.
type ImageCommonDTO struct {
	UID                string          `json:"uid"`
	GVR                string          `json:"gvr"`
	Kind               string          `json:"kind"`
	Namespace          string          `json:"namespace"`
	Name               string          `json:"name"`
	Generation         int64           `json:"generation"`
	ObservedGeneration int64           `json:"observedGeneration,omitempty"`
	ReadyGeneration    int64           `json:"readyGeneration,omitempty"`
	Created            *time.Time      `json:"created,omitempty"`
	Suspended          bool            `json:"suspended"`
	Ready              string          `json:"ready"`
	Reason             string          `json:"reason,omitempty"`
	Message            string          `json:"message,omitempty"`
	ReadySince         *time.Time      `json:"readySince,omitempty"`
	Stalled            bool            `json:"stalled,omitempty"`
	RequestedAt        string          `json:"requestedAt,omitempty"`
	LastHandled        string          `json:"lastHandled,omitempty"`
	Owner              *FluxOpOwnerDTO `json:"owner,omitempty"`
	Clipped            []string        `json:"clipped"`
	Incomplete         []string        `json:"incomplete"`
	Rule               RuleDTO         `json:"rule"`
}

// ImageRefDTO is a resolved image; Text is name:tag@digest.
type ImageRefDTO struct {
	Name   string `json:"name"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest,omitempty"`
	Text   string `json:"text"`
}

// ImageRepositoryDTO is an ImageRepository row.
type ImageRepositoryDTO struct {
	ImageCommonDTO
	Image               string              `json:"image"`
	CanonicalImage      string              `json:"canonicalImage,omitempty"`
	Provider            string              `json:"provider,omitempty"`
	ServiceAccountName  string              `json:"serviceAccountName,omitempty"`
	SecretRef           string              `json:"secretRef,omitempty"`
	CertSecretRef       string              `json:"certSecretRef,omitempty"`
	ProxySecretRef      string              `json:"proxySecretRef,omitempty"`
	Interval            string              `json:"interval,omitempty"`
	Timeout             string              `json:"timeout,omitempty"`
	Insecure            bool                `json:"insecure,omitempty"`
	ExclusionList       []string            `json:"exclusionList"`
	AccessFromSelectors int                 `json:"accessFromSelectors,omitempty"`
	Scanned             bool                `json:"scanned"`
	TagCount            int                 `json:"tagCount"`
	ScanTime            *time.Time          `json:"scanTime,omitempty"`
	LatestTags          []string            `json:"latestTags"`
	Policies            []ImagePolicyRefDTO `json:"policies"`
}

// ImagePolicyRefDTO names a policy with its verdict.
type ImagePolicyRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Role      string `json:"role"`
}

// ImageRepositoryLinkDTO is a policy's repository block.
type ImageRepositoryLinkDTO struct {
	State     string     `json:"state"`
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Label     string     `json:"label,omitempty"`
	Role      string     `json:"role,omitempty"`
	ScanTime  *time.Time `json:"scanTime,omitempty"`
}

// ImageAutomationUseDTO is an automation that uses a policy.
type ImageAutomationUseDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
}

// ImagePolicyDTO is an ImagePolicy row.
type ImagePolicyDTO struct {
	ImageCommonDTO
	RepositoryNamespace string                  `json:"repositoryNamespace"`
	RepositoryName      string                  `json:"repositoryName"`
	PolicyType          string                  `json:"policyType,omitempty"`
	PolicyValue         string                  `json:"policyValue,omitempty"`
	FilterPattern       string                  `json:"filterPattern,omitempty"`
	FilterExtract       string                  `json:"filterExtract,omitempty"`
	DigestReflection    string                  `json:"digestReflection,omitempty"`
	Interval            string                  `json:"interval,omitempty"`
	Latest              *ImageRefDTO            `json:"latest,omitempty"`
	Previous            *ImageRefDTO            `json:"previous,omitempty"`
	BlockedBy           string                  `json:"blockedBy,omitempty"`
	Repository          *ImageRepositoryLinkDTO `json:"repository,omitempty"`
	Automations         []ImageAutomationUseDTO `json:"automations"`
}

// ImagePolicyUseDTO is a policy an automation uses now.
type ImagePolicyUseDTO struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Resolved  ImageRefDTO  `json:"resolved"`
	Observed  *ImageRefDTO `json:"observed,omitempty"`
	State     string       `json:"state"`
}

// ImageCheckoutDTO is a Git reference.
type ImageCheckoutDTO struct {
	Branch string `json:"branch,omitempty"`
	Tag    string `json:"tag,omitempty"`
	SemVer string `json:"semver,omitempty"`
	Name   string `json:"name,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// ImageAutomationDTO is an ImageUpdateAutomation row. The author's email
// is shown to the person only; it is never an AI fact (S1b §6).
type ImageAutomationDTO struct {
	ImageCommonDTO
	SourceKind             string              `json:"sourceKind,omitempty"`
	SourceName             string              `json:"sourceName,omitempty"`
	SourceNamespace        string              `json:"sourceNamespace,omitempty"`
	Interval               string              `json:"interval,omitempty"`
	Checkout               *ImageCheckoutDTO   `json:"checkout,omitempty"`
	PushBranch             string              `json:"pushBranch,omitempty"`
	PushRefspec            string              `json:"pushRefspec,omitempty"`
	PushOptionCount        int                 `json:"pushOptionCount,omitempty"`
	AuthorName             string              `json:"authorName,omitempty"`
	AuthorEmail            string              `json:"authorEmail,omitempty"`
	SigningKeySecret       string              `json:"signingKeySecret,omitempty"`
	UpdateStrategy         string              `json:"updateStrategy,omitempty"`
	UpdatePath             string              `json:"updatePath,omitempty"`
	Selector               string              `json:"selector,omitempty"`
	SelectorIncomplete     bool                `json:"selectorIncomplete,omitempty"`
	LastRun                *time.Time          `json:"lastRun,omitempty"`
	LastPushTime           *time.Time          `json:"lastPushTime,omitempty"`
	LastPushCommit         string              `json:"lastPushCommit,omitempty"`
	ObservedSourceRevision string              `json:"observedSourceRevision,omitempty"`
	ObservedComplete       bool                `json:"observedComplete"`
	Uses                   []ImagePolicyUseDTO `json:"uses"`
	UsesState              string              `json:"usesState"`
}

// ImageRowDTO is one row of the Images view: exactly one kind is set.
type ImageRowDTO struct {
	UID        string              `json:"uid"`
	Kind       string              `json:"kind"`
	Rule       RuleDTO             `json:"rule"`
	Repository *ImageRepositoryDTO `json:"repository,omitempty"`
	Policy     *ImagePolicyDTO     `json:"policy,omitempty"`
	Automation *ImageAutomationDTO `json:"automation,omitempty"`
}

func imageCommon(c imageauto.Common, ref healthsrc.RowRef, eval RuleDTO) ImageCommonDTO {
	d := ImageCommonDTO{UID: ref.UID, GVR: ref.GVR, Generation: ref.Generation, Kind: c.Kind, Namespace: c.Namespace, Name: c.Name,
		ObservedGeneration: c.ObservedGeneration, ReadyGeneration: c.ReadyGeneration, Created: timePtrOf(c.Created), Suspended: c.Suspended,
		Ready: string(c.Ready), Reason: c.Reason, Message: c.Message, ReadySince: timePtrOf(c.ReadySince), Stalled: c.Stalled,
		RequestedAt: c.RequestedAt, LastHandled: c.LastHandled, Clipped: nonNil(c.Clipped), Incomplete: nonNil(c.Incomplete), Rule: eval}
	if c.Owner != nil {
		d.Owner = &FluxOpOwnerDTO{Kind: c.Owner.Kind, Namespace: c.Owner.Namespace, Name: c.Owner.Name}
	}
	return d
}

func imageRef(r *imageauto.ImageRef) *ImageRefDTO {
	if r == nil {
		return nil
	}
	return &ImageRefDTO{Name: r.Name, Tag: r.Tag, Digest: r.Digest, Text: r.String()}
}

func checkoutDTO(r *imageauto.CheckoutRef) *ImageCheckoutDTO {
	if r == nil {
		return nil
	}
	return &ImageCheckoutDTO{Branch: r.Branch, Tag: r.Tag, SemVer: r.SemVer, Name: r.Name, Commit: r.Commit}
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func imageRowDTO(r healthsrc.Row[healthsrc.ImageRow]) ImageRowDTO {
	eval := toRuleDTO(r.Eval)
	out := ImageRowDTO{UID: r.Ref.UID, Rule: eval}
	switch {
	case r.Data.Repository != nil:
		x := r.Data.Repository
		d := ImageRepositoryDTO{ImageCommonDTO: imageCommon(x.Common, r.Ref, eval), Image: x.Image, CanonicalImage: x.CanonicalImage,
			Provider: x.Provider, ServiceAccountName: x.ServiceAccountName, SecretRef: x.SecretRef, CertSecretRef: x.CertSecretRef,
			ProxySecretRef: x.ProxySecretRef, Interval: x.IntervalText, Timeout: x.Timeout, Insecure: x.Insecure,
			ExclusionList: nonNil(x.ExclusionList), AccessFromSelectors: x.AccessFromSelectors, Scanned: x.Scanned, TagCount: x.TagCount,
			ScanTime: timePtrOf(x.ScanTime), LatestTags: nonNil(x.LatestTags), Policies: []ImagePolicyRefDTO{}}
		for _, p := range r.Data.Policies {
			d.Policies = append(d.Policies, ImagePolicyRefDTO{Namespace: p.Namespace, Name: p.Name, Label: p.Label, Role: p.Role})
		}
		out.Kind, out.Repository = imageauto.KindRepository, &d
	case r.Data.Policy != nil:
		x := r.Data.Policy
		d := ImagePolicyDTO{ImageCommonDTO: imageCommon(x.Common, r.Ref, eval), RepositoryNamespace: x.RepositoryNamespace,
			RepositoryName: x.RepositoryName, PolicyType: x.PolicyType, PolicyValue: x.PolicyValue, FilterPattern: x.FilterPattern,
			FilterExtract: x.FilterExtract, DigestReflection: x.DigestReflection, Interval: x.Interval, Latest: imageRef(x.Latest),
			Previous: imageRef(x.Previous), BlockedBy: r.Data.BlockedBy, Automations: []ImageAutomationUseDTO{}}
		if l := r.Data.RepositoryLink; l != nil {
			d.Repository = &ImageRepositoryLinkDTO{State: l.State, Namespace: l.Namespace, Name: l.Name, Label: l.Label, Role: l.Role, ScanTime: parseTime(l.ScanTime)}
		}
		for _, a := range r.Data.Automations {
			d.Automations = append(d.Automations, ImageAutomationUseDTO{Namespace: a.Namespace, Name: a.Name, State: a.State})
		}
		out.Kind, out.Policy = imageauto.KindPolicy, &d
	case r.Data.Automation != nil:
		x := r.Data.Automation
		d := ImageAutomationDTO{ImageCommonDTO: imageCommon(x.Common, r.Ref, eval), SourceKind: x.SourceKind, SourceName: x.SourceName,
			SourceNamespace: x.SourceNamespace, Interval: x.IntervalText, Checkout: checkoutDTO(x.Checkout), PushBranch: x.PushBranch,
			PushRefspec: x.PushRefspec, PushOptionCount: x.PushOptionCount, AuthorName: x.AuthorName, AuthorEmail: x.AuthorEmail,
			SigningKeySecret: x.SigningKeySecret, UpdateStrategy: x.UpdateStrategy, UpdatePath: x.UpdatePath,
			SelectorIncomplete: x.SelectorIncomplete, LastRun: timePtrOf(x.LastRun), LastPushTime: timePtrOf(x.LastPushTime),
			LastPushCommit: x.LastPushCommit, ObservedSourceRevision: x.ObservedSourceRevision, ObservedComplete: x.ObservedComplete,
			Uses: []ImagePolicyUseDTO{}, UsesState: r.Data.UsesState}
		switch {
		case x.Selector != nil:
			d.Selector = metav1.FormatLabelSelector(x.Selector)
		case !x.HasSelector:
			d.Selector = "every policy in its namespace"
		}
		for _, u := range r.Data.Uses {
			res := u.Resolved
			d.Uses = append(d.Uses, ImagePolicyUseDTO{Namespace: u.Namespace, Name: u.Name, Resolved: *imageRef(&res), Observed: imageRef(u.Observed), State: u.State})
		}
		out.Kind, out.Automation = imageauto.KindAutomation, &d
	}
	return out
}

func init() {
	rowsConverters[healthsrc.TaskFlux+"/images"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ImageRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ImageRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, imageRowDTO(r))
		}
		return out, nil
	}
}

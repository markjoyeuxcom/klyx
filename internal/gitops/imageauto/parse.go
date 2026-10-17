package imageauto

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Readiness is the Ready condition as the rule reads it.
type Readiness string

const (
	Ready       Readiness = "Ready"
	Failed      Readiness = "Failed"
	Reconciling Readiness = "Reconciling"
	Unknown     Readiness = "Unknown"
)

// Owner is who applies an object (roadmap C4's consequence line).
type Owner struct {
	Kind, Namespace, Name string
}

// Common is what every image kind carries.
type Common struct {
	Kind, Namespace, Name, UID string
	Generation                 int64
	// ObservedGeneration is status.observedGeneration; ObservedSet is false
	// when status carries none.
	ObservedGeneration int64
	ObservedSet        bool
	Created            time.Time
	Suspended          bool
	Ready              Readiness
	// Reason and Message are Ready's (the message clipped, URL-scrubbed, and
	// for an automation passed through its allowlist); ReadySince its
	// transition time; ReadyGeneration its own observedGeneration (0 when
	// absent).
	Reason, Message string
	ReadySince      time.Time
	ReadyGeneration int64
	Stalled         bool
	StalledReason   string
	// LastHandled is status.lastHandledReconcileAt; RequestedAt the request
	// annotation. A request is handled when they are equal.
	LastHandled, RequestedAt string
	Owner                    *Owner
	Clipped, Incomplete      []string
}

func common(o *Object) Common {
	u := o.Unstructured
	c := Common{Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time, Clipped: o.Clipped, Incomplete: o.Incomplete}
	c.ObservedGeneration, c.ObservedSet, _ = unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	c.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	c.LastHandled, _, _ = unstructured.NestedString(u.Object, "status", "lastHandledReconcileAt")
	ann := u.GetAnnotations()
	c.RequestedAt = ann[AnnRequestedAt]
	c.Owner = ownerOf(u.GetLabels(), ann)
	c.Ready = Unknown
	reconciling := false
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, x := range conds {
		cm, _ := x.(map[string]any)
		t, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		switch t {
		case "Ready":
			switch st {
			case "True":
				c.Ready = Ready
			case "False":
				c.Ready = Failed
			}
			c.Reason = reason
			c.Message, _ = cm["message"].(string)
			if ts, ok := cm["lastTransitionTime"].(string); ok {
				c.ReadySince, _ = time.Parse(time.RFC3339, ts)
			}
			c.ReadyGeneration, _ = cm["observedGeneration"].(int64)
		case "Reconciling":
			reconciling = st == "True"
		case "Stalled":
			if st == "True" {
				c.Stalled, c.StalledReason = true, reason
			}
		}
	}
	if reconciling && c.Ready != Failed {
		c.Ready = Reconciling
	}
	return c
}

func ownerOf(labels, ann map[string]string) *Owner {
	switch {
	case labels["kustomize.toolkit.fluxcd.io/name"] != "":
		return &Owner{Kind: "Kustomization", Namespace: labels["kustomize.toolkit.fluxcd.io/namespace"], Name: labels["kustomize.toolkit.fluxcd.io/name"]}
	case labels["helm.toolkit.fluxcd.io/name"] != "":
		return &Owner{Kind: "HelmRelease", Namespace: labels["helm.toolkit.fluxcd.io/namespace"], Name: labels["helm.toolkit.fluxcd.io/name"]}
	case ann["argocd.argoproj.io/tracking-id"] != "":
		return &Owner{Kind: "Application", Name: ann["argocd.argoproj.io/tracking-id"]}
	}
	return nil
}

// incomplete reports whether a join input was dropped to fit.
func (c Common) incomplete(path string) bool {
	for _, p := range c.Incomplete {
		if p == path {
			return true
		}
	}
	return false
}

// Repository is an ImageRepository.
type Repository struct {
	Common
	Image, CanonicalImage, Provider, ServiceAccountName string
	SecretRef, CertSecretRef, ProxySecretRef            string
	// Interval is spec.interval (0 when unset or unparsable; IntervalText
	// keeps the text).
	Interval            time.Duration
	IntervalText        string
	Timeout             string
	Insecure            bool
	ExclusionList       []string
	AccessFromSelectors int
	// Scanned: status has a scan result. ScanTime is the controller's clock
	// at the last successful scan.
	Scanned      bool
	TagCount     int
	ScanRevision string
	ScanTime     time.Time
	// LatestTags are the controller's ten highest tags by reverse string
	// order, never "latest" by any policy.
	LatestTags []string
}

// ParseRepository reads a projected ImageRepository.
func ParseRepository(o *Object) Repository {
	m := o.Object
	r := Repository{Common: common(o)}
	r.Image, _, _ = unstructured.NestedString(m, "spec", "image")
	r.Provider, _, _ = unstructured.NestedString(m, "spec", "provider")
	r.ServiceAccountName, _, _ = unstructured.NestedString(m, "spec", "serviceAccountName")
	r.SecretRef, _, _ = unstructured.NestedString(m, "spec", "secretRef")
	r.CertSecretRef, _, _ = unstructured.NestedString(m, "spec", "certSecretRef")
	r.ProxySecretRef, _, _ = unstructured.NestedString(m, "spec", "proxySecretRef")
	r.IntervalText, _, _ = unstructured.NestedString(m, "spec", "interval")
	r.Interval, _ = time.ParseDuration(r.IntervalText)
	r.Timeout, _, _ = unstructured.NestedString(m, "spec", "timeout")
	r.Insecure, _, _ = unstructured.NestedBool(m, "spec", "insecure")
	r.ExclusionList, _, _ = unstructured.NestedStringSlice(m, "spec", "exclusionList")
	if n, ok, _ := unstructured.NestedInt64(m, "spec", "accessFromSelectors"); ok {
		r.AccessFromSelectors = int(n)
	}
	r.CanonicalImage, _, _ = unstructured.NestedString(m, "status", "canonicalImageName")
	if scan, ok, _ := unstructured.NestedMap(m, "status", "lastScanResult"); ok {
		r.Scanned = true
		if n, ok, _ := unstructured.NestedInt64(scan, "tagCount"); ok {
			r.TagCount = int(n)
		}
		r.ScanRevision, _, _ = unstructured.NestedString(scan, "revision")
		if ts, ok, _ := unstructured.NestedString(scan, "scanTime"); ok {
			r.ScanTime, _ = time.Parse(time.RFC3339, ts)
		}
		r.LatestTags, _, _ = unstructured.NestedStringSlice(scan, "latestTags")
	}
	return r
}

// ImageRef is a resolved image.
type ImageRef struct {
	Name, Tag, Digest string
}

// String is name:tag, then @digest when present.
func (r ImageRef) String() string {
	s := r.Name
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// SplitImage splits a v1beta2 latestImage string: the digest after "@",
// then the tag after the last ":" that follows the final "/".
func SplitImage(s string) ImageRef {
	var r ImageRef
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s, r.Digest = s[:i], s[i+1:]
	}
	slash := strings.LastIndexByte(s, '/')
	if i := strings.LastIndexByte(s, ':'); i > slash {
		s, r.Tag = s[:i], s[i+1:]
	}
	r.Name = s
	return r
}

func imageRefAt(m map[string]any, field, legacy string) *ImageRef {
	if ref, ok, _ := unstructured.NestedMap(m, "status", field); ok {
		r := ImageRef{}
		r.Name, _, _ = unstructured.NestedString(ref, "name")
		r.Tag, _, _ = unstructured.NestedString(ref, "tag")
		r.Digest, _, _ = unstructured.NestedString(ref, "digest")
		if r.Name != "" {
			return &r
		}
	}
	if s, ok, _ := unstructured.NestedString(m, "status", legacy); ok && s != "" {
		r := SplitImage(s)
		return &r
	}
	return nil
}

// Policy is an ImagePolicy.
type Policy struct {
	Common
	// RepositoryNamespace defaults to the policy's own.
	RepositoryName, RepositoryNamespace string
	// PolicyType is semver, alphabetical or numerical; PolicyValue its range
	// or order.
	PolicyType, PolicyValue      string
	FilterPattern, FilterExtract string
	DigestReflection             string
	Interval                     string
	// Latest is the resolved image (latestRef, else latestImage); it stays
	// while the policy fails (the controller never clears it). Previous is
	// the one before.
	Latest, Previous *ImageRef
	// Labels are the policy's labels, whole: an automation's selector
	// matches them. LabelsIncomplete: they could not be kept.
	Labels           map[string]string
	LabelsIncomplete bool
}

// ParsePolicy reads a projected ImagePolicy.
func ParsePolicy(o *Object) Policy {
	m := o.Object
	p := Policy{Common: common(o)}
	p.RepositoryName, _, _ = unstructured.NestedString(m, "spec", "imageRepositoryRef", "name")
	p.RepositoryNamespace, _, _ = unstructured.NestedString(m, "spec", "imageRepositoryRef", "namespace")
	if p.RepositoryNamespace == "" {
		p.RepositoryNamespace = p.Namespace
	}
	for _, t := range []string{"semver", "alphabetical", "numerical"} {
		if x, ok, _ := unstructured.NestedMap(m, "spec", "policy", t); ok {
			p.PolicyType = t
			if v, ok := x["range"].(string); ok {
				p.PolicyValue = v
			} else if v, ok := x["order"].(string); ok {
				p.PolicyValue = v
			}
			break
		}
	}
	p.FilterPattern, _, _ = unstructured.NestedString(m, "spec", "filterTags", "pattern")
	p.FilterExtract, _, _ = unstructured.NestedString(m, "spec", "filterTags", "extract")
	p.DigestReflection, _, _ = unstructured.NestedString(m, "spec", "digestReflectionPolicy")
	p.Interval, _, _ = unstructured.NestedString(m, "spec", "interval")
	p.Latest = imageRefAt(m, "latestRef", "latestImage")
	p.Previous = imageRefAt(m, "observedPreviousRef", "observedPreviousImage")
	p.Labels = o.GetLabels()
	p.LabelsIncomplete = p.incomplete("metadata.labels")
	return p
}

// CheckoutRef is a Git reference, as the automation or the GitRepository
// declares it.
type CheckoutRef struct {
	Branch, Tag, SemVer, Name, Commit string
}

// Automation is an ImageUpdateAutomation.
type Automation struct {
	Common
	SourceKind, SourceName, SourceNamespace string
	Interval                                time.Duration
	IntervalText                            string
	// Checkout is git.checkout.ref, nil when unset.
	Checkout                   *CheckoutRef
	PushBranch, PushRefspec    string
	HasPush                    bool
	PushOptionCount            int
	AuthorName, AuthorEmail    string
	SigningKeySecret           string
	UpdateStrategy, UpdatePath string
	// Selector is policySelector; HasSelector false means every policy in
	// the namespace. SelectorIncomplete: it could not be kept whole.
	Selector           *metav1.LabelSelector
	HasSelector        bool
	SelectorIncomplete bool
	// LastRun is lastAutomationRunTime, set when a run starts: an attempt,
	// never a success. LastPushTime is the controller's clock just after the
	// push.
	LastRun, LastPushTime  time.Time
	LastPushCommit         string
	ObservedSourceRevision string
	// ObservedPolicies are the images of the last successful run;
	// ObservedComplete false when the map could not be kept whole.
	ObservedPolicies map[string]ImageRef
	ObservedComplete bool
}

// ParseAutomation reads a projected ImageUpdateAutomation.
func ParseAutomation(o *Object) Automation {
	m := o.Object
	a := Automation{Common: common(o)}
	a.SourceKind, _, _ = unstructured.NestedString(m, "spec", "sourceRef", "kind")
	a.SourceName, _, _ = unstructured.NestedString(m, "spec", "sourceRef", "name")
	a.SourceNamespace, _, _ = unstructured.NestedString(m, "spec", "sourceRef", "namespace")
	if a.SourceNamespace == "" {
		a.SourceNamespace = a.Namespace
	}
	a.IntervalText, _, _ = unstructured.NestedString(m, "spec", "interval")
	a.Interval, _ = time.ParseDuration(a.IntervalText)
	if ref, ok, _ := unstructured.NestedMap(m, "spec", "git", "checkout", "ref"); ok {
		r := CheckoutRef{}
		r.Branch, _, _ = unstructured.NestedString(ref, "branch")
		r.Tag, _, _ = unstructured.NestedString(ref, "tag")
		r.SemVer, _, _ = unstructured.NestedString(ref, "semver")
		r.Name, _, _ = unstructured.NestedString(ref, "name")
		r.Commit, _, _ = unstructured.NestedString(ref, "commit")
		a.Checkout = &r
	}
	if push, ok, _ := unstructured.NestedMap(m, "spec", "git", "push"); ok {
		a.HasPush = true
		a.PushBranch, _, _ = unstructured.NestedString(push, "branch")
		a.PushRefspec, _, _ = unstructured.NestedString(push, "refspec")
		if n, ok, _ := unstructured.NestedInt64(push, "optionCount"); ok {
			a.PushOptionCount = int(n)
		}
	}
	a.AuthorName, _, _ = unstructured.NestedString(m, "spec", "git", "commit", "author", "name")
	a.AuthorEmail, _, _ = unstructured.NestedString(m, "spec", "git", "commit", "author", "email")
	a.SigningKeySecret, _, _ = unstructured.NestedString(m, "spec", "git", "commit", "signingKeySecret")
	a.UpdateStrategy, _, _ = unstructured.NestedString(m, "spec", "update", "strategy")
	a.UpdatePath, _, _ = unstructured.NestedString(m, "spec", "update", "path")
	a.SelectorIncomplete = a.incomplete("spec.policySelector")
	if sel, ok, _ := unstructured.NestedMap(m, "spec", "policySelector"); ok {
		a.HasSelector = true
		var ls metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(sel, &ls); err == nil {
			a.Selector = &ls
		}
	} else if a.SelectorIncomplete {
		a.HasSelector = true
	}
	for f, t := range map[string]*time.Time{"lastAutomationRunTime": &a.LastRun, "lastPushTime": &a.LastPushTime} {
		if ts, ok, _ := unstructured.NestedString(m, "status", f); ok {
			*t, _ = time.Parse(time.RFC3339, ts)
		}
	}
	a.LastPushCommit, _, _ = unstructured.NestedString(m, "status", "lastPushCommit")
	a.ObservedSourceRevision, _, _ = unstructured.NestedString(m, "status", "observedSourceRevision")
	a.ObservedComplete = !a.incomplete("status.observedPolicies")
	if pols, ok, _ := unstructured.NestedMap(m, "status", "observedPolicies"); ok {
		a.ObservedPolicies = map[string]ImageRef{}
		for name, x := range pols {
			ref, _ := x.(map[string]any)
			r := ImageRef{}
			r.Name, _ = ref["name"].(string)
			r.Tag, _ = ref["tag"].(string)
			r.Digest, _ = ref["digest"].(string)
			a.ObservedPolicies[name] = r
		}
	}
	return a
}

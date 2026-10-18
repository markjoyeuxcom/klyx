package identity

import (
	"sort"
	"strings"
	"time"
)

// Adapters.
const (
	AdapterAWS   = "aws"
	AdapterAzure = "azure"
	AdapterGKE   = "gke"
)

// Reading states (S5 §3.3).
const (
	StateMatch       = "match"
	StateMatchClient = "match-client" // Azure: the client ID matches, no tenant declared
	StateDiffers     = "differs"
	StateUndeclared  = "configured-undeclared"
	StatePodIdentity = "pod-identity"
	StateBothAWS     = "both-aws"
	StateNotConfig   = "not-configured"
	StateNotOptedIn  = "not-opted-in"
	StateExcluded    = "excluded"
	StatePartial     = "partial"
	StateUnresolved  = "unresolved"
	StateLinked      = "linked"
	StateUnlinked    = "unlinked"
	StateHostNetwork = "host-network"
	StateNotGKE      = "not-gke" // a GKE link on a cluster whose nodes are not GKE
)

// Reading is one comparison, current against current, naming what it
// compared. Container is "" for a pod-level reading (GKE).
type Reading struct {
	Adapter   string `json:"adapter"`
	Container string `json:"container,omitempty"`
	State     string `json:"state"`
	Text      string `json:"text"`
	Compared  string `json:"compared,omitempty"`
}

// Limits, said every time (S5 §5).
var Limits = []string{
	"Cloud trust, Pod Identity associations and permissions are not visible to Klyx.",
	"Identity is given when a pod is created; when an annotation changed is not visible.",
	"A value in the pod spec wins over the webhook, and Klyx cannot tell which wrote it.",
	"envFrom sources and the image's own environment are not read.",
	"Which credential the SDK picks is not inferred.",
	"Only EKS, Azure and GKE identity are read.",
}

const envFromNote = " envFrom sources and the image's environment are not read."

func notConfigured(p Pod, c Container) string {
	t := "Not configured in the pod spec: it was created at " + p.Created.UTC().Format(time.RFC3339) +
		". A pod created before the annotation keeps none; when the annotation was set is not visible."
	if c.EnvFrom {
		t += envFromNote
	}
	return t
}

func mechOf(c Container, kind string) *Mechanism {
	for i := range c.Mechanisms {
		if c.Mechanisms[i].Kind == kind {
			return &c.Mechanisms[i]
		}
	}
	return nil
}

// bothAWS reads a container with both AWS mechanisms by each one's own
// state: "configured" only for a complete chain.
func bothAWS(r Reading, irsa, pi *Mechanism) Reading {
	const sdk = " Which credential the SDK uses is not inferred."
	irsaOK, piOK := irsa.State == ChainComplete, pi.State == ChainComplete
	switch {
	case irsaOK && piOK:
		r.State, r.Text = StateBothAWS, "Both IRSA and EKS Pod Identity are configured in this container."+sdk
	case irsaOK:
		r.State, r.Text = StateBothAWS, "IRSA is configured; EKS Pod Identity is also present but "+pi.State+": "+pi.Missing+"."+sdk
	case piOK:
		r.State, r.Text = StateBothAWS, "EKS Pod Identity is configured; IRSA is also present but "+irsa.State+": "+irsa.Missing+"."+sdk
	default:
		r.State = StatePartial
		if irsa.State == ChainUnresolved || pi.State == ChainUnresolved {
			r.State = StateUnresolved
		}
		r.Text = "Neither AWS mechanism is complete: IRSA " + irsa.State + " (" + irsa.Missing + "); EKS Pod Identity " + pi.State + " (" + pi.Missing + "); nothing is compared."
	}
	return r
}

// oversizedAnn names a declared value past MaxValue, which is never
// compared (S5 §6); "" when none is.
func oversizedAnn(vs map[string]*Value) string {
	var names []string
	for name, v := range vs {
		if v != nil && v.Oversized() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func unresolvedDeclared(r Reading, ann string) Reading {
	r.State, r.Text = StateUnresolved, "Unresolved: the ServiceAccount's "+ann+" annotation is oversized; nothing is compared."
	return r
}

func chainReading(adapter, container string, m *Mechanism) Reading {
	if m.State == ChainUnresolved {
		return Reading{Adapter: adapter, Container: container, State: StateUnresolved, Text: "Unresolved: " + m.Missing + "; nothing is compared."}
	}
	return Reading{Adapter: adapter, Container: container, State: StatePartial, Text: "Partial: " + m.Missing + "; nothing is compared."}
}

// Platform is what the nodes show of the cluster, for the GKE reading:
// only GKE's metadata server honours a linked Google service account.
type Platform struct {
	Kind string // gke, other or unverified
	Why  string // for unverified: why GKE is neither proven nor ruled out
}

const (
	platformGKE        = "gke"
	platformOther      = "other"
	platformUnverified = "unverified"
)

var (
	// PlatformGKE: a gce:// node with GKE's own evidence (its node-pool
	// label, or a gke-metadata-server pod on it).
	PlatformGKE = Platform{Kind: platformGKE}
	// PlatformOther: every node read has a provider ID and none is a Google
	// Compute (gce://) node, which every GKE node is.
	PlatformOther = Platform{Kind: platformOther}
)

// PlatformUnverified: the nodes do not settle it, and why.
func PlatformUnverified(why string) Platform { return Platform{Kind: platformUnverified, Why: why} }

// Compare reads every container of a pod against the ServiceAccount's
// current declarations, on a cluster of the given platform.
func Compare(d Declared, p Pod, pl Platform) []Reading {
	out := []Reading{} // compared: empty is "nothing configured", never unread
	for _, c := range p.Containers {
		if r, ok := compareAWS(d, p, c); ok {
			out = append(out, r)
		}
		if r, ok := compareAzure(d, p, c); ok {
			out = append(out, r)
		}
	}
	if d.GKEServiceAccount != nil || pl.Kind == platformGKE {
		out = append(out, compareGKE(d, p, pl))
	}
	return out
}

func compareAWS(d Declared, p Pod, c Container) (Reading, bool) {
	irsa, pi := mechOf(c, MechIRSA), mechOf(c, MechPodIdentity)
	r := Reading{Adapter: AdapterAWS, Container: c.Name}
	switch {
	case irsa != nil && pi != nil:
		return bothAWS(r, irsa, pi), true
	case pi != nil:
		if pi.State != ChainComplete {
			return chainReading(AdapterAWS, c.Name, pi), true
		}
		r.State, r.Text = StatePodIdentity, "EKS Pod Identity configured. Its association and role live in AWS: not visible."
		if d.RoleARN != nil {
			r.Text += " The webhook prefers Pod Identity over IRSA when both apply."
		}
		return r, true
	case irsa != nil:
		if irsa.State != ChainComplete {
			return chainReading(AdapterAWS, c.Name, irsa), true
		}
		if ann := oversizedAnn(map[string]*Value{AnnRoleARN: d.RoleARN, AnnAudience: d.Audience}); ann != "" {
			return unresolvedDeclared(r, ann), true
		}
		r.Compared = "role and audience"
		if d.RoleARN == nil {
			r.State, r.Text = StateUndeclared, "Configured with "+irsa.Role.Shown+"; the ServiceAccount declares no role now (removed later, set in the template, or a self-hosted webhook's ConfigMap)."
			return r, true
		}
		roleOK, audOK := irsa.Role.Equal(*d.RoleARN), irsa.Audience != nil && irsa.Audience.raw == d.DeclaredAudience()
		switch {
		case roleOK && audOK:
			r.State, r.Text = StateMatch, "Role and audience match the ServiceAccount."
		case !roleOK:
			r.State, r.Text = StateDiffers, "Differs: the pod carries "+irsa.Role.Shown+"; the ServiceAccount now declares "+d.RoleARN.Shown+
				". Pods take identity when created; one created before the change keeps the old role. A value set in the pod template also wins over the webhook."
		default:
			r.State, r.Text = StateDiffers, "Differs: the token's audience is "+irsa.Audience.Shown+"; the ServiceAccount now asks for "+audienceShown(d.DeclaredAudience())+"."
		}
		return r, true
	}
	if d.RoleARN == nil {
		return r, false
	}
	switch {
	case p.AWSOptOut:
		r.State, r.Text = StateExcluded, "Excluded: the pod carries the "+LabelAWSOptOut+" label."
	case c.SkippedAWS:
		r.State, r.Text = StateExcluded, "Excluded: the pod's "+AnnAWSSkip+" annotation names this container."
	default:
		r.State, r.Text = StateNotConfig, notConfigured(p, c)
	}
	return r, true
}

func compareAzure(d Declared, p Pod, c Container) (Reading, bool) {
	az := mechOf(c, MechAzure)
	r := Reading{Adapter: AdapterAzure, Container: c.Name}
	if az != nil {
		if az.State != ChainComplete {
			return chainReading(AdapterAzure, c.Name, az), true
		}
		if ann := oversizedAnn(map[string]*Value{AnnAzureClient: d.AzureClient, AnnAzureTenant: d.AzureTenant}); ann != "" {
			return unresolvedDeclared(r, ann), true
		}
		r.Compared = "client ID and tenant"
		if d.AzureClient == nil {
			r.State, r.Text = StateUndeclared, "Configured with client "+az.Client.Shown+"; the ServiceAccount declares no client ID now."
			return r, true
		}
		clientOK := az.Client.Equal(*d.AzureClient)
		switch {
		case !clientOK:
			r.State, r.Text = StateDiffers, "Differs: the pod carries client "+az.Client.Shown+"; the ServiceAccount now declares "+d.AzureClient.Shown+". Pods take identity when created."
		case d.AzureTenant == nil:
			r.State, r.Text = StateMatchClient, "Client ID matches. The pod carries tenant "+az.Tenant.Shown+
				"; the ServiceAccount declares no tenant now. Where that tenant came from (the webhook's default, a hand-set value, an earlier annotation) is not visible."
		case az.Tenant.Equal(*d.AzureTenant):
			r.State, r.Text = StateMatch, "Client ID and tenant match."
		default:
			r.State, r.Text = StateDiffers, "Differs: the pod carries tenant "+az.Tenant.Shown+"; the ServiceAccount now declares "+d.AzureTenant.Shown+"."
		}
		return r, true
	}
	if d.AzureClient == nil {
		return r, false
	}
	switch {
	case c.SkippedAzure:
		r.State, r.Text = StateExcluded, "Excluded: the pod's "+AnnAzureSkip+" annotation names this container."
	case !p.AzureOptIn:
		r.State, r.Text = StateNotOptedIn, "Not opted in: the ServiceAccount annotation alone does nothing; the pod needs the label "+LabelAzureUse+`: "true".`
	default:
		r.State, r.Text = StateNotConfig, notConfigured(p, c)
	}
	return r, true
}

func compareGKE(d Declared, p Pod, pl Platform) Reading {
	r := Reading{Adapter: AdapterGKE}
	switch {
	case d.GKEServiceAccount != nil && pl.Kind == platformOther:
		r.State, r.Text = StateNotGKE, "Declares "+d.GKEServiceAccount.Shown+", which only GKE's metadata server honours. Every node read has a provider ID and none is a Google Compute (gce://) node, as every GKE node is, so the annotation does nothing here."
	case p.HostNetwork:
		r.State, r.Text = StateHostNetwork, "This pod runs on the host network, so it does not use Workload Identity Federation for GKE."
	case d.GKEServiceAccount != nil:
		r.State, r.Text = StateLinked, "Linked to "+d.GKEServiceAccount.Shown+" for impersonation; the IAM binding is not visible."
		if pl.Kind == platformUnverified {
			r.Text += " Whether this cluster is GKE is not verified: " + pl.Why + "."
		}
	default:
		r.State, r.Text = StateUnlinked, "No link declared; direct principal access may still be granted in IAM, which Klyx cannot see."
	}
	return r
}

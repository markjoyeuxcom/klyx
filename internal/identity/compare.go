package identity

import (
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

func chainReading(adapter, container string, m *Mechanism) Reading {
	if m.State == ChainUnresolved {
		return Reading{Adapter: adapter, Container: container, State: StateUnresolved, Text: "Unresolved: " + m.Missing + "; nothing is compared."}
	}
	return Reading{Adapter: adapter, Container: container, State: StatePartial, Text: "Partial: " + m.Missing + "; nothing is compared."}
}

// Compare reads every container of a pod against the ServiceAccount's
// current declarations. gke says the pod's cluster is GKE (a gce:// node).
func Compare(d Declared, p Pod, gke bool) []Reading {
	var out []Reading
	for _, c := range p.Containers {
		if r, ok := compareAWS(d, p, c); ok {
			out = append(out, r)
		}
		if r, ok := compareAzure(d, p, c); ok {
			out = append(out, r)
		}
	}
	if d.GKEServiceAccount != nil || gke {
		out = append(out, compareGKE(d, p))
	}
	return out
}

func compareAWS(d Declared, p Pod, c Container) (Reading, bool) {
	irsa, pi := mechOf(c, MechIRSA), mechOf(c, MechPodIdentity)
	r := Reading{Adapter: AdapterAWS, Container: c.Name}
	switch {
	case irsa != nil && pi != nil:
		r.State, r.Text = StateBothAWS, "Both IRSA and EKS Pod Identity are configured in this container; which credential the SDK uses is not inferred."
		return r, true
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
		r.Compared = "role and audience"
		if d.RoleARN == nil {
			r.State, r.Text = StateUndeclared, "Configured with "+irsa.Role.Shown+"; the ServiceAccount declares no role now (removed later, set in the template, or a self-hosted webhook's ConfigMap)."
			return r, true
		}
		roleOK, audOK := irsa.Role.Equal(*d.RoleARN), irsa.Audience == d.DeclaredAudience()
		switch {
		case roleOK && audOK:
			r.State, r.Text = StateMatch, "Role and audience match the ServiceAccount."
		case !roleOK:
			r.State, r.Text = StateDiffers, "Differs: the pod carries "+irsa.Role.Shown+"; the ServiceAccount now declares "+d.RoleARN.Shown+
				". Pods take identity when created; one created before the change keeps the old role. A value set in the pod template also wins over the webhook."
		default:
			r.State, r.Text = StateDiffers, "Differs: the token's audience is "+audienceShown(irsa.Audience)+"; the ServiceAccount now asks for "+audienceShown(d.DeclaredAudience())+"."
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

func compareGKE(d Declared, p Pod) Reading {
	r := Reading{Adapter: AdapterGKE}
	switch {
	case p.HostNetwork:
		r.State, r.Text = StateHostNetwork, "This pod runs on the host network, so it does not use Workload Identity Federation for GKE."
	case d.GKEServiceAccount != nil:
		r.State, r.Text = StateLinked, "Linked to "+d.GKEServiceAccount.Shown+" for impersonation; the IAM binding is not visible."
	default:
		r.State, r.Text = StateUnlinked, "No link declared; direct principal access may still be granted in IAM, which Klyx cannot see."
	}
	return r
}

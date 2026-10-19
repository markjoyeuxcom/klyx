package identity

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Read states of a report and its parts.
const (
	StateRead       = "read"
	StateReplaced   = "replaced"
	StateNotFound   = "not-found"
	StateUnreadable = "unreadable"
	ReadComplete    = "complete"
	ReadMore        = "more"    // a continued list: more not read, count unknown
	ReadPartial     = "partial" // read, but a join behind it is not resolved
	ReadNone        = "unread"  // not read
)

// MaxStores bounds the stores listed per ServiceAccount (S5 §6).
const MaxStores = 20

// PodReport is one pod's evidence, readings and node side.
type PodReport struct {
	Pod Pod `json:"pod"`
	// Compared: the pod was read against its ServiceAccount. Without it the
	// readings are absent, never "nothing configured" (the ServiceAccount
	// was not read).
	Compared bool      `json:"compared"`
	Readings []Reading `json:"readings"`
	Nodes    []Side    `json:"nodes,omitempty"`
}

// Env is what the caller read of the cluster around a ServiceAccount.
type Env struct {
	Nodes        map[string]*NodeInfo
	NodesRead    bool
	AgentPods    []ComponentPod
	AgentRead    bool
	MetadataPods []ComponentPod
	MetadataRead bool
	Webhooks     []Webhook
}

// platform says what the nodes read show of the cluster. A gce:// provider
// ID proves Google Compute, not GKE: GKE needs its own evidence (C2).
func (e Env) platform() Platform {
	if !e.NodesRead {
		return PlatformUnverified("the nodes were not read")
	}
	gce, noID := false, false
	for _, n := range e.Nodes {
		switch {
		case n.ProviderID == "":
			noID = true
		case strings.HasPrefix(n.ProviderID, "gce://"):
			gce = true
			if e.gkeNode(n) {
				return PlatformGKE
			}
		}
	}
	switch {
	case gce:
		return PlatformUnverified("its Google Compute (gce://) nodes carry no " + LabelGKENodePool + " label and run no gke-metadata-server pod")
	case noID:
		return PlatformUnverified("a node has no provider ID")
	}
	return PlatformOther
}

// gkeNode: a gce:// node with GKE's own evidence: its node-pool label, or a
// gke-metadata-server pod on it.
func (e Env) gkeNode(n *NodeInfo) bool {
	if _, ok := n.Labels[LabelGKENodePool]; ok {
		return true
	}
	if e.MetadataRead {
		for _, p := range e.MetadataPods {
			if p.Node == n.Name {
				return true
			}
		}
	}
	return false
}

// PodReportOf compares one pod with its ServiceAccount's declarations and
// reads its node's side.
func PodReportOf(d Declared, p Pod, e Env) PodReport {
	pl := e.platform()
	r := PodReport{Pod: p, Readings: Compare(d, p, pl), Compared: true}
	var node *NodeInfo
	if e.NodesRead {
		node = e.Nodes[p.Node]
	}
	for _, c := range p.Containers {
		if m := mechOf(c, MechPodIdentity); m != nil {
			r.Nodes = append(r.Nodes, PodIdentityNode(node, e.AgentPods, e.AgentRead))
			break
		}
	}
	if pl.Kind == platformGKE && !p.HostNetwork {
		r.Nodes = append(r.Nodes, GKENode(node, e.MetadataPods, e.MetadataRead))
	}
	return r
}

// StoreLink is a store that authenticates through a ServiceAccount.
type StoreLink struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode"`
	// Via: "serviceAccountRef", or "controller" (no auth: the handling
	// controller's identity, this ServiceAccount's pods).
	Via string `json:"via"`
	// Referent: a ClusterSecretStore naming no namespace, said as "for
	// ExternalSecrets in this namespace".
	Referent bool `json:"referent,omitempty"`
}

// StoreRefersTo reports whether a store's serviceAccountRef names the
// ServiceAccount, by ESO's namespace rules (S5 §4.3).
func StoreRefersTo(st *unstructured.Unstructured, saNS, saName string) (StoreLink, bool) {
	sub := StoreSubject(st, nil)
	link := StoreLink{Kind: st.GetKind(), Namespace: st.GetNamespace(), Name: st.GetName(), UID: string(st.GetUID()), Provider: sub.Provider, Mode: sub.Mode}
	if sub.SA == nil || sub.SA.Name != saName {
		return link, false
	}
	switch {
	case sub.SA.Referent:
		link.Via, link.Referent = "serviceAccountRef", true
		return link, true
	case sub.SA.Namespace == saNS:
		link.Via = "serviceAccountRef"
		return link, true
	}
	return link, false
}

// SAReport is the ServiceAccount inspector's Identity block (S5 §5).
type SAReport struct {
	ReadAt   time.Time   `json:"readAt"`
	State    string      `json:"state"`
	Error    string      `json:"error,omitempty"`
	Declared *Declared   `json:"declared,omitempty"`
	Pods     []PodReport `json:"pods"`
	PodsRead string      `json:"podsRead"`
	Webhooks []Side      `json:"webhooks,omitempty"`
	Stores   []StoreLink `json:"stores"`
	// StoresRead: complete, unread, partial (a controller join is not
	// resolved; StoresGap says which), or "cut" past MaxStores.
	StoresRead string   `json:"storesRead"`
	StoresGap  string   `json:"storesGap,omitempty"`
	Limits     []string `json:"limits"`
}

// PodInspectorReport is the Pod inspector's Identity block.
type PodInspectorReport struct {
	ReadAt   time.Time  `json:"readAt"`
	State    string     `json:"state"`
	Error    string     `json:"error,omitempty"`
	Declared *Declared  `json:"declared,omitempty"`
	SAState  string     `json:"saState"`
	Report   *PodReport `json:"report,omitempty"`
	Webhooks []Side     `json:"webhooks,omitempty"`
	Limits   []string   `json:"limits"`
}

// StoreReport is the ESO store inspector's "Authenticates as" block.
type StoreReport struct {
	ReadAt  time.Time `json:"readAt"`
	State   string    `json:"state"`
	Error   string    `json:"error,omitempty"`
	Subject Subject   `json:"subject"`
	// Handlers: each applicable group with its pods (their mechanisms).
	Handlers *Handlers `json:"handlers,omitempty"`
	EnvGaps  []string  `json:"envGaps,omitempty"`
	Limits   []string  `json:"limits"`
}

// StoreLimits are said under every store subject.
var StoreLimits = []string{
	"Cloud trust and permissions are not visible to Klyx; a resolved subject is what ESO is configured to use, not proof that it works.",
	"Secret and ConfigMap contents (static keys, credential configurations, Secret-sourced IDs) are never read.",
	"envFrom sources and the image's own environment are not read.",
}

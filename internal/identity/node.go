package identity

import (
	"strings"

	"github.com/moomora/klyx/internal/capability"
)

// NodeInfo is what the Observer's nodes model says of a pod's node.
type NodeInfo struct {
	Name       string
	Labels     map[string]string
	ProviderID string
	OS         string // from the kubernetes.io/os label
}

// ComponentPod is one platform component's pod on a node (the agent, the
// GKE metadata server).
type ComponentPod struct {
	Name  string
	Node  string
	Ready bool
}

// Side is a component's management and health for one workload (C2).
type Side struct {
	Component  string                `json:"component"`
	Management capability.Management `json:"management"`
	Health     string                `json:"health"`
	// Fault: an observed component that is not ready on this node, said as
	// evidence the workload will feel, never suppressed by its manager.
	Fault bool   `json:"fault,omitempty"`
	Note  string `json:"note,omitempty"`
}

const (
	componentAgent    = "EKS Pod Identity agent"
	componentMetadata = "GKE metadata server"
)

// PodIdentityNode is the node side of a pod using EKS Pod Identity (S5 §3.4,
// C2's table). agentRead says the agent's pods were read.
func PodIdentityNode(n *NodeInfo, agentPods []ComponentPod, agentRead bool) Side {
	s := Side{Component: componentAgent}
	if n == nil {
		s.Management = capability.Management{Manager: capability.ManagerUnknown, Observed: capability.NotObserved, ObservedEvidence: "the pod's node is not known"}
		s.Health = "the pod's node is not known"
		return s
	}
	switch n.Labels[LabelComputeType] {
	case "auto":
		s.Management = capability.Management{Manager: capability.ManagerPlatform,
			ManagerEvidence:  "the node label " + LabelComputeType + ": auto; the EKS user guide: built into EKS Auto Mode",
			Observed:         capability.NotObserved,
			ObservedEvidence: "how the agent runs on Auto Mode nodes is not documented"}
		s.Health = s.Management.Unobserved()
		return s
	case "fargate":
		s.Note = "EKS Pod Identity is not supported on Fargate nodes."
	}
	if n.OS == "windows" {
		s.Note = "EKS Pod Identity is not supported on Windows nodes."
	}
	s.Management = capability.Management{Manager: capability.ManagerUnknown, ManagerEvidence: "the add-on's labels are not verified",
		Observed: capability.Observed, ObservedEvidence: "the eks-pod-identity-agent DaemonSet's pod on this node"}
	if !agentRead {
		s.Health = "the agent's pods were not read"
		return s
	}
	for _, p := range agentPods {
		if p.Node != n.Name {
			continue
		}
		if p.Ready {
			s.Health = "agent pod " + p.Name + " is ready on this node"
		} else {
			s.Health, s.Fault = "agent pod "+p.Name+" on this node is not ready: credential requests from this pod are expected to fail", true
		}
		return s
	}
	s.Health = "no Pod Identity agent pod on this node"
	s.Fault = s.Note == ""
	return s
}

// GKENode is the node side of a pod on GKE (S5 §3.4, C2's table).
func GKENode(n *NodeInfo, mdPods []ComponentPod, read bool) Side {
	s := Side{Component: componentMetadata}
	if n == nil {
		s.Management = capability.Management{Manager: capability.ManagerUnknown, Observed: capability.NotObserved, ObservedEvidence: "the pod's node is not known"}
		s.Health = "the pod's node is not known"
		return s
	}
	gce := strings.HasPrefix(n.ProviderID, "gce://")
	mgr, mev := capability.ManagerUnknown, "the node is not a gce:// node"
	if gce {
		mgr, mev = capability.ManagerPlatform, "a gke-metadata-server pod in kube-system, on a gce:// node"
	}
	if n.Labels[LabelGKEMetadata] == "true" {
		s.Note = "the node carries " + LabelGKEMetadata + `: "true"`
	} else {
		s.Note = "the node does not carry " + LabelGKEMetadata + `: "true"`
	}
	if n.OS == "windows" {
		s.Management = capability.Management{Manager: mgr, ManagerEvidence: mev, Observed: capability.NotObserved,
			ObservedEvidence: "on Windows nodes the metadata server is a native Windows service, so no pod is expected"}
		s.Health = "runs as a Windows service; not directly observed"
		return s
	}
	s.Management = capability.Management{Manager: mgr, ManagerEvidence: mev, Observed: capability.Observed, ObservedEvidence: "its pod on this node"}
	if !read {
		s.Health = "the metadata server's pods were not read"
		return s
	}
	for _, p := range mdPods {
		if p.Node != n.Name {
			continue
		}
		if p.Ready {
			s.Health = "metadata server pod " + p.Name + " is ready on this node"
		} else {
			s.Health, s.Fault = "metadata server pod "+p.Name+" on this node is not ready", true
		}
		return s
	}
	s.Health = "no metadata server pod on this node"
	return s
}

// Webhook is a mutating webhook configuration as the Observer's admission
// kinds read it (S3a's projection).
type Webhook struct {
	Name    string
	Present bool
	Read    bool // the admission kind was read completely
	// URL: served by clientConfig.url; Service: "<ns>/<name>" when served
	// by an in-cluster Service.
	URL        bool
	Service    string
	FailClosed bool
	Selectors  bool // an object or namespace selector narrows it
}

// WebhookSide is the webhook's management and health reading (C2's table).
// A Service-served webhook links to its Admission verdict, never repeats it.
func WebhookSide(w Webhook) Side {
	s := Side{Component: "webhook " + w.Name}
	switch {
	case !w.Read:
		s.Management = capability.Management{Manager: capability.ManagerUnknown, Observed: capability.NotObserved, ObservedEvidence: "the webhook configurations were not read"}
		s.Health = "not read"
	case !w.Present:
		s.Management = capability.Management{Manager: capability.ManagerUnknown, Observed: capability.NotObserved, ObservedEvidence: "no such configuration"}
		s.Health = "not installed: no " + w.Name + " configuration"
	case w.URL:
		s.Management = capability.Management{Manager: capability.ManagerUnknown, ManagerEvidence: "served by URL: not an in-cluster Service, which proves nothing of who runs it",
			Observed: capability.NotObserved, ObservedEvidence: "served outside the cluster"}
		s.Health = s.Management.Unobserved()
	default:
		s.Management = capability.Management{Manager: capability.ManagerUnknown, ManagerEvidence: "served by the in-cluster Service " + w.Service,
			Observed: capability.Observed, ObservedEvidence: "its backend verdict in the Admission lens"}
		s.Health = "see its backend in the Admission lens"
	}
	if w.Present && w.FailClosed {
		scope := "every pod creation it matches"
		if w.Selectors {
			scope = "pod creations its selectors match"
		}
		s.Note = "failurePolicy Fail: while its backend is unavailable, " + scope + " are refused."
	}
	return s
}

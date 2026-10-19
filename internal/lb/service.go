package lb

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/health"
)

// Grace is how long a LoadBalancer Service may wait for an address before
// it is a fault (S4 §3).
const Grace = 10 * time.Minute

// ErrorSet stands in a projected ingress port's error: the rule reads
// whether one is set, never its text (S4 §2).
const ErrorSet = "(set)"

// Address families.
const (
	FamilyV4 = "IPv4"
	FamilyV6 = "IPv6"
)

// AddressAnnotations is the fixed allowlist of annotations whose values
// request an address or a pool (S4 §2); every other annotation is dropped.
var AddressAnnotations = []string{
	"metallb.io/address-pool", "metallb.universe.tf/address-pool",
	"metallb.io/loadBalancerIPs", "metallb.universe.tf/loadBalancerIPs",
	"lbipam.cilium.io/ips", "io.cilium/lb-ipam-ips", "lbipam.cilium.io/sharing-key",
	"kube-vip.io/loadbalancerIPs",
}

// IsLoadBalancer reports a Service of type LoadBalancer.
func IsLoadBalancer(s *corev1.Service) bool { return s.Spec.Type == corev1.ServiceTypeLoadBalancer }

// Allocation is a LoadBalancer Service's allocation, classified over the
// whole ingress list (S4 §2, §3): the display caps come after.
type Allocation struct {
	Allocated bool
	// IPs and Hostnames are every allocated entry's, in order.
	IPs, Hostnames []string
	Entries        int // ingress entries, empty ones included
	Families       map[string]bool
	// PortError: an ingress port carries an error; PortsErrorCond: the
	// LoadBalancerPortsError condition is True.
	PortError, PortsErrorCond bool
}

// HostnameOnly reports an allocation by hostname alone: its families are
// unknown.
func (a Allocation) HostnameOnly() bool { return a.Allocated && len(a.IPs) == 0 }

// AllocationOf classifies a Service's status. An entry with neither an IP
// nor a hostname is no allocation (revision 1).
func AllocationOf(s *corev1.Service) Allocation {
	a := Allocation{Families: map[string]bool{}}
	for _, in := range s.Status.LoadBalancer.Ingress {
		a.Entries++
		if in.IP != "" {
			a.Allocated = true
			a.IPs = append(a.IPs, in.IP)
			if ip := net.ParseIP(in.IP); ip != nil {
				if ip.To4() != nil {
					a.Families[FamilyV4] = true
				} else {
					a.Families[FamilyV6] = true
				}
			}
		}
		if in.Hostname != "" {
			a.Allocated = true
			a.Hostnames = append(a.Hostnames, in.Hostname)
		}
		for _, p := range in.Ports {
			if p.Error != nil && *p.Error != "" {
				a.PortError = true
			}
		}
	}
	for _, c := range s.Status.Conditions {
		if c.Type == "LoadBalancerPortsError" && c.Status == "True" {
			a.PortsErrorCond = true
		}
	}
	return a
}

// MissingFamily is the family a RequireDualStack Service lacks, "" when it
// has both or does not require both. A hostname-only allocation has
// unknown families and is never judged.
func MissingFamily(s *corev1.Service, a Allocation) string {
	if s.Spec.IPFamilyPolicy == nil || *s.Spec.IPFamilyPolicy != corev1.IPFamilyPolicyRequireDualStack || !a.Allocated || a.HostnameOnly() {
		return ""
	}
	switch {
	case !a.Families[FamilyV4]:
		return FamilyV4
	case !a.Families[FamilyV6]:
		return FamilyV6
	}
	return ""
}

// ServiceCond is a Service's condition with its freshness against the
// Service's generation; Current is false for an earlier generation's.
type ServiceCond struct {
	Type, Status, Reason, Message string
	At                            time.Time
	Current                       bool
}

// CiliumSatisfied is the Service's cilium.io/IPAMRequestSatisfied condition,
// nil when absent. Only a current one is evidence (S4 §4).
func CiliumSatisfied(s *corev1.Service) *ServiceCond {
	for _, c := range s.Status.Conditions {
		if c.Type == CondRequestSatisfied {
			return &ServiceCond{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message,
				At: c.LastTransitionTime.Time, Current: c.ObservedGeneration == s.Generation}
		}
	}
	return nil
}

// Episode is a pending Service's current episode: Start, and whether the
// tracker saw it begin (Observed) or Start is only a lower bound, the
// first observation (S4 §3).
type Episode struct {
	Start    time.Time `json:"start"`
	Observed bool      `json:"observed,omitempty"`
}

// ServiceRule judges one LoadBalancer Service (S4 §3, first match wins).
// ep is the pending episode (the caller supplies a first-seen lower bound
// when the tracker has none); reason is the diagnosis summary (§4).
func ServiceRule(s *corev1.Service, a Allocation, ep Episode, now time.Time, reason string) health.NativeEval {
	if a.Allocated {
		if a.PortError || a.PortsErrorCond {
			return health.NativeEval{Included: true, Role: health.Degraded, Label: "Port error",
				Reason: health.CapReason("Port error", "an ingress port reports an error")}
		}
		if f := MissingFamily(s, a); f != "" {
			msg := "the Service requires both families; " + f + " is not assigned"
			if c := CiliumSatisfied(s); c != nil && c.Current && c.Status == "False" {
				msg += " (Cilium: " + c.Reason + ")"
			}
			return health.NativeEval{Included: true, Role: health.Degraded, Label: "One address family", Reason: health.CapReason("One address family", msg)}
		}
		return health.NativeEval{Role: health.OK, Label: "Address assigned"}
	}
	deadline := ep.Start.Add(Grace)
	if now.Before(deadline) {
		return health.NativeEval{Role: health.Progressing, Label: "Pending", Next: health.At(deadline)}
	}
	return health.NativeEval{Included: true, Role: health.Fault, Label: "No address", Boundary: health.At(deadline),
		Reason: health.CapReason("No address", reason)}
}

// Controller names (S4 §4).
const (
	CtlMetalLB    = "MetalLB"
	CtlCiliumLB   = "Cilium LB IPAM"
	CtlCiliumNode = "Cilium node IPAM"
	CtlServiceLB  = "k3s ServiceLB"
	CtlKubeVIP    = "kube-vip"
	CtlAWSLBC     = "AWS Load Balancer Controller"
	CtlGKE        = "GKE"
	CtlCloud      = "a cloud controller"
)

// Env is what Klyx can see of the cluster's load-balancer controllers,
// from one capture (S4 §4). Each is a candidate, never an owner.
type Env struct {
	MetalLB    bool // its pool CRD served or its controller found
	CiliumLB   bool // the CiliumLoadBalancerIPPool CRD served
	K3s        bool // a node's kubelet version carries +k3s
	KubeVIP    bool // kube-vip pods present
	AWSLBC     bool // the AWS Load Balancer Controller's Deployment found
	Cloud      string
	NodesKnown bool // the nodes were read, so K3s and Cloud are evidence
}

// Candidate is one controller that may serve a Service, with its limit.
type Candidate struct {
	Controller string `json:"controller"`
	Detected   bool   `json:"detected"`
	Limit      string `json:"limit,omitempty"`
}

// Expected is the class's expected controllers (S4 §4).
type Expected struct {
	Class      string      `json:"class,omitempty"`
	Known      bool        `json:"known"`
	Candidates []Candidate `json:"candidates,omitempty"`
	// Note is the class's reading when nothing Klyx can see serves it.
	Note string `json:"note,omitempty"`
}

// Class is a Service's loadBalancerClass, "" when unset.
func Class(s *corev1.Service) string {
	if s.Spec.LoadBalancerClass == nil {
		return ""
	}
	return *s.Spec.LoadBalancerClass
}

// knownClasses maps the known classes to their controller and limit.
var knownClasses = map[string]Candidate{
	"io.cilium/bgp-control-plane":                   {Controller: CtlCiliumLB, Limit: "served only when that Cilium feature is enabled"},
	"io.cilium/l2-announcer":                        {Controller: CtlCiliumLB, Limit: "served only when that Cilium feature is enabled"},
	"io.cilium/node":                                {Controller: CtlCiliumNode, Limit: "served only when node IPAM is enabled; it needs no pool"},
	"kube-vip.io/kube-vip-class":                    {Controller: CtlKubeVIP},
	"service.k8s.aws/nlb":                           {Controller: CtlAWSLBC},
	"networking.gke.io/l4-regional-internal":        {Controller: CtlGKE},
	"networking.gke.io/l4-regional-external":        {Controller: CtlGKE},
	"networking.gke.io/l4-regional-internal-legacy": {Controller: CtlGKE},
	"networking.gke.io/l4-regional-external-legacy": {Controller: CtlGKE},
}

func (e Env) detected(ctl string) bool {
	switch ctl {
	case CtlMetalLB:
		return e.MetalLB
	case CtlCiliumLB, CtlCiliumNode:
		return e.CiliumLB
	case CtlServiceLB:
		return e.K3s
	case CtlKubeVIP:
		return e.KubeVIP
	case CtlAWSLBC:
		return e.AWSLBC
	case CtlGKE:
		return e.Cloud == "gce"
	}
	return false
}

// ExpectedFor maps a class onto the controllers that may serve it.
func ExpectedFor(class string, env Env) Expected {
	ex := Expected{Class: class}
	if class == "" {
		ex.Known = true
		add := func(ok bool, ctl, limit string) {
			if ok {
				ex.Candidates = append(ex.Candidates, Candidate{Controller: ctl, Detected: true, Limit: limit})
			}
		}
		add(env.MetalLB, CtlMetalLB, "unless started with --lb-class, which Klyx cannot read")
		add(env.CiliumLB, CtlCiliumLB, "under Cilium's default default-lb-service-ipam=lbipam")
		add(env.K3s, CtlServiceLB, "unless disabled with --disable=servicelb, which Klyx cannot see")
		add(env.KubeVIP, CtlKubeVIP, "it serves Services without a class by default")
		if env.Cloud != "" {
			ex.Candidates = append(ex.Candidates, Candidate{Controller: CtlCloud + " (" + env.Cloud + ")", Detected: true,
				Limit: "the cloud's service controller acts only on Services without a class"})
		}
		if len(ex.Candidates) == 0 {
			ex.Note = "no controller Klyx can see serves Services without a class"
		}
		return ex
	}
	if c, ok := knownClasses[class]; ok {
		ex.Known = true
		c.Detected = env.detected(c.Controller)
		ex.Candidates = []Candidate{c}
		if !c.Detected {
			ex.Note = "no " + c.Controller + " detected for class " + class
		}
		return ex
	}
	ex.Note = "no controller Klyx knows serves class " + class
	if env.MetalLB || env.KubeVIP {
		ex.Note += "; a detected MetalLB or kube-vip may be configured for it, through settings Klyx does not read"
	}
	return ex
}

// Cloud providerID schemes (a kind:// or k3s:// node is not a cloud).
var cloudSchemes = map[string]bool{"aws": true, "azure": true, "gce": true, "openstack": true, "vsphere": true, "oci": true,
	"ibm": true, "digitalocean": true, "linode": true, "hcloud": true, "equinixmetal": true, "packet": true, "alicloud": true}

// CloudOf is the nodes' cloud providerID scheme, "" when none is a cloud's.
func CloudOf(nodes []*corev1.Node) string {
	for _, n := range nodes {
		if i := strings.Index(n.Spec.ProviderID, "://"); i > 0 && cloudSchemes[n.Spec.ProviderID[:i]] {
			return n.Spec.ProviderID[:i]
		}
	}
	return ""
}

// K3sOf reports a node whose kubelet version carries +k3s (S4 §4.1).
func K3sOf(nodes []*corev1.Node) bool {
	for _, n := range nodes {
		if strings.Contains(n.Status.NodeInfo.KubeletVersion, "+k3s") {
			return true
		}
	}
	return false
}

// SvcLBName is the ServiceLB DaemonSet k3s generates for a Service
// (servicelb.go 780-792): svclb-<name, trimmed to 48 without a trailing
// hyphen>-<the UID's first 8>.
func SvcLBName(s *corev1.Service) string {
	name := s.Name
	if len(name) > 48 {
		cut := 48
		for cut > 0 && name[cut-1] == '-' {
			cut--
		}
		name = name[:cut]
	}
	uid := string(s.UID)
	if len(uid) > 8 {
		uid = uid[:8]
	}
	return "svclb-" + name + "-" + uid
}

// ServiceLB's pod labels and namespace.
const (
	SvcLBNamespace      = "kube-system"
	SvcLBNameLabel      = "svccontroller.k3s.cattle.io/svcname"
	SvcLBNamespaceLabel = "svccontroller.k3s.cattle.io/svcnamespace"
)

// SvcLB is a Service's ServiceLB evidence (S4 §4): its own DaemonSet and
// the pods it owns, and pods carrying its name labels owned elsewhere.
type SvcLB struct {
	DaemonSet string `json:"daemonSet,omitempty"` // found under the generated name
	Pods      int    `json:"pods"`
	Ready     int    `json:"ready"`
	Pending   int    `json:"pending"`
	// HostPortConflict: a Pending pod's scheduler reason names no free port.
	HostPortConflict bool   `json:"hostPortConflict,omitempty"`
	PendingReason    string `json:"pendingReason,omitempty"`
	// Candidates are pods with the Service's name labels owned by another
	// DaemonSet: unproven, a replaced Service's.
	Candidates int `json:"candidates,omitempty"`
}

// PodOwned reports whether a pod is owned by the given UID.
func PodOwned(p *corev1.Pod, uid string) bool {
	for _, o := range p.OwnerReferences {
		if string(o.UID) == uid {
			return true
		}
	}
	return false
}

// SvcLBOf joins a Service's ServiceLB DaemonSet (by its generated name in
// kube-system) and pods (by owner UID).
func SvcLBOf(s *corev1.Service, dsName, dsUID string, pods []*corev1.Pod) *SvcLB {
	ev := &SvcLB{}
	if dsUID != "" {
		ev.DaemonSet = dsName
	}
	for _, p := range pods {
		if p.Namespace != SvcLBNamespace {
			continue
		}
		owned := dsUID != "" && PodOwned(p, dsUID)
		if !owned {
			if p.Labels[SvcLBNameLabel] == s.Name && p.Labels[SvcLBNamespaceLabel] == s.Namespace {
				ev.Candidates++
			}
			continue
		}
		ev.Pods++
		if podReady(p) {
			ev.Ready++
		}
		if p.Status.Phase == corev1.PodPending {
			ev.Pending++
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
					if ev.PendingReason == "" {
						ev.PendingReason = clip(c.Reason, 64)
					}
					if strings.Contains(c.Message, "didn't have free ports") {
						ev.HostPortConflict = true
					}
				}
			}
		}
	}
	if ev.DaemonSet == "" && ev.Candidates == 0 {
		return nil
	}
	return ev
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Event is one Warning event joined to a Service by its UID.
type Event struct {
	Reason   string `json:"reason"`
	Message  string `json:"message,omitempty"`
	Count    int32  `json:"count,omitempty"`
	LastSeen int64  `json:"lastSeen,omitempty"`
}

// RequestedPool is the pool a Service names (metallb.io/address-pool),
// joined inside MetalLB's instance namespace (S4 §4).
type RequestedPool struct {
	Name string `json:"name"`
	// State: "found", "missing" (from a complete read of the one instance
	// namespace), or "unverified" (no single instance namespace known, or
	// the read not complete).
	State     string `json:"state"`
	Namespace string `json:"namespace,omitempty"`
	Label     string `json:"label,omitempty"` // the found pool's verdict label
}

// PoolAnnotation is the requested MetalLB pool, "" when none.
func PoolAnnotation(s *corev1.Service) string {
	for _, k := range []string{"metallb.io/address-pool", "metallb.universe.tf/address-pool"} {
		if v := s.Annotations[k]; v != "" {
			return v
		}
	}
	return ""
}

// AddressRequest is the Service's allowlisted address request: the
// annotations' keys and values, and the deprecated spec.loadBalancerIP.
func AddressRequest(s *corev1.Service) []string {
	var out []string
	for _, k := range AddressAnnotations {
		if v := s.Annotations[k]; v != "" && k != "metallb.io/address-pool" && k != "metallb.universe.tf/address-pool" {
			out = append(out, k+"="+clip(v, 128))
		}
	}
	if s.Spec.LoadBalancerIP != "" {
		out = append(out, "spec.loadBalancerIP="+clip(s.Spec.LoadBalancerIP, 64)+" (deprecated)")
	}
	return out
}

// Diagnosis is a pending Service's evidence (S4 §4): the expected
// controller, each source's evidence and the silent case that fits.
type Diagnosis struct {
	Expected Expected `json:"expected"`
	Events   []Event  `json:"events,omitempty"`
	// EventsRead: "complete", "capped" (the snapshot's cap, said), or
	// "unverified".
	EventsRead string         `json:"eventsRead"`
	SvcLB      *SvcLB         `json:"svclb,omitempty"`
	SvcLBRead  bool           `json:"svclbRead"`
	Cilium     *ServiceCond   `json:"cilium,omitempty"`
	Request    []string       `json:"request,omitempty"`
	Pool       *RequestedPool `json:"pool,omitempty"`
	// Silent is the silent case that fits when nothing above speaks.
	Silent string `json:"silent,omitempty"`
	// Summary is the one line the fault's reason carries.
	Summary string `json:"summary"`
}

// DiagnosisInput is one Service's evidence, joined by the caller.
type DiagnosisInput struct {
	Svc        *corev1.Service
	Env        Env
	Events     []Event // joined by involved-object UID, newest first
	EventsRead string
	SvcLB      *SvcLB
	SvcLBRead  bool
	Pool       *RequestedPool
	// NoPools names the detected controllers that have no pool, from a
	// complete read (MetalLB, Cilium LB IPAM): they stay silent.
	NoPools []string
}

// Diagnose assembles a Service's diagnosis and its summary line (S4 §4).
func Diagnose(in DiagnosisInput) Diagnosis {
	s := in.Svc
	d := Diagnosis{Expected: ExpectedFor(Class(s), in.Env), Events: in.Events, EventsRead: in.EventsRead,
		SvcLB: in.SvcLB, SvcLBRead: in.SvcLBRead, Cilium: CiliumSatisfied(s), Request: AddressRequest(s), Pool: in.Pool}
	if len(d.Events) > 8 {
		d.Events = d.Events[:8]
	}
	switch {
	case len(in.Events) > 0:
		e := in.Events[0]
		d.Summary = e.Reason
		if e.Message != "" {
			d.Summary += ": " + e.Message
		}
	case d.Cilium != nil && d.Cilium.Current && d.Cilium.Status == "False":
		d.Summary = "Cilium: " + d.Cilium.Reason
		if d.Cilium.Message != "" {
			d.Summary += ": " + d.Cilium.Message
		}
	case in.SvcLB != nil && in.SvcLB.HostPortConflict:
		d.Summary = "ServiceLB: pods pending, no free host port on any node"
	case in.SvcLB != nil && in.SvcLB.DaemonSet != "" && in.SvcLB.Ready == 0:
		d.Summary = fmt.Sprintf("ServiceLB: %d pod(s), none ready", in.SvcLB.Pods)
	case in.Pool != nil && in.Pool.State == "missing":
		d.Summary = "the requested pool " + in.Pool.Name + " does not exist"
	}
	if d.Summary == "" {
		d.Silent = silentCase(d.Expected, in)
		d.Summary = d.Silent
	}
	return d
}

// silentCase names the silent case that fits a pending Service with no
// evidence (S4 §4).
func silentCase(ex Expected, in DiagnosisInput) string {
	if ex.Note != "" {
		return ex.Note
	}
	if len(in.NoPools) > 0 {
		sort.Strings(in.NoPools)
		return strings.Join(in.NoPools, " and ") + " has no pool: it stays silent"
	}
	for _, c := range ex.Candidates {
		if c.Controller == CtlCiliumLB && ex.Class != "" {
			return "class " + ex.Class + " is " + c.Limit
		}
	}
	if in.Env.K3s && Class(in.Svc) == "" && in.SvcLBRead && in.SvcLB == nil {
		return "k3s ServiceLB may be disabled (--disable=servicelb), which Klyx cannot see"
	}
	return "the expected controller wrote no event or condition for it"
}

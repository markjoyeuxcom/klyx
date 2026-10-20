package karpenter

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Adapter is a provider's class adapter (coverage S10 §1). Any other
// nodeClassRef is "provider not supported".
type Adapter string

const (
	AdapterEC2     Adapter = "ec2"
	AdapterAKS     Adapter = "aks"
	AdapterEKSAuto Adapter = "eks-auto"
)

// ClassKind is one supported class kind, pinned to the version S10 read
// (§2): any other served version is "unsupported version", never a decode
// of an unread schema.
type ClassKind struct {
	Adapter  Adapter
	Group    string
	Version  string
	Resource string
	Kind     string
}

// ClassKinds are the three adapters' kinds.
var ClassKinds = []ClassKind{
	{AdapterEC2, "karpenter.k8s.aws", "v1", "ec2nodeclasses", "EC2NodeClass"},
	{AdapterAKS, "karpenter.azure.com", "v1beta1", "aksnodeclasses", "AKSNodeClass"},
	{AdapterEKSAuto, "eks.amazonaws.com", "v1", "nodeclasses", "NodeClass"},
}

// KindOf returns the supported class kind a reference names: group and
// kind both, exactly (a wrong Auto Mode reference does not resolve, §1.4).
func KindOf(group, kind string) (ClassKind, bool) {
	for _, k := range ClassKinds {
		if k.Group == group && k.Kind == kind {
			return k, true
		}
	}
	return ClassKind{}, false
}

// KindFor returns the adapter's class kind.
func KindFor(a Adapter) (ClassKind, bool) {
	for _, k := range ClassKinds {
		if k.Adapter == a {
			return k, true
		}
	}
	return ClassKind{}, false
}

// dependents are each adapter's Ready dependents in the provider's order
// (§1.2-1.4): the reading names the first one False for the current
// generation.
var dependents = map[Adapter][]string{
	AdapterEC2: {"AMIsReady", "SubnetsReady", "SecurityGroupsReady", "InstanceProfileReady",
		"ValidationSucceeded", "PlacementGroupReady", "CapacityReservationsReady"},
	AdapterAKS: {"ImagesReady", "KubernetesVersionReady", "SubnetsReady", "ValidationSucceeded",
		"LocalDNSReady", "CapacityReservationGroupReady"},
	// Auto Mode's set is community evidence, never assumed complete: a False
	// type outside it is still named (§1.4).
	AdapterEKSAuto: {"SubnetsReady", "SecurityGroupsReady", "InstanceProfileReady",
		"CapacityReservationsReady", "ValidationSucceeded"},
}

// NodeClass is one provider class: inventory, never counted (§2, §4). Its
// authoritative fields come from every condition, before the display cap.
type NodeClass struct {
	Meta    Meta    `json:"meta"`
	Adapter Adapter `json:"adapter"`
	// Ready is the Ready condition; nil when absent.
	Ready *Cond `json:"ready,omitempty"`
	// Failing is the current failure: the first False dependent in the
	// provider's order whose observedGeneration is not behind the
	// generation, else the first other such False condition; nil when none.
	// A condition reported for an older generation never hides it.
	Failing *Cond `json:"failing,omitempty"`
	// Historical is the first False condition, in the same order, reported
	// for an older generation, without its message: context beside the
	// reading, never the reading itself (a reconciler can leave its
	// condition unchanged while another writes a current one, and an
	// optional dependent can outlive its feature gate).
	Historical *Cond `json:"historical,omitempty"`
	// Syncing: Ready's observedGeneration is behind the generation, so the
	// aggregate is not the current reading (§3 rule 2).
	Syncing bool `json:"syncing,omitempty"`
	// failingDependent: Failing is one of the adapter's Ready dependents.
	failingDependent bool
	// Unknown lists condition types the adapter does not know, never dropped
	// (§4): at most 8 named, with the exact count.
	Unknown      []string   `json:"unknown,omitempty"`
	UnknownTotal int        `json:"unknownTotal,omitempty"`
	Conds        Conds      `json:"conds"`
	Facts        ClassFacts `json:"facts"`
	size         int
}

// ClassFacts are a class's provider facts (§4): counts and names, never a
// subnet, security group, AMI or ARM ID. A nil count is not reported.
type ClassFacts struct {
	// ec2 and eks-auto: the resolved counts in status.
	Subnets        *int `json:"subnets,omitempty"`
	SecurityGroups *int `json:"securityGroups,omitempty"`
	// ec2
	AMIs          *int `json:"amis,omitempty"`
	DeprecatedAMI bool `json:"deprecatedAmi,omitempty"`
	// eks-auto: pod subnet selector terms are set.
	PodSubnets bool `json:"podSubnets,omitempty"`
	// aks
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	Images            *int   `json:"images,omitempty"`
	ImageFamily       string `json:"imageFamily,omitempty"`
	// SubnetSet: spec.vnetSubnetID is set. The ID itself is never kept.
	SubnetSet bool `json:"subnetSet,omitempty"`
	// Role and InstanceProfile are local only: outside every AI and chat
	// allowlist (§8).
	Role            string `json:"role,omitempty"`
	InstanceProfile string `json:"instanceProfile,omitempty"`
}

// ParseNodeClass projects one class of the adapter's kind.
func ParseNodeClass(u *unstructured.Unstructured, a Adapter) NodeClass {
	c := NodeClass{Meta: metaOf(u, true), Adapter: a}
	all := condsOf(u)
	c.Conds = display(all)
	var idx [3]int
	c.Ready, idx[0] = find(all, condReady)
	// each False condition is the current failure or historical context,
	// by its own generation: the whole object is read before the display cap
	pick := func(f *Cond, i int, dependent bool) {
		switch {
		case f == nil || f.Status != "False":
		case behind(f, c.Meta.Generation):
			if c.Historical == nil {
				// the note is Klyx's wording: the message is not kept
				h := *f
				h.Message = ""
				c.Historical, idx[2] = &h, i
			}
		case c.Failing == nil:
			c.Failing, idx[1], c.failingDependent = f, i, dependent
		}
	}
	known := map[string]bool{condReady: true}
	for _, t := range dependents[a] {
		known[t] = true
		f, i := find(all, t)
		pick(f, i, true)
	}
	for i := range all {
		if known[all[i].Type] {
			continue
		}
		f := all[i]
		pick(&f, i, false)
		c.UnknownTotal++
		if len(c.Unknown) < maxUnknown {
			c.Unknown = append(c.Unknown, all[i].Type)
		}
	}
	c.Syncing = behind(c.Ready, c.Meta.Generation)
	c.Facts = factsOf(u, a)
	c.size = classSize(&c, idx)
	return c
}

// behind: the condition reports a generation, and it is older.
func behind(c *Cond, generation int64) bool {
	return c != nil && c.ObservedGeneration > 0 && c.ObservedGeneration < generation
}

func count(u *unstructured.Unstructured, path ...string) *int {
	l, ok, _ := unstructured.NestedSlice(u.Object, path...)
	if !ok {
		return nil
	}
	n := len(l)
	return &n
}

func str(u *unstructured.Unstructured, n int, path ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, path...)
	return clip(s, n)
}

func factsOf(u *unstructured.Unstructured, a Adapter) ClassFacts {
	var f ClassFacts
	switch a {
	case AdapterEC2:
		f.Subnets, f.SecurityGroups, f.AMIs = count(u, "status", "subnets"), count(u, "status", "securityGroups"), count(u, "status", "amis")
		amis, _, _ := unstructured.NestedSlice(u.Object, "status", "amis")
		for _, ami := range amis {
			if m, ok := ami.(map[string]any); ok && m["deprecated"] == true {
				f.DeprecatedAMI = true
			}
		}
		f.Role = str(u, maxName, "spec", "role")
		if f.InstanceProfile = str(u, maxName, "status", "instanceProfile"); f.InstanceProfile == "" {
			f.InstanceProfile = str(u, maxName, "spec", "instanceProfile")
		}
	case AdapterAKS:
		f.KubernetesVersion = str(u, maxToken, "status", "kubernetesVersion")
		f.Images = count(u, "status", "images")
		f.ImageFamily = str(u, maxToken, "spec", "imageFamily")
		id, _, _ := unstructured.NestedString(u.Object, "spec", "vnetSubnetID")
		f.SubnetSet = id != ""
	case AdapterEKSAuto:
		f.Subnets, f.SecurityGroups = count(u, "status", "subnets"), count(u, "status", "securityGroups")
		terms, _, _ := unstructured.NestedSlice(u.Object, "spec", "podSubnetSelectorTerms")
		f.PodSubnets = len(terms) > 0
		f.Role = str(u, maxName, "spec", "role")
	}
	return f
}

// Class reading states (§4).
const (
	ClassReady       = "ready"
	ClassNotReady    = "not ready"
	ClassSyncing     = "syncing"
	ClassTerminating = "terminating"
	ClassUnknown     = "unknown"
)

// ClassReading is a class's own reading: display evidence beside its row,
// never a verdict (a class row carries no NativeEval, §2). Text and Note
// are Klyx's fixed wording; Native is the provider's, for the local
// inspector only.
type ClassReading struct {
	State  string `json:"state"`
	Text   string `json:"text,omitempty"`
	Native string `json:"native,omitempty"`
	// Note names a False condition reported for an older generation:
	// historical context, never the reading.
	Note string `json:"note,omitempty"`
	cond *Cond
}

// Read is the class's reading, from current evidence over the whole object:
// terminating; then a current Ready True (an old False dependent cannot
// hold it); then a current Ready False, named by its current failure, else
// by Ready itself; then a current False dependent while the aggregate is
// behind, absent or Unknown (a current failure is never hidden); then
// syncing (never its old Ready); then unknown.
func (c *NodeClass) Read() ClassReading {
	if c.Meta.Deleting {
		return ClassReading{State: ClassTerminating, Text: "the NodeClass is being deleted"}
	}
	r := c.read()
	if h := c.Historical; h != nil {
		r.Note = Phrase(h) + " was reported for generation " + itoa(h.ObservedGeneration) + "; the NodeClass is at generation " + itoa(c.Meta.Generation)
	}
	return r
}

func (c *NodeClass) read() ClassReading {
	current := c.Ready != nil && !c.Syncing
	notReady := func(at *Cond) ClassReading {
		return ClassReading{State: ClassNotReady, Text: Phrase(at), Native: Native(at), cond: at}
	}
	switch {
	case current && c.Ready.Status == "True":
		return ClassReading{State: ClassReady, cond: c.Ready}
	case current && c.Ready.Status == "False" && c.Failing != nil:
		return notReady(c.Failing)
	case current && c.Ready.Status == "False":
		return notReady(c.Ready)
	case c.Failing != nil && c.failingDependent:
		return notReady(c.Failing)
	case c.Syncing:
		return ClassReading{State: ClassSyncing, Text: "its Ready condition is behind generation " + itoa(c.Meta.Generation)}
	case c.Ready == nil:
		return ClassReading{State: ClassUnknown, Text: "Ready not reported"}
	}
	return ClassReading{State: ClassUnknown, Text: Phrase(c.Ready), Native: Native(c.Ready), cond: c.Ready}
}

func classSize(c *NodeClass, idx [3]int) int {
	n := metaSize(c.Meta) + 104 + len(c.Adapter) + condsSize(c.Conds)
	for i, a := range []*Cond{c.Ready, c.Failing, c.Historical} {
		n += authSize(a, idx[i])
	}
	for _, t := range c.Unknown {
		n += 16 + len(t)
	}
	f := &c.Facts
	n += 64 + len(f.KubernetesVersion) + len(f.ImageFamily) + len(f.Role) + len(f.InstanceProfile)
	return n
}

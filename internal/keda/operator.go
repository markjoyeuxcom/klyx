package keda

import (
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// The env names §6.1 reads from an operator's running pods: an allowlist,
// never any other env.
const (
	EnvWatchNamespace      = "WATCH_NAMESPACE"
	EnvWatchSelector       = "WATCH_LABEL_SELECTOR"
	EnvWatchAuthSelector   = "WATCH_LABEL_SELECTOR_FOR_TRIGGERAUTH"
	EnvClusterObjectNS     = "KEDA_CLUSTER_OBJECT_NAMESPACE"
	EnvRestrictSecretAcces = "KEDA_RESTRICT_SECRET_ACCESS"
)

// OperatorEnvNames is the allowlist, in a stable order.
var OperatorEnvNames = []string{EnvWatchNamespace, EnvWatchSelector, EnvWatchAuthSelector, EnvClusterObjectNS, EnvRestrictSecretAcces}

// EnvEntry is one allowlisted name as a container declares it.
type EnvEntry struct {
	Literal   bool   // an explicit `value`
	Value     string // the literal value
	ValueFrom bool   // an explicit `valueFrom`: its source is never read
}

// OperatorPod is one running operator pod, as §6.1 reads it.
type OperatorPod struct {
	Name           string
	Namespace      string
	ServiceAccount string
	Image          string
	Env            map[string]EnvEntry // the allowlisted names explicitly declared
	EnvFrom        bool                // the container has any envFrom
	Readable       bool
}

// Resolution is how one setting resolved.
type Resolution int

const (
	Unresolved Resolution = iota
	Resolved              // a literal value, or KEDA's default with proof
)

// Setting is one allowlisted name's resolved value (Unset: KEDA's default).
type Setting struct {
	Res    Resolution
	Value  string
	Unset  bool
	Reason string // why it is unresolved
}

// resolveOne resolves one name in one pod (§6.1).
func resolveOne(p OperatorPod, name string) Setting {
	if !p.Readable {
		return Setting{Reason: "the pod could not be read"}
	}
	if e, ok := p.Env[name]; ok {
		if e.ValueFrom {
			return Setting{Reason: name + " comes from valueFrom, which Klyx does not read"}
		}
		if e.Literal {
			return Setting{Res: Resolved, Value: e.Value}
		}
	}
	if p.EnvFrom {
		return Setting{Reason: "the container has envFrom, which may set " + name}
	}
	if !IsOperatorImage(p.Image) {
		return Setting{Reason: "the image is not KEDA's own, so it may set " + name}
	}
	// KEDA's own image sets no environment (its Dockerfile, distroless)
	return Setting{Res: Resolved, Unset: true}
}

// Config is a candidate operator's configuration from its running pods.
type Config struct {
	Name, Namespace string
	ServiceAccount  string
	Version         Version
	Settings        map[string]Setting
	// Disagree lists settings the running pods disagree on (a rollout).
	Disagree []string
	Pods     int
}

// ConfigOf resolves a candidate from its running pods: a setting the pods
// disagree on is unresolved, with both values listed (§6.1).
func ConfigOf(name, namespace string, pods []OperatorPod) Config {
	c := Config{Name: name, Namespace: namespace, Settings: map[string]Setting{}, Pods: len(pods)}
	if len(pods) == 0 {
		for _, n := range OperatorEnvNames {
			c.Settings[n] = Setting{Reason: "no running pod was read"}
		}
		return c
	}
	sas, versions := map[string]bool{}, map[string]Version{}
	for _, p := range pods {
		sas[p.ServiceAccount] = true
		v := VersionOfImage(p.Image)
		versions[v.String()] = v
	}
	if len(sas) == 1 {
		c.ServiceAccount = pods[0].ServiceAccount
	}
	if len(versions) == 1 {
		for _, v := range versions {
			c.Version = v
		}
	}
	for _, n := range OperatorEnvNames {
		first := resolveOne(pods[0], n)
		same := true
		for _, p := range pods[1:] {
			if s := resolveOne(p, n); s != first {
				same = false
			}
		}
		if !same {
			first = Setting{Reason: "the running pods disagree on " + n}
			c.Disagree = append(c.Disagree, n)
		}
		c.Settings[n] = first
	}
	return c
}

// Incomplete is the candidate when not every one of its running pods was
// read (a continued list, more pods than the read keeps): an omitted pod
// may run another scope, ServiceAccount or version, so none is resolved.
func (c Config) Incomplete(why string) Config {
	out := Config{Name: c.Name, Namespace: c.Namespace, Settings: map[string]Setting{}, Disagree: c.Disagree, Pods: c.Pods}
	for _, n := range OperatorEnvNames {
		out.Settings[n] = Setting{Reason: why}
	}
	return out
}

// emptyScope is KEDA's empty forms: "" and the literal two characters ""
// (pkg/util/watch.go).
func emptyScope(v string) bool { return v == "" || v == `""` }

// Tri is a three-valued answer.
type Tri int

const (
	Unknown Tri = iota
	Yes
	No
)

// Watches reports whether a candidate handles a user in namespace ns with
// labels lbls (cut: the projection cut them): its namespace scope and its
// WATCH_LABEL_SELECTOR, never the authentication selector (§6.1).
func (c Config) Watches(ns string, lbls map[string]string, cut bool) Tri {
	w := c.Settings[EnvWatchNamespace]
	if w.Res != Resolved {
		return Unknown
	}
	if !w.Unset && !emptyScope(w.Value) {
		in := false
		for _, n := range strings.Split(w.Value, ",") {
			in = in || n == ns
		}
		if !in {
			return No
		}
	}
	return selects(c.Settings[EnvWatchSelector], lbls, cut)
}

// SeesAuth reports whether the handler's cache holds an authentication
// with labels lbls: WATCH_LABEL_SELECTOR_FOR_TRIGGERAUTH (§6.1).
func (c Config) SeesAuth(lbls map[string]string, cut bool) Tri {
	return selects(c.Settings[EnvWatchAuthSelector], lbls, cut)
}

func selects(s Setting, lbls map[string]string, cut bool) Tri {
	if s.Res != Resolved {
		return Unknown
	}
	if s.Unset || emptyScope(s.Value) {
		return Yes
	}
	ls, err := metav1.ParseToLabelSelector(s.Value)
	if err != nil {
		return Unknown // an invalid selector is unresolved, never an exclusion
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return Unknown
	}
	if !cut {
		if sel.Matches(labels.Set(lbls)) {
			return Yes
		}
		return No
	}
	// A cut label map proves only what its retained keys say: a requirement
	// on a key it holds is decided by that value; one on a key it lacks -
	// positive or negative ("!hidden", "hidden!=x") - might be decided
	// either way by an omitted label.
	reqs, _ := sel.Requirements()
	out := Yes
	for _, r := range reqs {
		if _, held := lbls[r.Key()]; !held {
			out = Unknown
			continue
		}
		if !r.Matches(labels.Set(lbls)) {
			return No
		}
	}
	return out
}

// Handling is who handles one user (§6.1).
type Handling struct {
	Handler    *Config  // exactly one resolved candidate that applies
	None       bool     // every candidate resolved and none applies
	Candidates []string // when unverified: the candidates that might apply
	// Unread: candidates exist past the read (the cap), any of which might
	// apply - never a handler, never none.
	Unread bool
}

// HandlerOf decides a user's handling operator from its own scope: one
// that applies is the handler; none, with every candidate read and
// resolved, is none; anything else is unverified, the candidates listed,
// never a pick. unread says candidates exist that were not read.
func HandlerOf(cands []Config, unread bool, ns string, lbls map[string]string, cut bool) Handling {
	var yes []int
	var maybe []string
	for i, c := range cands {
		switch c.Watches(ns, lbls, cut) {
		case Yes:
			yes = append(yes, i)
			maybe = append(maybe, c.Namespace+"/"+c.Name)
		case Unknown:
			maybe = append(maybe, c.Namespace+"/"+c.Name)
		}
	}
	sort.Strings(maybe)
	switch {
	case unread:
		return Handling{Candidates: maybe, Unread: true}
	case len(yes) == 1 && len(maybe) == 1:
		return Handling{Handler: &cands[yes[0]]}
	case len(maybe) == 0:
		return Handling{None: true}
	}
	return Handling{Candidates: maybe}
}

// ReadNamespace is where the handler reads one referenced object (§6.2's
// table): ok false when the namespace cannot be resolved.
func (c Config) ReadNamespace(auth *Auth, ref Ref) (string, bool) {
	clusterNS := func() (string, bool) {
		s := c.Settings[EnvClusterObjectNS]
		if s.Res != Resolved {
			return "", false
		}
		if !s.Unset && s.Value != "" {
			return s.Value, true
		}
		return c.Namespace, true
	}
	if auth.Cluster {
		return clusterNS()
	}
	if ref.Kind == "Secret" {
		r := c.Settings[EnvRestrictSecretAcces]
		if r.Res != Resolved {
			return "", false
		}
		if !r.Unset && strings.EqualFold(r.Value, "true") {
			return clusterNS()
		}
	}
	return auth.Namespace, true
}

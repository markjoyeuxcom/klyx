// Package loki is Coverage S8b's pure Loki reader: the selector branches a
// cluster's labels allow, each branch's LogQL, the query_range response,
// the page across branches and the identity mode. It holds no transport
// and no state beyond what a caller passes in.
package loki

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// The label spellings the common shippers use (S8b §3).
const (
	LabelNamespace    = "namespace"          // Kubernetes SD, k8s-monitoring
	LabelK8sNamespace = "k8s_namespace_name" // OTLP
	LabelPod          = "pod"                // Kubernetes SD (index), k8s-monitoring (structured metadata)
	LabelK8sPod       = "k8s_pod_name"       // OTLP (index by default)
	LabelContainer    = "container"
	LabelK8sContainer = "k8s_container_name"
	LabelPodUID       = "k8s_pod_uid" // OTLP structured metadata
)

// PodShape is how a branch selects the pod.
type PodShape int

const (
	// PodIndexed: the stream selector carries pod="P".
	PodIndexed PodShape = iota
	// K8sPodIndexed: the stream selector carries k8s_pod_name="P", pod="".
	K8sPodIndexed
	// PodNotIndexed: the streams carry neither pod label; a filter on
	// structured metadata names both spellings.
	PodNotIndexed
)

// Branch is one disjoint stream selection (S8b §3): every stream a pod's
// lines can be in belongs to exactly one branch.
type Branch struct {
	NSLabel string // LabelNamespace or LabelK8sNamespace
	// WithoutNamespace: a k8s_namespace_name branch when both spellings are
	// listed adds namespace="", so a stream carrying both belongs to the
	// namespace branches only.
	WithoutNamespace bool
	Pod              PodShape
}

// ErrNoNamespaceLabel: neither namespace spelling is listed for the
// cluster's streams. A selector of match alone would select the cluster.
var ErrNoNamespaceLabel = errors.New("no namespace label in this cluster's streams (looked for k8s_namespace_name, namespace)")

// Branches are the selections a labels read allows. The labels are a
// superset for the range (Loki may consult a wider span and merges names
// across streams), so they only prune: an indexed branch runs when its
// label is listed; the "neither" branch always runs, since structured
// metadata cannot be listed without scanning lines.
func Branches(labels []string) ([]Branch, error) {
	has := map[string]bool{}
	for _, l := range labels {
		has[l] = true
	}
	var spellings []string
	for _, ns := range []string{LabelNamespace, LabelK8sNamespace} {
		if has[ns] {
			spellings = append(spellings, ns)
		}
	}
	if len(spellings) == 0 {
		return nil, ErrNoNamespaceLabel
	}
	var out []Branch
	for _, ns := range spellings {
		without := ns == LabelK8sNamespace && has[LabelNamespace]
		if has[LabelPod] {
			out = append(out, Branch{NSLabel: ns, WithoutNamespace: without, Pod: PodIndexed})
		}
		if has[LabelK8sPod] {
			out = append(out, Branch{NSLabel: ns, WithoutNamespace: without, Pod: K8sPodIndexed})
		}
		out = append(out, Branch{NSLabel: ns, WithoutNamespace: without, Pod: PodNotIndexed})
	}
	return out, nil
}

// Target is the pod a history reads, with the cluster's match.
type Target struct {
	Match     map[string]string
	Namespace string
	Pod       string
	Container string // "" for every container
	UID       string // the pod's UID, for UID mode
}

// Mode is how a page set selects the pod (S8b §4).
type Mode int

const (
	// ModeName selects by namespace and name: never a UID filter, even
	// where a shipper indexed the UID.
	ModeName Mode = iota
	// ModeUID adds | k8s_pod_uid="U" to every branch.
	ModeUID
)

type matcher struct{ name, value string }

// Query is a branch's LogQL for the target in the mode: the stream
// selector, then the label filters (pod when not indexed, container, UID).
// Values are quoted with strconv.Quote. Raw: it may carry a value equal to
// the endpoint's credential, so it never leaves the reader as display text.
func Query(b Branch, t Target, m Mode) string {
	var ms []matcher
	keys := make([]string, 0, len(t.Match))
	for k := range t.Match {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ms = append(ms, matcher{k, t.Match[k]})
	}
	ms = append(ms, matcher{b.NSLabel, t.Namespace})
	if b.WithoutNamespace {
		ms = append(ms, matcher{LabelNamespace, ""})
	}
	switch b.Pod {
	case PodIndexed:
		ms = append(ms, matcher{LabelPod, t.Pod})
	case K8sPodIndexed:
		ms = append(ms, matcher{LabelK8sPod, t.Pod}, matcher{LabelPod, ""})
	case PodNotIndexed:
		ms = append(ms, matcher{LabelPod, ""}, matcher{LabelK8sPod, ""})
	}
	var sb strings.Builder
	sb.WriteString("{")
	for i, x := range ms {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(x.name)
		sb.WriteString("=")
		sb.WriteString(strconv.Quote(x.value))
	}
	sb.WriteString("}")
	if b.Pod == PodNotIndexed {
		either(&sb, LabelPod, LabelK8sPod, t.Pod)
	}
	if t.Container != "" {
		either(&sb, LabelContainer, LabelK8sContainer, t.Container)
	}
	if m == ModeUID {
		sb.WriteString(" | ")
		sb.WriteString(LabelPodUID)
		sb.WriteString("=")
		sb.WriteString(strconv.Quote(t.UID))
	}
	return sb.String()
}

// either is a label filter naming both spellings. A filter reads index
// labels and structured metadata alike, and one on a key a line lacks
// matches nothing, so naming both never widens the selection.
func either(sb *strings.Builder, a, b, v string) {
	q := strconv.Quote(v)
	sb.WriteString(" | ")
	sb.WriteString(a)
	sb.WriteString("=")
	sb.WriteString(q)
	sb.WriteString(" or ")
	sb.WriteString(b)
	sb.WriteString("=")
	sb.WriteString(q)
}

package alertmanager

import (
	"crypto/sha256"
	"encoding/hex"
)

// Subject is the Kubernetes object an alert's labels name (S8a §5): a
// correlation by label, never a join by UID.
type Subject struct {
	Kind      string // Pod, Deployment, ..., Node, Namespace; "" for a cluster alert
	Resource  string // the kind's resource (pods, deployments, ...)
	Group     string // the API group ("" core, "apps", "batch", ...)
	Namespace string // "" for a Node
	Name      string
	Basis     []string // the label names the match used
	Container string   // a pod subject's container label, as a refinement
	// AlsoNames are the further subjects the labels name, lower in the
	// table's order.
	AlsoNames []Subject
}

type subjectRule struct {
	label, kind, resource, group string
	namespaced                   bool
}

// subjectTable is the correlation table, most specific first (S8a §5):
// kube-state-metrics' and the kube-prometheus rules' label conventions.
// job, instance, service, endpoint and container never name a subject.
var subjectTable = []subjectRule{
	{"pod", "Pod", "pods", "", true},
	{"deployment", "Deployment", "deployments", "apps", true},
	{"statefulset", "StatefulSet", "statefulsets", "apps", true},
	{"daemonset", "DaemonSet", "daemonsets", "apps", true},
	{"job_name", "Job", "jobs", "batch", true},
	{"cronjob", "CronJob", "cronjobs", "batch", true},
	{"persistentvolumeclaim", "PersistentVolumeClaim", "persistentvolumeclaims", "", true},
	{"horizontalpodautoscaler", "HorizontalPodAutoscaler", "horizontalpodautoscalers", "autoscaling", true},
	{"node", "Node", "nodes", "", false},
}

// SubjectOf reads an alert's subject from its labels: the first rule of the
// table whose labels are present (a namespaced kind needs namespace too),
// the rest as AlsoNames; else the namespace alone; else a cluster alert
// (Kind "").
func SubjectOf(labels map[string]string) Subject {
	ns := labels["namespace"]
	var found []Subject
	for _, r := range subjectTable {
		name := labels[r.label]
		if name == "" || (r.namespaced && ns == "") {
			continue
		}
		s := Subject{Kind: r.kind, Resource: r.resource, Group: r.group, Name: name, Basis: []string{r.label}}
		if r.namespaced {
			s.Namespace = ns
			s.Basis = []string{"namespace", r.label}
		}
		if r.kind == "Pod" {
			s.Container = labels["container"]
		}
		found = append(found, s)
	}
	if len(found) > 0 {
		s := found[0]
		s.AlsoNames = found[1:]
		return s
	}
	if ns != "" {
		return Subject{Kind: "Namespace", Resource: "namespaces", Name: ns, Basis: []string{"namespace"}}
	}
	return Subject{}
}

// Read is one alerts snapshot read's envelope (S8a §4): the source identity
// it was read under, so even an empty read names its source, with the
// source line and every alert's raw subject (for joins) and display values.
type Read struct {
	Identity  string
	Source    string            // the source line, a display value
	Match     map[string]string // a display value (DisplayMatch)
	Alerts    []Item
	Truncated bool
	// No read time: the snapshot's freshness (its coverage's AsOf) is the
	// read's time, so an unchanged read is an unchanged item.
}

// Item is one alert of a read.
type Item struct {
	Subject Subject // raw: for the joins only
	Counted bool
	// Key is the alert's opaque key: its source identity and raw
	// fingerprint, hashed. Selection and reveal use it, so an alert of
	// another source sharing a fingerprint is never taken for this one.
	Key     string
	Display DisplayAlert
}

// AlertKey is an alert's opaque key within its source (Item.Key).
func AlertKey(identity, fingerprint string) string {
	return digest(identity + "\x00" + fingerprint)
}

// SourceKey is a source identity's opaque key: display-safe, so it can
// travel to the lens, which clears a selection when it changes.
func SourceKey(identity string) string {
	if identity == "" {
		return ""
	}
	return digest(identity)
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

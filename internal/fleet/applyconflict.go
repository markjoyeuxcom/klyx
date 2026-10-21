package fleet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// An apply the server refused because another field manager owns fields the
// edit changed (v0.15.1, from the owner's first check of v0.15.0: a Pod's
// probe owned by kube-controller-manager read "another controller (e.g.
// Flux/Argo) ... reverted on the next reconcile"). Each field names its
// manager, read from the conflict's causes, and the advice follows the kind
// of manager: a GitOps controller's field changes in Git, a Kubernetes
// controller's in the object that drives it, anyone else's with the tool that
// set it. Klyx never forces.

// Manager classes.
const (
	ManagerGitOps     = "gitops"
	ManagerKubernetes = "kubernetes"
	ManagerOther      = "other"
)

// ConflictField is one refused field and the manager that owns it.
type ConflictField struct {
	Manager, Class, Path string
}

// ConflictOwner is the object's controller, and that controller's own (one
// step up: a Pod's ReplicaSet's Deployment, a Job's CronJob).
type ConflictOwner struct {
	Group, Version, Kind, Namespace, Name string
	Up                                    *ConflictOwner
	// UpUnknown: this owner usually has one of its own (a ReplicaSet, a
	// Job), but the read of it failed or found another object at its name
	// (Codex's v0.15.1 review, finding 2): its owner is not established
	UpUnknown bool
}

// ApplyConflictError is the refused apply: its fields, and the object's
// controller when a Kubernetes controller owns a field.
type ApplyConflictError struct {
	Kind, Namespace, Name string
	Pod                   bool
	Fields                []ConflictField
	Owner                 *ConflictOwner
	Err                   error
}

func (e *ApplyConflictError) Unwrap() error { return e.Err }

// managerClass reads a field manager's name.
func managerClass(m string) (class, label string) {
	switch m {
	case "kustomize-controller", "helm-controller":
		return ManagerGitOps, "Flux's " + m
	case "argocd-controller", "argocd-application-controller":
		return ManagerGitOps, "Argo CD's " + m
	case "kube-controller-manager", "kube-scheduler", "kubelet", "kube-apiserver":
		return ManagerKubernetes, m
	}
	return ManagerOther, strconv.Quote(m)
}

// conflictFields reads the server-side apply conflict's causes:
// FieldManagerConflict, `conflict with "<manager>"` (Go-quoted, then an
// optional subresource, version and time), and the field path.
func conflictFields(err error) []ConflictField {
	var st apierrors.APIStatus
	if !errors.As(err, &st) || st.Status().Details == nil {
		return nil
	}
	var out []ConflictField
	for _, c := range st.Status().Details.Causes {
		if c.Type != metav1.CauseTypeFieldManagerConflict {
			continue
		}
		rest, ok := strings.CutPrefix(c.Message, "conflict with ")
		if !ok {
			continue
		}
		q, qerr := strconv.QuotedPrefix(rest)
		if qerr != nil {
			continue
		}
		m, uerr := strconv.Unquote(q)
		if uerr != nil || m == "" {
			continue
		}
		class, _ := managerClass(m)
		out = append(out, ConflictField{Manager: m, Class: class, Path: c.Field})
	}
	return out
}

// conflictOwner reads the object's controller from its owner references and,
// for a ReplicaSet or a Job, that controller's own. A failed read keeps what
// is known.
func (c *ClusterConn) conflictOwner(ctx context.Context, ns string, live *unstructured.Unstructured) *ConflictOwner {
	ref := controllerRef(live.GetOwnerReferences())
	if ref == nil {
		return nil
	}
	gv, _ := schema.ParseGroupVersion(ref.APIVersion)
	o := &ConflictOwner{Group: gv.Group, Version: gv.Version, Kind: ref.Kind, Namespace: ns, Name: ref.Name}
	var resource string
	switch {
	case gv.Group == "apps" && ref.Kind == "ReplicaSet":
		resource = "replicasets"
	case gv.Group == "batch" && ref.Kind == "Job":
		resource = "jobs"
	default:
		return o
	}
	up, err := c.dyn.Resource(schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: resource}).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
	// the second hop only from the object the reference names, by UID: a
	// replacement at that name is another object, whose owner says nothing
	if err != nil || up.GetUID() != ref.UID {
		o.UpUnknown = true
		return o
	}
	if r := controllerRef(up.GetOwnerReferences()); r != nil {
		ugv, _ := schema.ParseGroupVersion(r.APIVersion)
		o.Up = &ConflictOwner{Group: ugv.Group, Version: ugv.Version, Kind: r.Kind, Namespace: ns, Name: r.Name}
	}
	return o
}

func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// newApplyConflict builds the refused apply's error. Without readable causes
// it keeps the server's own text, which names the managers.
func (c *ClusterConn) newApplyConflict(ctx context.Context, kind, ns, name string, live *unstructured.Unstructured, err error) error {
	fields := conflictFields(err)
	if len(fields) == 0 {
		return fmt.Errorf("apply conflict - another field manager owns fields you changed; Klyx does not force: %w", err)
	}
	e := &ApplyConflictError{Kind: kind, Namespace: ns, Name: name, Pod: kind == "Pod" && live.GetAPIVersion() == "v1", Fields: fields, Err: err}
	for _, f := range fields {
		if f.Class == ManagerKubernetes {
			e.Owner = c.conflictOwner(ctx, ns, live)
			break
		}
	}
	return e
}

func (o *ConflictOwner) words() string {
	if o.Namespace == "" {
		return o.Kind + " " + o.Name
	}
	return o.Kind + " " + o.Namespace + "/" + o.Name
}

// Top is the owner an edit belongs in: the highest one read.
func (o *ConflictOwner) Top() *ConflictOwner {
	if o.Up != nil {
		return o.Up
	}
	return o
}

func fieldsWords(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%s and %d more", paths[0], len(paths)-1)
}

func (e *ApplyConflictError) Error() string {
	byManager := map[string][]string{}
	var managers []string
	for _, f := range e.Fields {
		if _, seen := byManager[f.Manager]; !seen {
			managers = append(managers, f.Manager)
		}
		byManager[f.Manager] = append(byManager[f.Manager], f.Path)
	}
	sort.Strings(managers)
	var parts []string
	for _, m := range managers {
		class, label := managerClass(m)
		paths := byManager[m]
		it, was := "it", "was"
		if len(paths) > 1 {
			it, was = "them", "were"
		}
		switch class {
		case ManagerGitOps:
			parts = append(parts, fmt.Sprintf("%s owns %s: change %s in Git - a forced apply would be reverted on the next reconcile", label, fieldsWords(paths), it))
		case ManagerKubernetes:
			s := fmt.Sprintf("%s sets %s, as the Kubernetes controller that maintains this %s", label, fieldsWords(paths), e.Kind)
			if e.Owner != nil {
				s += "; its controller is " + e.Owner.words()
				switch {
				case e.Owner.Up != nil:
					s += ", owned by " + e.Owner.Up.words() + ": change the " + e.Owner.Up.Kind + " instead"
				case e.Owner.UpUnknown:
					s += ", whose own owner Klyx could not establish: change that owner, or the " + e.Owner.Kind + " if it has none"
				default:
					s += ": change the " + e.Owner.Kind + " instead"
				}
			} else {
				s += ": change what that controller follows instead"
			}
			parts = append(parts, s)
		default:
			parts = append(parts, fmt.Sprintf("%s %s last set by %s: change %s with the tool that set %s", fieldsWords(paths), was, label, it, it))
		}
	}
	msg := "apply conflict: " + strings.Join(parts, "; ")
	if e.Pod {
		msg += ". A Pod's spec cannot change after it is created, apart from a few fields such as its containers' images, activeDeadlineSeconds, added tolerations and removed scheduling gates"
	}
	return msg + ". Klyx never forces another manager's fields."
}

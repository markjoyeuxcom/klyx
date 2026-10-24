package evalrun

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/moomora/klyx/internal/evalrun/record"
)

// Plant is one plant check's result (eval design §2), read on the runner's
// own connection, never through Klyx.
type Plant struct {
	OK     bool   `json:"ok"`
	Failed string `json:"failed,omitempty"` // the first invariant that does not hold
	// Witnesses are the values compared between the two checks.
	Witnesses map[string]string `json:"witnesses,omitempty"`
	AnchorUID string            `json:"anchorUID,omitempty"`
	At        time.Time         `json:"at"`
	// Predicates are the scenario's predicates with what only the lab
	// knows filled in: subjects' UIDs and a log's pods.
	Predicates []record.Predicate `json:"-"`
}

// Changed names the witnesses whose values differ between two checks.
func (p Plant) Changed(after Plant) []string {
	var out []string
	for k, v := range p.Witnesses {
		if after.Witnesses[k] != v {
			out = append(out, k)
		}
	}
	if p.AnchorUID != after.AnchorUID {
		out = append(out, "the anchor's UID")
	}
	sort.Strings(out)
	return out
}

// Checker reads the lab for plant checks.
type Checker struct {
	Dyn  dynamic.Interface
	Kube kubernetes.Interface
	Now  func() time.Time
}

// Check evaluates the scenario's plant: every invariant, the witnesses,
// the anchor's UID and the predicates' lab-only parts.
func (c Checker) Check(ctx context.Context, s *Scenario) Plant {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	p := Plant{At: now(), Witnesses: map[string]string{}}
	fail := func(format string, a ...any) Plant {
		p.OK, p.Failed = false, fmt.Sprintf(format, a...)
		return p
	}
	anchor, err := c.get(ctx, s.Anchor)
	if err != nil {
		return fail("the anchor %s: %v", s.Anchor, err)
	}
	p.AnchorUID = string(anchor.GetUID())
	for i, inv := range s.Invariants {
		if err := c.invariant(ctx, inv, now()); err != nil {
			return fail("invariant %d (%s): %v", i+1, inv.Object, err)
		}
	}
	for _, w := range s.Witnesses {
		o, err := c.get(ctx, w.Object)
		if err != nil {
			return fail("witness %s: %v", w.Object, err)
		}
		v, ok := lookup(o.Object, w.Path)
		if !ok {
			return fail("witness %s has no %s", w.Object, w.Path)
		}
		p.Witnesses[w.Object.String()+" "+w.Path] = fmt.Sprint(v)
	}
	for _, ps := range s.Predicates {
		pr := ps.Predicate
		if ps.SubjectRef != nil {
			o, err := c.get(ctx, *ps.SubjectRef)
			if err != nil {
				return fail("predicate %s's subject %s: %v", pr.ID, ps.SubjectRef, err)
			}
			pr.Subject = record.Subject{Kind: ps.SubjectRef.Kind, Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID())}
		}
		if ps.PodsOf != nil {
			pods, err := c.newestPods(ctx, *ps.PodsOf)
			if err != nil {
				return fail("predicate %s's pods: %v", pr.ID, err)
			}
			pr.Pods = nil
			for _, pod := range pods {
				pr.Pods = append(pr.Pods, string(pod.UID))
			}
			if len(pr.Pods) == 0 {
				return fail("predicate %s: %s has no pods", pr.ID, ps.PodsOf)
			}
		}
		p.Predicates = append(p.Predicates, pr)
	}
	p.OK = true
	return p
}

func (c Checker) get(ctx context.Context, r Ref) (*unstructured.Unstructured, error) {
	return c.Dyn.Resource(r.GVR()).Namespace(r.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
}

func (c Checker) invariant(ctx context.Context, inv Invariant, now time.Time) error {
	if inv.Absent {
		_, err := c.get(ctx, inv.Object)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		default:
			return fmt.Errorf("exists")
		}
	}
	if inv.Pods != nil {
		return c.pods(ctx, inv.Object, *inv.Pods)
	}
	o, err := c.get(ctx, inv.Object)
	if err != nil {
		return err
	}
	switch {
	case inv.Path != "":
		v, ok := lookup(o.Object, inv.Path)
		if !ok {
			return fmt.Errorf("no %s", inv.Path)
		}
		if !regexp.MustCompile(inv.Matches).MatchString(fmt.Sprint(v)) {
			return fmt.Errorf("%s is %q, not /%s/", inv.Path, fmt.Sprint(v), inv.Matches)
		}
	case inv.Condition != nil:
		cs := inv.Condition
		v, ok := lookup(o.Object, "status.conditions[type="+cs.Type+"]")
		m, _ := v.(map[string]any)
		if !ok || m == nil {
			return fmt.Errorf("no %s condition", cs.Type)
		}
		if m["status"] != cs.Status || (cs.Reason != "" && m["reason"] != cs.Reason) {
			return fmt.Errorf("%s is %v/%v, not %s/%s", cs.Type, m["status"], m["reason"], cs.Status, cs.Reason)
		}
	case inv.Event != nil:
		return c.event(ctx, o, *inv.Event, now)
	}
	return nil
}

// newestPods are the pods of a Deployment's newest ReplicaSet, by its
// revision annotation, joined by controller UID.
func (c Checker) newestPods(ctx context.Context, d Ref) ([]corev1.Pod, error) {
	dep, err := c.Kube.AppsV1().Deployments(d.Namespace).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	rss, err := c.Kube.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var newest string
	best := int64(-1)
	for _, rs := range rss.Items {
		ref := metav1.GetControllerOf(&rs)
		if ref == nil || ref.UID != dep.UID {
			continue
		}
		if n, err := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64); err == nil && n > best {
			best, newest = n, string(rs.UID)
		}
	}
	if newest == "" {
		return nil, fmt.Errorf("no ReplicaSet of %s", d)
	}
	pods, err := c.Kube.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []corev1.Pod
	for _, pod := range pods.Items {
		if ref := metav1.GetControllerOf(&pod); ref != nil && string(ref.UID) == newest {
			out = append(out, pod)
		}
	}
	return out, nil
}

func (c Checker) pods(ctx context.Context, d Ref, spec PodsSpec) error {
	pods, err := c.newestPods(ctx, d)
	if err != nil {
		return err
	}
	min := spec.Min
	if min == 0 {
		min = 1
	}
	if len(pods) < min {
		return fmt.Errorf("%d pods of the newest ReplicaSet, want at least %d", len(pods), min)
	}
	for _, pod := range pods {
		switch {
		case spec.Waiting != "":
			ok := false
			for _, cs := range pod.Status.ContainerStatuses {
				if w := cs.State.Waiting; w != nil && w.Reason == spec.Waiting && cs.RestartCount >= spec.MinRestarts {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("pod %s is not waiting for %s with %d restarts", pod.Name, spec.Waiting, spec.MinRestarts)
			}
		case spec.Crashing:
			ok := false
			for _, cs := range pod.Status.ContainerStatuses {
				if t := cs.LastTerminationState.Terminated; t != nil && t.ExitCode != 0 && cs.RestartCount >= spec.MinRestarts {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("pod %s has not crashed %d times", pod.Name, spec.MinRestarts)
			}
		case spec.Ready:
			ready := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					ready = true
				}
			}
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.RestartCount > 0 {
					ready = false
				}
			}
			if !ready {
				return fmt.Errorf("pod %s is not Ready without restarts", pod.Name)
			}
		}
	}
	return nil
}

func (c Checker) event(ctx context.Context, o *unstructured.Unstructured, spec EventSpec, now time.Time) error {
	within := time.Hour
	if spec.Within != "" {
		d, err := time.ParseDuration(spec.Within)
		if err != nil {
			return err
		}
		within = d
	}
	var msg *regexp.Regexp
	if spec.Message != "" {
		msg = regexp.MustCompile(spec.Message)
	}
	evs, err := c.Kube.CoreV1().Events(o.GetNamespace()).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(o.GetUID())).String()})
	if err != nil {
		return err
	}
	for _, e := range evs.Items {
		last := e.LastTimestamp.Time
		if e.Series != nil && e.Series.LastObservedTime.After(last) {
			last = e.Series.LastObservedTime.Time
		}
		if last.IsZero() {
			last = e.EventTime.Time
		}
		if e.Reason == spec.Reason && (msg == nil || msg.MatchString(e.Message)) && now.Sub(last) <= within {
			return nil
		}
	}
	return fmt.Errorf("no %s event within %s", spec.Reason, within)
}

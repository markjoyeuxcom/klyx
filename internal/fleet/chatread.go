package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/observe"
)

// The cluster chat's reads (spec 2026-10-14). Every one is read-only and runs
// on this conn only: chat holds one conn per conversation and never names a
// cluster in a tool call.

// ChatKind is one served top-level resource a chat tool may get and list.
type ChatKind struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
}

// ChatResolveKind resolves a kind the model named - Kind, plural, singular or
// short name, case-insensitive, optionally within one group - to the served
// top-level resources that support get and list, each group's preferred
// version first. Several results mean the name is ambiguous; none means it is
// not served - unless incomplete names group versions whose discovery failed,
// in which case neither absence nor a unique match is proven (Codex review
// of aea77e7). Subresources are never returned (spec §3.2).
func (c *ClusterConn) ChatResolveKind(ctx context.Context, kind, group string) (kinds []ChatKind, incomplete []string, err error) {
	if c.typed == nil {
		return nil, nil, errors.New("discovery unavailable: no cluster connection")
	}
	want := strings.ToLower(strings.TrimSpace(kind))
	if want == "" {
		return nil, nil, errors.New("no kind given")
	}
	d := c.probeDiscovery()
	groups, err := d.ServerGroupsCtx(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("discovery failed: %w", err)
	}
	var out []ChatKind
	seen := map[string]bool{}
	for _, g := range groups.Groups {
		if group != "" && !strings.EqualFold(g.Name, group) {
			continue
		}
		versions := []string{g.PreferredVersion.GroupVersion}
		for _, v := range g.Versions {
			if v.GroupVersion != g.PreferredVersion.GroupVersion {
				versions = append(versions, v.GroupVersion)
			}
		}
		for _, gvStr := range versions {
			if gvStr == "" {
				continue
			}
			gv, err := schema.ParseGroupVersion(gvStr)
			if err != nil {
				continue
			}
			rl, err := d.ServerResourcesForGroupVersionCtx(ctx, gvStr)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				incomplete = append(incomplete, gvStr)
				continue
			}
			for _, r := range rl.APIResources {
				if strings.Contains(r.Name, "/") || !hasVerbs(r.Verbs, "get", "list") {
					continue
				}
				if !chatKindMatches(r, want) {
					continue
				}
				key := gv.Group + "/" + r.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, ChatKind{GVR: gv.WithResource(r.Name), Kind: r.Kind, Namespaced: r.Namespaced})
			}
		}
	}
	return out, incomplete, nil
}

func chatKindMatches(r metav1.APIResource, want string) bool {
	if strings.ToLower(r.Kind) == want || r.Name == want || strings.ToLower(r.SingularName) == want {
		return true
	}
	for _, s := range r.ShortNames {
		if strings.ToLower(s) == want {
			return true
		}
	}
	return false
}

// ChatGet reads one object.
func (c *ClusterConn) ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	if c.dyn == nil {
		return nil, errors.New("dynamic client unavailable")
	}
	if ns == "" {
		return c.dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	}
	return c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

// ChatList reads ONE page of a kind: limit objects at most. The continue
// token says whether more exist (spec §3.3: a lower-bound total).
func (c *ClusterConn) ChatList(ctx context.Context, gvr schema.GroupVersionResource, ns, selector string, limit int64) (*unstructured.UnstructuredList, error) {
	if c.dyn == nil {
		return nil, errors.New("dynamic client unavailable")
	}
	opts := metav1.ListOptions{LabelSelector: selector, Limit: limit}
	if ns == "" {
		return c.dyn.Resource(gvr).List(ctx, opts)
	}
	return c.dyn.Resource(gvr).Namespace(ns).List(ctx, opts)
}

// ChatSummary is the cluster's latest Observer summary.
func (c *ClusterConn) ChatSummary() (observe.Summary, bool) {
	h := c.Health()
	if h == nil {
		return observe.Summary{}, false
	}
	return h.Latest()
}

// LogSnapshot is one finite, bounded log read (spec §4). Text is raw: the
// caller scrubs it before anything leaves the machine.
type LogSnapshot struct {
	PodUID     string
	Container  string
	Previous   bool
	ReadAt     time.Time
	Text       string
	Truncated  bool     // the byte cap cut the read
	Containers []string // set when the container was ambiguous; Text is then empty
}

const (
	// LogSnapshotMaxBytes caps a log read on the server (LimitBytes) and again
	// on the client.
	LogSnapshotMaxBytes int64 = 128 * 1024
	logSnapshotTimeout        = 10 * time.Second
)

// ErrPodReplaced: the pod's UID changed while its log was read, so the log
// may belong to another pod and is discarded.
var ErrPodReplaced = errors.New("the pod was replaced while its log was read; nothing was kept")

// PodLogSnapshot reads the last lines of one container's log without
// following it: TailLines and a server-side LimitBytes, a client-side byte
// cap, its own deadline, and a close on every path. The pod is read before
// and after; a changed UID discards the log (the log subresource is not
// atomic with the UID). A pod with several containers and none named reads
// nothing and lists them.
func (c *ClusterConn) PodLogSnapshot(ctx context.Context, ns, pod, container string, previous bool, tail int64) (LogSnapshot, error) {
	if c.typed == nil {
		return LogSnapshot{}, errors.New("no cluster connection")
	}
	ctx, cancel := context.WithTimeout(ctx, logSnapshotTimeout)
	defer cancel()
	before, err := c.typed.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return LogSnapshot{}, err
	}
	names := make([]string, 0, len(before.Spec.Containers))
	for _, ct := range before.Spec.Containers {
		names = append(names, ct.Name)
	}
	if container == "" {
		if len(names) != 1 {
			return LogSnapshot{PodUID: string(before.UID), Containers: names}, nil
		}
		container = names[0]
	} else if !containerExists(before, container) {
		return LogSnapshot{PodUID: string(before.UID), Containers: names}, fmt.Errorf("pod %s/%s has no container %q", ns, pod, container)
	}
	if previous && !restarted(before, container) {
		return LogSnapshot{}, fmt.Errorf("container %q has not restarted, so it has no previous log", container)
	}
	maxBytes := LogSnapshotMaxBytes
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &tail, LimitBytes: &maxBytes}
	rc, err := c.typed.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return LogSnapshot{}, err
	}
	b, rerr := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	_ = rc.Close()
	if rerr != nil {
		return LogSnapshot{}, rerr
	}
	// The server stops at LimitBytes without saying so: a read that reached
	// the cap is reported as possibly truncated.
	truncated := int64(len(b)) >= maxBytes
	if int64(len(b)) > maxBytes {
		b = b[:maxBytes]
	}
	after, err := c.typed.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return LogSnapshot{}, err
	}
	if after.UID != before.UID {
		return LogSnapshot{}, ErrPodReplaced
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	return LogSnapshot{PodUID: string(before.UID), Container: container, Previous: previous, ReadAt: clk.Now(), Text: string(b), Truncated: truncated}, nil
}

func containerExists(p *corev1.Pod, name string) bool {
	for _, ct := range p.Spec.Containers {
		if ct.Name == name {
			return true
		}
	}
	for _, ct := range p.Spec.InitContainers {
		if ct.Name == name {
			return true
		}
	}
	return false
}

func restarted(p *corev1.Pod, name string) bool {
	for _, s := range append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...) {
		if s.Name == name {
			return s.RestartCount > 0 || s.LastTerminationState.Terminated != nil
		}
	}
	return false
}

// ErrChatMetricsUnavailable: the cluster has no usable metrics source. It
// says nothing about whether a series exists.
var ErrChatMetricsUnavailable = errors.New("metrics unavailable on this cluster")

// ChatMetricsRange runs one range query Klyx built from its own template (AI
// A1 §3.4; the model never writes PromQL) through the cluster's metrics
// transport: the same lazy probe and cache as every metrics read, and a
// result read across a credential change is dropped (C5 §7).
func (c *ClusterConn) ChatMetricsRange(ctx context.Context, promql string, start, end time.Time, step time.Duration) ([]metrics.LabeledSeries, error) {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	mr := c.readMetrics(ctx, clk.Now())
	if !mr.cap.Available {
		if mr.cap.Reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrChatMetricsUnavailable, mr.cap.Reason)
		}
		return nil, ErrChatMetricsUnavailable
	}
	res, err := metrics.NewClient(mr.transport).RangeMatrix(ctx, promql, start, end, step)
	if err == nil && c.credentialMoved(mr.rev) {
		return nil, fmt.Errorf("%w: %s", ErrChatMetricsUnavailable, reasonCredentialChanged)
	}
	return res, err
}

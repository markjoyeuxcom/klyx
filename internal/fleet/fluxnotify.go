package fleet

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/notify"
)

// FluxNotifications reads Providers and Alerts on demand for the
// Notifications view (coverage S1c §3): each kind at v1beta3, capped at
// notify.ListCap with what is past the cap said, and the reasons of the
// Alerts' recent events. Nothing is watched or kept.
func (c *ClusterConn) FluxNotifications(ctx context.Context) notify.Read {
	out := notify.Read{Providers: c.notificationList(ctx, "providers"), Alerts: c.notificationList(ctx, "alerts"), AlertReasons: map[string][]string{}}
	if c.typed == nil {
		out.EventsError = "the events could not be read"
		return out
	}
	evs, err := c.typed.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.kind=Alert", Limit: notify.ListCap})
	if err != nil {
		out.EventsError = err.Error()
		return out
	}
	out.EventsTruncated = evs.Continue != ""
	for _, e := range evs.Items {
		// a server (or fake) that ignores the field selector; an event
		// naming no UID is attributed to no Alert (Codex's S1c
		// implementation review, finding 3)
		if e.InvolvedObject.Kind != "Alert" || e.InvolvedObject.UID == "" {
			continue
		}
		key := string(e.InvolvedObject.UID)
		out.AlertReasons[key] = append(out.AlertReasons[key], e.Reason)
	}
	return out
}

func (c *ClusterConn) notificationList(ctx context.Context, resource string) notify.List {
	const group = "notification.toolkit.fluxcd.io"
	if c.dyn == nil {
		return notify.List{Error: "dynamic client unavailable"}
	}
	version, served, err := servedVersion(c.discovery(), group, resource, notify.StaticVersion)
	switch {
	case err != nil:
		return notify.List{Error: err.Error()}
	case !served:
		return notify.List{Absent: true}
	case version != notify.StaticVersion:
		return notify.List{Error: fmt.Sprintf("unsupported version %s for %s: Klyx reads %s", version, resource, notify.StaticVersion)}
	}
	l, err := c.dyn.Resource(schema.GroupVersionResource{Group: group, Version: version, Resource: resource}).List(ctx, metav1.ListOptions{Limit: notify.ListCap})
	if err != nil {
		return notify.List{Error: err.Error()}
	}
	out := notify.List{Items: make([]*unstructured.Unstructured, 0, len(l.Items))}
	for i := range l.Items {
		out.Items = append(out.Items, &l.Items[i])
	}
	if l.GetContinue() != "" {
		out.More = -1
		if n := l.GetRemainingItemCount(); n != nil && *n > 0 {
			out.More = *n
		}
	}
	return out
}

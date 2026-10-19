package capability

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Velero's API group and its server's selector (coverage S2a §1, §3): a
// Deployment labelled component=velero with a container named velero, the
// Velero CLI's own selector (pkg/cmd/cli/plugin/helpers.go 31-55), which
// holds for both the CLI and the Helm installs.
const (
	veleroGroup         = "velero.io"
	veleroSelector      = "component=velero"
	veleroContainer     = "velero"
	veleroDisableFlag   = "--disable-controllers"
	veleroScheduleCtrl  = "schedule"
	veleroTZ            = "TZ"
	veleroNotReady      = "Velero server %s/%s is not ready"
	veleroNoServer      = "Velero CRDs installed but no Velero server found (no Deployment labelled " + veleroSelector + " with a container named " + veleroContainer + ")"
	veleroServersUnread = "Velero CRDs installed but the Velero servers could not be read"
)

// VeleroServer is one Velero server Deployment, as read. Its configuration
// facts are the running server's only once it has rolled out (RolledOut).
type VeleroServer struct {
	Namespace, Name string
	Ready           bool
	RolledOut       bool
	// Image is the velero container's image (the version is in its tag).
	Image string
	// TZ is the container's literal TZ; TZFromRef marks one set by
	// valueFrom, which Klyx never resolves.
	TZ        string
	TZSet     bool
	TZFromRef bool
	// ScheduleDisabled: "schedule" is among --disable-controllers.
	ScheduleDisabled bool
}

// VeleroCapability models Velero: presence of the velero.io CRDs, and the
// servers found by Velero's own selector. Klyx reads only the image, the
// literal TZ and --disable-controllers from them.
type VeleroCapability struct {
	Base
	Present  bool
	Healthy  bool
	Verified bool // a server was read, so Healthy is a claim
	// ServersRead: the server list was read (an empty list is then proof
	// that no server watches any namespace).
	ServersRead bool
	Servers     []VeleroServer
}

// detectVelero classifies Velero (S2a §3 controllers): not ready when a
// found server is not ready; unknown when the list is unreadable, or when
// no server is found while the CRDs are served (a retained CRD is not an
// active install).
func (d *Detector) detectVelero(ctx context.Context, served map[string]bool) VeleroCapability {
	out := VeleroCapability{Present: served[veleroGroup]}
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no Velero CRDs installed"}
		return out
	}
	deps, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: veleroSelector})
	if err != nil {
		out.Base = Base{Tier: Classify(true, false), Reason: veleroServersUnread + ": " + err.Error()}
		return out
	}
	out.ServersRead = true
	var notReady []string
	for i := range deps.Items {
		s, ok := veleroServerOf(&deps.Items[i])
		if !ok {
			continue
		}
		out.Servers = append(out.Servers, s)
		if !s.Ready {
			notReady = append(notReady, fmt.Sprintf(veleroNotReady, s.Namespace, s.Name))
		}
	}
	sort.Slice(out.Servers, func(i, j int) bool {
		if out.Servers[i].Namespace != out.Servers[j].Namespace {
			return out.Servers[i].Namespace < out.Servers[j].Namespace
		}
		return out.Servers[i].Name < out.Servers[j].Name
	})
	switch {
	case len(out.Servers) == 0:
		out.Base.Reason = veleroNoServer
	case len(notReady) > 0:
		out.Verified = true
		out.Base.Reason = strings.Join(notReady, "; ")
	default:
		out.Verified, out.Healthy = true, true
	}
	out.Base.Tier = Classify(true, out.Healthy)
	return out
}

// veleroServerOf reads one Deployment as a Velero server; ok is false when
// it has no container named velero.
func veleroServerOf(d *appsv1.Deployment) (VeleroServer, bool) {
	var c *corev1.Container
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == veleroContainer {
			c = &d.Spec.Template.Spec.Containers[i]
		}
	}
	if c == nil {
		return VeleroServer{}, false
	}
	s := VeleroServer{Namespace: d.Namespace, Name: d.Name, Ready: DeploymentReady(d), RolledOut: rolledOut(d), Image: c.Image}
	for _, e := range c.Env {
		if e.Name != veleroTZ {
			continue
		}
		s.TZSet = true
		if e.ValueFrom != nil {
			s.TZFromRef = true
		} else {
			s.TZ = e.Value
		}
	}
	s.ScheduleDisabled = scheduleDisabled(append(append([]string(nil), c.Command...), c.Args...))
	return s, true
}

// rolledOut reports a Deployment whose configuration is the running one:
// observed, and its updated, available and total replicas all desired.
func rolledOut(d *appsv1.Deployment) bool {
	want := desiredReplicas(d.Spec.Replicas)
	return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want &&
		d.Status.AvailableReplicas == want && d.Status.Replicas == want
}

// scheduleDisabled reads --disable-controllers (=value or a following
// argument), a comma-separated list, for the schedule controller.
func scheduleDisabled(argv []string) bool {
	for i, a := range argv {
		var v string
		switch {
		case strings.HasPrefix(a, veleroDisableFlag+"="):
			v = strings.TrimPrefix(a, veleroDisableFlag+"=")
		case a == veleroDisableFlag && i+1 < len(argv):
			v = argv[i+1]
		default:
			continue
		}
		for _, c := range strings.Split(v, ",") {
			if strings.TrimSpace(c) == veleroScheduleCtrl {
				return true
			}
		}
	}
	return false
}

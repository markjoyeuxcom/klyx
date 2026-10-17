package capability

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Flux image automation's controllers (coverage S1b §3): found by their
// app.kubernetes.io/component label in the Flux namespace detection
// resolves - flux-system, or a single operator runtime namespace (S1a).
// Their readiness feeds the Flux task as kustomize-controller's does.
const (
	fluxImageGroup     = "image.toolkit.fluxcd.io"
	imageReflector     = "image-reflector-controller"
	imageAutomation    = "image-automation-controller"
	imageControllerMax = 5
)

// ImageControllers is the image controllers' state; Served is false when
// the image API group is not.
type ImageControllers struct {
	Served     bool
	Namespace  string
	Reflector  ImageController
	Automation ImageController
}

// ImageController is one controller's read (Codex's S1b implementation
// review, findings 2 and 4). It is Needed only when its own resources are
// served: a reflector-only installation needs no automation controller.
// Every Deployment found is judged on its own - a ready shard never
// certifies another - and absence needs a complete list. Unverified (the
// discovery or the list failed) claims nothing.
type ImageController struct {
	Name        string
	Namespace   string
	Needed      bool
	Deployments []ImageDeployment
	Complete    bool
	Unverified  bool
	Why         string
}

// ImageDeployment is one Deployment carrying the controller's label.
type ImageDeployment struct {
	Namespace, Name string
	Ready           bool
}

// Problems splits the image controllers' issues into proven failures (a
// Deployment not ready, or none in a complete list while its resources are
// served) and gaps (unreadable, or a list cut short).
func (i ImageControllers) Problems() (failures, gaps []string) {
	if !i.Served {
		return nil, nil
	}
	for _, c := range []ImageController{i.Reflector, i.Automation} {
		f, g := c.Problems("Flux image automation CRDs")
		failures, gaps = append(failures, f...), append(gaps, g...)
	}
	return failures, gaps
}

// Problems is one controller's: a Deployment not ready, or none in a
// complete list while its resources are served, is a failure; an
// unreadable read or a list cut short is a gap. what names the CRDs whose
// presence makes it needed.
func (c ImageController) Problems(what string) (failures, gaps []string) {
	switch {
	case !c.Needed:
		return nil, nil
	case c.Unverified:
		return nil, []string{"Flux " + c.Name + " health is unverified (" + c.Why + ")"}
	}
	for _, d := range c.Deployments {
		if !d.Ready {
			failures = append(failures, "Flux "+c.Name+" "+d.Namespace+"/"+d.Name+" is not ready")
		}
	}
	switch {
	case !c.Complete:
		gaps = append(gaps, "Flux "+c.Name+": the list of Deployments in "+c.Namespace+" was cut short")
	case len(c.Deployments) == 0:
		failures = append(failures, fmt.Sprintf("%s installed but no %s in %s", what, c.Name, c.Namespace))
	}
	return failures, gaps
}

// Issue is every problem, proven failures first; "" when none.
func (i ImageControllers) Issue() string {
	f, g := i.Problems()
	return strings.Join(append(f, g...), "; ")
}

// groupResources reads which resources group's served versions carry: a
// version that reads proves its resources; an unreadable one is a gap.
func (d *Detector) groupResources(served map[string]bool, group string, versions ...string) (map[string]bool, string) {
	res, gap := map[string]bool{}, ""
	for _, v := range versions {
		gv := group + "/" + v
		if !served[gv] {
			continue
		}
		rl, err := d.cs.Discovery().ServerResourcesForGroupVersion(gv)
		if err != nil || rl == nil {
			gap = "the " + group + " resources at " + v + " could not be read"
			continue
		}
		for _, r := range rl.APIResources {
			res[r.Name] = true
		}
	}
	return res, gap
}

// readController reads one controller by its component label in ns. It is
// read when its resources are proven served; when they are not, a
// discovery gap leaves it unverified, else it is not needed.
func (d *Detector) readController(ctx context.Context, name, ns string, needed bool, gap string) ImageController {
	c := ImageController{Name: name, Namespace: ns}
	switch {
	case needed:
	case gap != "":
		c.Needed, c.Unverified, c.Why = true, true, gap
		return c
	default:
		return c
	}
	c.Needed = true
	deps, err := d.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + name, Limit: imageControllerMax})
	if err != nil {
		c.Unverified, c.Why = true, "could not list Deployments in "+ns+": "+err.Error()
		return c
	}
	c.Complete = deps.Continue == ""
	for i := range deps.Items {
		dep := &deps.Items[i]
		c.Deployments = append(c.Deployments, ImageDeployment{Namespace: dep.Namespace, Name: dep.Name, Ready: DeploymentReady(dep)})
	}
	return c
}

// imageResources reads which image resources the served versions carry.
// A version that reads proves its resources present; an unreadable one is a
// gap that proves nothing either way, and never discards what another
// version proved (Codex's S1b implementation review 2, finding 2).
func (d *Detector) imageResources(served map[string]bool) (map[string]bool, string) {
	return d.groupResources(served, fluxImageGroup, "v1", "v1beta2")
}

func (d *Detector) detectImageControllers(ctx context.Context, served map[string]bool, ns string) ImageControllers {
	out := ImageControllers{Served: served[fluxImageGroup], Namespace: ns,
		Reflector: ImageController{Name: imageReflector, Namespace: ns}, Automation: ImageController{Name: imageAutomation, Namespace: ns}}
	if !out.Served {
		return out
	}
	res, gap := d.imageResources(served)
	out.Reflector = d.readController(ctx, imageReflector, ns, res["imagerepositories"] || res["imagepolicies"], gap)
	out.Automation = d.readController(ctx, imageAutomation, ns, res["imageupdateautomations"], gap)
	return out
}

// Coverage S1c §3: source-watcher (ArtifactGenerators) and
// notification-controller (Receivers, Providers, Alerts), found and judged
// as the image controllers are.
const (
	sourceWatcher          = "source-watcher"
	notificationController = "notification-controller"
	sourceExtensionsGroup  = "source.extensions.fluxcd.io"
	notificationGroup      = "notification.toolkit.fluxcd.io"
)

// FluxComponents are the component controllers beyond kustomize-controller
// that S1c reads.
type FluxComponents struct {
	SourceWatcher, Notification ImageController
}

// Problems is both controllers'.
func (f FluxComponents) Problems() (failures, gaps []string) {
	for _, c := range []struct {
		c    ImageController
		what string
	}{{f.SourceWatcher, "ArtifactGenerator CRDs"}, {f.Notification, "Flux notification CRDs"}} {
		fl, g := c.c.Problems(c.what)
		failures, gaps = append(failures, fl...), append(gaps, g...)
	}
	return failures, gaps
}

func (d *Detector) detectFluxComponents(ctx context.Context, served map[string]bool, ns string) FluxComponents {
	out := FluxComponents{SourceWatcher: ImageController{Name: sourceWatcher, Namespace: ns}, Notification: ImageController{Name: notificationController, Namespace: ns}}
	if served[sourceExtensionsGroup] {
		res, gap := d.groupResources(served, sourceExtensionsGroup, "v1beta1")
		out.SourceWatcher = d.readController(ctx, sourceWatcher, ns, res["artifactgenerators"], gap)
	}
	if served[notificationGroup] {
		res, gap := d.groupResources(served, notificationGroup, "v1", "v1beta3")
		out.Notification = d.readController(ctx, notificationController, ns, res["receivers"] || res["providers"] || res["alerts"], gap)
	}
	return out
}

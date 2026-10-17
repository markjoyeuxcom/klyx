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
		switch {
		case !c.Needed:
			continue
		case c.Unverified:
			gaps = append(gaps, "Flux "+c.Name+" health is unverified ("+c.Why+")")
			continue
		}
		for _, d := range c.Deployments {
			if !d.Ready {
				failures = append(failures, "Flux "+c.Name+" "+d.Namespace+"/"+d.Name+" is not ready")
			}
		}
		switch {
		case !c.Complete:
			gaps = append(gaps, "Flux "+c.Name+": the list of Deployments in "+i.Namespace+" was cut short")
		case len(c.Deployments) == 0:
			failures = append(failures, fmt.Sprintf("Flux image automation CRDs installed but no %s in %s", c.Name, i.Namespace))
		}
	}
	return failures, gaps
}

// Issue is every problem, proven failures first; "" when none.
func (i ImageControllers) Issue() string {
	f, g := i.Problems()
	return strings.Join(append(f, g...), "; ")
}

// imageResources reads which image resources the served versions carry.
// A version that reads proves its resources present; an unreadable one is a
// gap that proves nothing either way, and never discards what another
// version proved (Codex's S1b implementation review 2, finding 2).
func (d *Detector) imageResources(served map[string]bool) (map[string]bool, string) {
	res, gap := map[string]bool{}, ""
	for _, v := range []string{"v1", "v1beta2"} {
		gv := fluxImageGroup + "/" + v
		if !served[gv] {
			continue
		}
		rl, err := d.cs.Discovery().ServerResourcesForGroupVersion(gv)
		if err != nil || rl == nil {
			gap = "the image API's resources at " + v + " could not be read"
			continue
		}
		for _, r := range rl.APIResources {
			res[r.Name] = true
		}
	}
	return res, gap
}

func (d *Detector) detectImageControllers(ctx context.Context, served map[string]bool, ns string) ImageControllers {
	out := ImageControllers{Served: served[fluxImageGroup], Namespace: ns,
		Reflector: ImageController{Name: imageReflector}, Automation: ImageController{Name: imageAutomation}}
	if !out.Served {
		return out
	}
	res, gap := d.imageResources(served)
	read := func(c *ImageController, needed bool) {
		switch {
		case needed:
			// its resources are proven served: read it, whatever else is
			// unknown
		case gap != "":
			// not proven needed, and the discovery that could tell failed
			c.Needed, c.Unverified, c.Why = true, true, gap
			return
		default:
			return
		}
		c.Needed = true
		deps, err := d.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + c.Name, Limit: imageControllerMax})
		if err != nil {
			c.Unverified, c.Why = true, "could not list Deployments in "+ns+": "+err.Error()
			return
		}
		c.Complete = deps.Continue == ""
		for i := range deps.Items {
			dep := &deps.Items[i]
			c.Deployments = append(c.Deployments, ImageDeployment{Namespace: dep.Namespace, Name: dep.Name, Ready: DeploymentReady(dep)})
		}
	}
	read(&out.Reflector, res["imagerepositories"] || res["imagepolicies"])
	read(&out.Automation, res["imageupdateautomations"])
	return out
}

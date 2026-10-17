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

// ImageController is one controller's read. Found with Ready a claim;
// not Found from a complete list is a proven absence; Unverified (the list
// failed) claims nothing.
type ImageController struct {
	Name         string
	Found, Ready bool
	Unverified   bool
	Why          string
}

// Problems splits the image controllers' issues into proven failures (not
// ready, or absent while their CRDs are served) and gaps (unreadable).
func (i ImageControllers) Problems() (failures, gaps []string) {
	if !i.Served {
		return nil, nil
	}
	for _, c := range []ImageController{i.Reflector, i.Automation} {
		switch {
		case c.Unverified:
			gaps = append(gaps, "Flux "+c.Name+" health is unverified ("+c.Why+")")
		case !c.Found:
			failures = append(failures, fmt.Sprintf("Flux image automation CRDs installed but no %s in %s", c.Name, i.Namespace))
		case !c.Ready:
			failures = append(failures, "Flux "+c.Name+" is not ready")
		}
	}
	return failures, gaps
}

// Issue is every problem, proven failures first; "" when none.
func (i ImageControllers) Issue() string {
	f, g := i.Problems()
	return strings.Join(append(f, g...), "; ")
}

func (d *Detector) detectImageControllers(ctx context.Context, served map[string]bool, ns string) ImageControllers {
	out := ImageControllers{Served: served[fluxImageGroup], Namespace: ns}
	if !out.Served {
		return out
	}
	read := func(name string) ImageController {
		c := ImageController{Name: name}
		deps, err := d.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + name, Limit: imageControllerMax})
		if err != nil {
			c.Unverified, c.Why = true, "could not list Deployments in "+ns+": "+err.Error()
			return c
		}
		if len(deps.Items) == 0 && deps.Continue != "" {
			c.Unverified, c.Why = true, "the list of Deployments in "+ns+" was cut short"
			return c
		}
		for i := range deps.Items {
			c.Found = true
			c.Ready = c.Ready || DeploymentReady(&deps.Items[i])
		}
		return c
	}
	out.Reflector, out.Automation = read(imageReflector), read(imageAutomation)
	return out
}

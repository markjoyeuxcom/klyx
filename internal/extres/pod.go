package extres

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// PodEntry is one container's extended request (request equals limit, so
// one figure), implicit DeviceClass names included.
type PodEntry struct {
	Name        string
	Quantity    Qty
	ServedByDRA bool
}

// ContainerEntries is one container's extended requests, within the bounds.
type ContainerEntries struct {
	Container string
	Init      bool
	Entries   []PodEntry
	// Omitted counts entries past MaxContainerEntries.
	Omitted int
}

// PodRequests is a pod's extended requests for its inspector (S11a §6).
type PodRequests struct {
	Containers []ContainerEntries
	// Omitted counts entries past MaxPodEntries, across containers.
	Omitted int
	// Claim names the special ResourceClaim serving the DRA-mapped pairs.
	Claim string
}

// OfPod lists a pod's extended requests: init containers first, then the
// containers, each by name; at most MaxContainerEntries per container and
// MaxPodEntries in all.
func OfPod(p *corev1.Pod) PodRequests {
	var out PodRequests
	mapped := map[[2]string]bool{}
	if st := p.Status.ExtendedResourceClaimStatus; st != nil {
		out.Claim = st.ResourceClaimName
		for _, m := range st.RequestMappings {
			mapped[[2]string{m.ContainerName, m.ResourceName}] = true
		}
	}
	kept := 0
	list := func(cs []corev1.Container, init bool) {
		for _, c := range cs {
			names := make([]string, 0, len(c.Resources.Requests))
			for k := range c.Resources.Requests {
				if IsDRAExtended(k) {
					names = append(names, string(k))
				}
			}
			if len(names) == 0 {
				continue
			}
			sort.Strings(names)
			ce := ContainerEntries{Container: c.Name, Init: init}
			for _, name := range names {
				if len(ce.Entries) >= MaxContainerEntries {
					ce.Omitted++
					continue
				}
				if kept >= MaxPodEntries {
					out.Omitted++
					continue
				}
				dra := mapped[[2]string{c.Name, name}] || strings.HasPrefix(name, DeviceClassPrefix)
				ce.Entries = append(ce.Entries, PodEntry{Name: name, Quantity: qtyOf(c.Resources.Requests[corev1.ResourceName(name)]), ServedByDRA: dra})
				kept++
			}
			out.Containers = append(out.Containers, ce)
		}
	}
	list(p.Spec.InitContainers, true)
	list(p.Spec.Containers, false)
	return out
}

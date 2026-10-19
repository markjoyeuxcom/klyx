package fleetindex

import (
	"fmt"
	"regexp"
	"strings"
)

// KindRef is one indexed kind: its group and resource, a preferred version
// (used only when served) and a display name.
type KindRef struct {
	Group, Version, Resource string
	Kind                     string
}

// Key is the index's name for the kind: group/resource, whatever version.
func (k KindRef) Key() string { return k.Group + "/" + k.Resource }

// DefaultKinds is parent §8.4's list.
var DefaultKinds = []KindRef{
	{"", "v1", "namespaces", "Namespace"},
	{"", "v1", "nodes", "Node"},
	{"apps", "v1", "deployments", "Deployment"},
	{"apps", "v1", "statefulsets", "StatefulSet"},
	{"apps", "v1", "daemonsets", "DaemonSet"},
	{"apps", "v1", "replicasets", "ReplicaSet"},
	{"batch", "v1", "jobs", "Job"},
	{"batch", "v1", "cronjobs", "CronJob"},
	{"", "v1", "pods", "Pod"},
	{"", "v1", "services", "Service"},
	{"networking.k8s.io", "v1", "ingresses", "Ingress"},
	{"gateway.networking.k8s.io", "v1", "gateways", "Gateway"},
	{"gateway.networking.k8s.io", "v1", "httproutes", "HTTPRoute"},
	{"gateway.networking.k8s.io", "v1", "grpcroutes", "GRPCRoute"},
	// coverage S7: the ListenerSets and the L4 route kinds have lenses too
	{"gateway.networking.k8s.io", "v1", "listenersets", "ListenerSet"},
	{"gateway.networking.k8s.io", "v1", "tlsroutes", "TLSRoute"},
	{"gateway.networking.k8s.io", "v1", "tcproutes", "TCPRoute"},
	{"gateway.networking.k8s.io", "v1", "udproutes", "UDPRoute"},
	{"", "v1", "configmaps", "ConfigMap"},
	{"", "v1", "secrets", "Secret"},
	{"", "v1", "persistentvolumeclaims", "PersistentVolumeClaim"},
	{"kustomize.toolkit.fluxcd.io", "v1", "kustomizations", "Kustomization"},
	{"helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease"},
	{"source.toolkit.fluxcd.io", "v1", "gitrepositories", "GitRepository"},
	{"source.toolkit.fluxcd.io", "v1", "ocirepositories", "OCIRepository"},
	{"source.toolkit.fluxcd.io", "v1", "buckets", "Bucket"},
	{"source.toolkit.fluxcd.io", "v1", "helmrepositories", "HelmRepository"},
	{"source.toolkit.fluxcd.io", "v1", "helmcharts", "HelmChart"},
	{"argoproj.io", "v1alpha1", "applications", "Application"},
	{"argoproj.io", "v1alpha1", "applicationsets", "ApplicationSet"},
	{"cert-manager.io", "v1", "certificates", "Certificate"},
	{"cert-manager.io", "v1", "issuers", "Issuer"},
	{"cert-manager.io", "v1", "clusterissuers", "ClusterIssuer"},
	{"external-secrets.io", "v1", "externalsecrets", "ExternalSecret"},
	{"external-secrets.io", "v1", "secretstores", "SecretStore"},
	{"external-secrets.io", "v1", "clustersecretstores", "ClusterSecretStore"},
}

var versionRE = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

// ParseKinds reads fleet.yaml's index.kinds (§2.2): "resource" (core),
// "group/resource", "version/resource" (core) or "group/version/resource"
// ("core" or an empty group for the core group). Entries are deduplicated
// by group and resource, the first kept; a malformed one is named in the
// warnings and ignored. A display name comes from the defaults when known.
func ParseKinds(entries []string) ([]KindRef, []string) {
	known := map[string]KindRef{}
	for _, k := range DefaultKinds {
		known[k.Key()] = k
	}
	out := []KindRef{} // an explicit empty list is no kinds, never the defaults
	var warnings []string
	seen := map[string]bool{}
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		parts := strings.Split(e, "/")
		var k KindRef
		switch len(parts) {
		case 1:
			k = KindRef{Resource: parts[0]}
		case 2:
			if versionRE.MatchString(parts[0]) {
				k = KindRef{Version: parts[0], Resource: parts[1]}
			} else {
				k = KindRef{Group: parts[0], Resource: parts[1]}
			}
		case 3:
			k = KindRef{Group: parts[0], Version: parts[1], Resource: parts[2]}
		}
		if k.Group == "core" {
			k.Group = ""
		}
		if k.Resource == "" || strings.ContainsAny(e, " \t") || (len(parts) == 3 && !versionRE.MatchString(k.Version)) || strings.ToLower(k.Resource) != k.Resource {
			warnings = append(warnings, fmt.Sprintf("index.kinds: %q is not group/resource or group/version/resource; ignored", raw))
			continue
		}
		if seen[k.Key()] {
			continue
		}
		seen[k.Key()] = true
		if d, ok := known[k.Key()]; ok {
			if k.Version == "" {
				k.Version = d.Version
			}
			k.Kind = d.Kind
		} else {
			k.Kind = k.Resource
		}
		out = append(out, k)
	}
	return out, warnings
}

// groupResource reads an Observer kind's spec GVR: a CRD kind's is
// group/resource (the group has a dot), a core kind's version/resource or
// group/version/resource.
func groupResource(spec string) (group, resource string) {
	parts := strings.Split(spec, "/")
	switch len(parts) {
	case 3:
		return parts[0], parts[2]
	case 2:
		if strings.Contains(parts[0], ".") {
			return parts[0], parts[1]
		}
		return "", parts[1]
	}
	return "", spec
}

// FromConfig reads fleet.yaml's index block into settings: the kinds (the
// defaults when absent) and the limits (each default when zero), with a
// warning for each malformed kind.
func FromConfig(kinds []string, kindsSet bool, entriesPerCluster, mibPerCluster, entriesFleet, mibFleet int) (Settings, []string) {
	var s Settings
	var warnings []string
	if kindsSet {
		s.Kinds, warnings = ParseKinds(kinds)
	}
	l := DefaultLimits
	if entriesPerCluster > 0 {
		l.ClusterEntries = entriesPerCluster
	}
	if mibPerCluster > 0 {
		l.ClusterBytes = int64(mibPerCluster) << 20
	}
	if entriesFleet > 0 {
		l.FleetEntries = entriesFleet
	}
	if mibFleet > 0 {
		l.FleetBytes = int64(mibFleet) << 20
	}
	s.Limits = l
	return s, warnings
}

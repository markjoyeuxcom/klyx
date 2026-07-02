package fleet

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"
)

// validateK8sName rejects strings that cannot be Kubernetes object names
// (DNS-1123 subdomain: lowercase alphanumerics, '-' and '.', 253 max, must
// start and end alphanumeric). The CLI argv builders splice names into kubectl
// and helm invocations - this is the bridge-side guard that keeps a hostile or
// buggy webview call from smuggling a flag ("--all" as a node name would make
// kubectl drain every node) or control characters into a command line. Real
// names always pass: node, pod, namespace, container, and helm release names
// are all DNS-1123 subdomains or stricter subsets of one.
func validateK8sName(what, name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return fmt.Errorf("invalid %s name %q: %s", what, name, errs[0])
	}
	return nil
}

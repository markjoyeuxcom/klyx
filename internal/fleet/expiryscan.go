package fleet

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/expiry"
)

// ExpiryScanResult is one cluster's expiring-material inventory plus honest
// per-source failure notes (a source that could not be read is NAMED, its
// absence never presented as "nothing expires").
type ExpiryScanResult struct {
	Items    []expiry.Item // soonest first
	Warnings []string
}

var cmCertGVRScan = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}

// ExpiryScan inventories certificate material with a lifetime:
//   - kubernetes.io/tls Secrets (leaf of tls.crt) - the certs nobody manages,
//   - Validating/MutatingWebhookConfiguration caBundles - an expired webhook
//     CA breaks the API surface,
//   - cert-manager Certificate statuses (notAfter) when the CRD is present -
//     read via a dynamic list, no lazy watch needed.
//
// The tls.crt certificate is PUBLIC material; keys are never read or carried.
// Read-only, on-demand; the UI merges results across the fleet.
func (c *ClusterConn) ExpiryScan(ctx context.Context) (ExpiryScanResult, error) {
	if c.typed == nil {
		return ExpiryScanResult{}, fmt.Errorf("cluster client not available")
	}
	out := ExpiryScanResult{}

	secrets, err := c.typed.CoreV1().Secrets("").List(ctx, metav1.ListOptions{FieldSelector: "type=kubernetes.io/tls"})
	if err != nil {
		out.Warnings = append(out.Warnings, "tls secrets: "+err.Error())
	} else {
		for i := range secrets.Items {
			s := &secrets.Items[i]
			crt, ok := s.Data["tls.crt"]
			if !ok || len(crt) == 0 {
				continue
			}
			info, perr := expiry.ParseLeaf(crt)
			if perr != nil {
				continue // unparseable material is skipped, not guessed at
			}
			out.Items = append(out.Items, expiry.Item{
				Source: "tls-secret", Namespace: s.Namespace, Name: s.Name,
				Detail: "tls.crt", Subject: info.Subject, NotAfter: info.NotAfter,
			})
		}
	}

	if vwcs, verr := c.typed.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{}); verr != nil {
		out.Warnings = append(out.Warnings, "validating webhooks: "+verr.Error())
	} else {
		for i := range vwcs.Items {
			w := &vwcs.Items[i]
			for _, hook := range w.Webhooks {
				out.Items = appendWebhookCA(out.Items, w.Name, hook.Name, hook.ClientConfig.CABundle)
			}
		}
	}
	if mwcs, merr := c.typed.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{}); merr != nil {
		out.Warnings = append(out.Warnings, "mutating webhooks: "+merr.Error())
	} else {
		for i := range mwcs.Items {
			w := &mwcs.Items[i]
			for _, hook := range w.Webhooks {
				out.Items = appendWebhookCA(out.Items, w.Name, hook.Name, hook.ClientConfig.CABundle)
			}
		}
	}

	// cert-manager statuses: authoritative renewal-managed expiry. Dynamic
	// list, tolerated as absent (no CRD -> skip silently: absence of the
	// operator is not a warning).
	if c.dyn != nil {
		if certs, cerr := c.dyn.Resource(cmCertGVRScan).List(ctx, metav1.ListOptions{}); cerr == nil && certs != nil {
			for i := range certs.Items {
				u := unstructured.Unstructured{Object: certs.Items[i].Object}
				notAfterStr, _, _ := unstructured.NestedString(u.Object, "status", "notAfter")
				if notAfterStr == "" {
					continue
				}
				notAfter, perr := time.Parse(time.RFC3339, notAfterStr)
				if perr != nil {
					continue
				}
				cn, _, _ := unstructured.NestedString(u.Object, "spec", "commonName")
				out.Items = append(out.Items, expiry.Item{
					Source: "cert-manager", Namespace: u.GetNamespace(), Name: u.GetName(),
					Detail: "renewal-managed", Subject: cn, NotAfter: notAfter,
				})
			}
		}
	}

	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].NotAfter.Before(out.Items[j].NotAfter) })
	return out, nil
}

func appendWebhookCA(items []expiry.Item, cfgName, hookName string, caBundle []byte) []expiry.Item {
	if len(caBundle) == 0 {
		return items
	}
	info, err := expiry.ParseLeaf(caBundle)
	if err != nil {
		return items
	}
	return append(items, expiry.Item{
		Source: "webhook-ca", Name: cfgName,
		Detail: hookName, Subject: info.Subject, NotAfter: info.NotAfter,
	})
}

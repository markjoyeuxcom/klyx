package fleet

import (
	"context"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/crd"
)

// readTable asks the API server for a kind's table (The Line slice 15a):
// a list (name "") or one object. A list asks includeObject=Metadata, so
// every row names its object; one object asks includeObject=Object, so a
// single response carries both the cells and the whole object - one
// snapshot, one resourceVersion (design review 1). ok=false means no table is to be had, and the caller
// reads as it did before tables: a connection without a REST client (the
// fake clients), a server that answers 406 or 415 to the table Accept, an
// answer that is not a meta.k8s.io Table, or any failed table read. A failed
// read's answer is the plain read's to give - forbidden, not found, a
// timeout - with the server's own message, which the table request's
// decoding loses (it reports "unknown").
func (c *ClusterConn) readTable(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, limit int64, continueToken string, wholeObjects bool) (*metav1.Table, bool) {
	if c.restConfig == nil {
		return nil, false
	}
	cfg := rest.CopyConfig(c.restConfig)
	cfg.GroupVersion = &schema.GroupVersion{Group: gvr.Group, Version: gvr.Version}
	cfg.APIPath = "/apis"
	if gvr.Group == "" {
		cfg.APIPath = "/api"
	}
	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	rc, err := rest.RESTClientFor(cfg)
	if err != nil {
		return nil, false
	}
	// A list of a registered payload kind asks for whole objects too: its
	// cells are evaluated locally over each sanitised object, never released
	// from the server's text (coverage S0b §3).
	include := "Metadata"
	if name != "" || wholeObjects {
		include = "Object"
	}
	req := rc.Get().Resource(gvr.Resource).SetHeader("Accept", crd.TableAccept).Param("includeObject", include)
	if ns != "" {
		req = req.Namespace(ns)
	}
	if name != "" {
		req = req.Name(name)
	}
	if limit > 0 {
		req = req.Param("limit", strconv.FormatInt(limit, 10))
	}
	if continueToken != "" {
		req = req.Param("continue", continueToken)
	}
	raw, err := req.Do(ctx).Raw()
	if err != nil {
		return nil, false
	}
	tbl, err := crd.DecodeTable(raw)
	if err != nil || tbl.Kind != "Table" {
		return nil, false
	}
	return tbl, true
}

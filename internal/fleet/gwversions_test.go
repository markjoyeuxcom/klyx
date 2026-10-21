package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

// gwDiscovery advertises gateway.networking.k8s.io at the given versions, in
// the order given (the fake's preferred version is the first), each serving
// the listed resources.
func gwDiscovery(versions ...[2]string) discoveryLike {
	byVersion := map[string][]metav1.APIResource{}
	var order []string
	for _, vr := range versions {
		if _, seen := byVersion[vr[0]]; !seen {
			order = append(order, vr[0])
		}
		if vr[1] != "" {
			byVersion[vr[0]] = append(byVersion[vr[0]], metav1.APIResource{Name: vr[1], Namespaced: true})
		} else if byVersion[vr[0]] == nil {
			byVersion[vr[0]] = []metav1.APIResource{}
		}
	}
	typed := fake.NewSimpleClientset()
	for _, v := range order {
		typed.Resources = append(typed.Resources, &metav1.APIResourceList{GroupVersion: gwapi.Group + "/" + v, APIResources: byVersion[v]})
	}
	return typed.Discovery()
}

// unreadableAt fails the resource lists of the named versions.
type unreadableAt struct {
	discoveryLike
	versions []string
}

func (d unreadableAt) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	for _, v := range d.versions {
		if strings.HasSuffix(gv, "/"+v) {
			return nil, errors.New("temporary discovery failure")
		}
	}
	return d.discoveryLike.ServerResourcesForGroupVersion(gv)
}

// Coverage S7 §2: every Gateway API resource is read at the first of its own
// candidates that is served, never the group's preferred version.
func TestGatewayVersionsFollowTheResourceCandidates(t *testing.T) {
	ctx := context.Background()
	v14 := gwDiscovery([2]string{"v1", "gateways"}, [2]string{"v1", "httproutes"}, [2]string{"v1beta1", "gateways"},
		// discovery lists v1alpha2 before v1alpha3: the order read is the candidates'
		[2]string{"v1alpha2", "tlsroutes"}, [2]string{"v1alpha2", "tcproutes"}, [2]string{"v1alpha3", "tlsroutes"})
	mixed := gwDiscovery([2]string{"v1", "gateways"}, [2]string{"v1", "tlsroutes"}, [2]string{"v1", "listenersets"},
		[2]string{"v1alpha2", "tlsroutes"}, [2]string{"v1alpha3", "tlsroutes"})
	future := gwDiscovery([2]string{"v2", "gateways"}, [2]string{"v1", "httproutes"})
	for _, tc := range []struct {
		name     string
		disc     discoveryLike
		resource string
		want     observe.Probe
		wantErr  string
	}{
		{"gateways at the preferred v1", v14, "gateways", observe.Probe{Served: true, Version: "v1"}, ""},
		{"v1.4 tlsroutes at the first served candidate, not discovery's order", v14, "tlsroutes", observe.Probe{Served: true, Version: "v1alpha3"}, ""},
		{"v1.4 tcproutes at v1alpha2", v14, "tcproutes", observe.Probe{Served: true, Version: "v1alpha2"}, ""},
		{"an experimental channel serving tlsroutes at v1 and both alphas reads v1", mixed, "tlsroutes", observe.Probe{Served: true, Version: "v1"}, ""},
		{"listenersets at v1", mixed, "listenersets", observe.Probe{Served: true, Version: "v1"}, ""},
		{"listenersets absent from a complete answer", v14, "listenersets", observe.Probe{Served: false}, ""},
		{"served only outside the candidates is a named gap", future, "gateways", observe.Probe{}, "served at v2; Klyx reads v1 or v1beta1"},
		{"a group missing from a successful discovery is absence", fake.NewSimpleClientset().Discovery(), "gateways", observe.Probe{Served: false}, ""},
		// one unreadable candidate is uncertainty: tried, never absence
		{"an unreadable candidate when none readable serves", unreadableAt{v14, []string{"v1alpha3"}}, "tlsroutes", observe.Probe{Served: true, Version: "v1alpha2"}, ""},
		{"a known served candidate wins over an unreadable earlier one", unreadableAt{mixed, []string{"v1"}}, "tlsroutes", observe.Probe{Served: true, Version: "v1alpha3"}, ""},
		{"only an unreadable candidate", unreadableAt{mixed, []string{"v1"}}, "listenersets", observe.Probe{Served: true, Version: "v1"}, ""},
		{"an unreadable non-candidate when no candidate serves is a gap", unreadableAt{future, []string{"v2"}}, "gateways", observe.Probe{}, "v2 could not be read"},
	} {
		got, err := probeConn(tc.disc).crdServedAmong(ctx, gwapi.Group, tc.resource, gwapi.Versions(tc.resource))
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: got %+v, %v; want an error naming %q", tc.name, got, err, tc.wantErr)
			}
		case err != nil || got != tc.want:
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	if _, err := probeConn(failingDiscovery{}).crdServedAmong(ctx, gwapi.Group, "gateways", gwapi.Versions("gateways")); err == nil {
		t.Error("a discovery failure must be reported, never read as absence")
	}
}

// The on-demand reads resolve the same way, and say why nothing is read.
func TestGatewayOnDemandReadsResolvePerResource(t *testing.T) {
	v14 := gwDiscovery([2]string{"v1", "gateways"}, [2]string{"v1alpha2", "tlsroutes"}, [2]string{"v1alpha3", "tlsroutes"})
	c := probeConn(v14)
	if gvr, err := c.gwGVR(context.Background(), "tlsroutes"); err != nil || gvr.Version != "v1alpha3" {
		t.Errorf("tlsroutes = %v, %v; want v1alpha3", gvr, err)
	}
	if _, err := c.gwGVR(context.Background(), "listenersets"); err == nil || !strings.Contains(err.Error(), "not served") {
		t.Errorf("an unserved resource must say so, got %v", err)
	}
	if _, err := probeConn(gwDiscovery([2]string{"v2", "gateways"})).gwGVR(context.Background(), "gateways"); err == nil || !strings.Contains(err.Error(), "Klyx reads") {
		t.Errorf("a version Klyx does not read must be named, got %v", err)
	}
	// discovery that cannot answer at all tries the first candidate
	if gvr, err := probeConn(failingDiscovery{}).gwGVR(context.Background(), "gateways"); err != nil || gvr.Version != "v1" {
		t.Errorf("failing discovery = %v, %v; want the first candidate", gvr, err)
	}
}

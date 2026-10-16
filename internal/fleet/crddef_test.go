package fleet

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// The Line slice 15c: when CRDs cannot be listed, discovery names the kinds
// the kube-apiserver does not serve itself.
func TestListCRDsFromDiscovery(t *testing.T) {
	cs := kubefake.NewSimpleClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: []string{"list"}}}},
		{GroupVersion: "example.com/v1", APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, ShortNames: []string{"wd"}, Verbs: []string{"get", "list"}},
			{Name: "widgets/status", Kind: "Widget", Namespaced: true, Verbs: []string{"get"}},
			{Name: "reviews", Kind: "Review", Verbs: []string{"create"}},
		}},
	}
	c := NewClusterConn("x", cs, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	infos, err := c.ListCRDsFromDiscovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Kind != "Widget" || infos[0].Plural != "widgets" || infos[0].Version != "v1" || infos[0].Scope != "Namespaced" || infos[0].ShortNames[0] != "wd" {
		t.Fatalf("infos %+v", infos)
	}
}

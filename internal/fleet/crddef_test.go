package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

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

// oneGroupFails serves two groups and fails one of them.
type oneGroupFails struct{}

func (oneGroupFails) ServerGroups() (*metav1.APIGroupList, error) {
	return &metav1.APIGroupList{Groups: []metav1.APIGroup{
		{Name: "works.example.com", PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "works.example.com/v1"}},
		{Name: "failed.example.com", PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "failed.example.com/v1"}},
	}}, nil
}

func (oneGroupFails) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if gv == "failed.example.com/v1" {
		return nil, errors.New("403 forbidden")
	}
	return &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Verbs: []string{"list"}}}}, nil
}

// A group discovery cannot read never hides the others and never goes
// unsaid (implementation review 1).
func TestListCRDsFromDiscovery_PartialFailureIsNamed(t *testing.T) {
	c := NewClusterConn("x", kubefake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.disc = oneGroupFails{}
	infos, err := c.ListCRDsFromDiscovery(context.Background())
	var partial *PartialDiscoveryError
	if !errors.As(err, &partial) || len(partial.Failed) != 1 || !strings.HasPrefix(partial.Failed[0], "failed.example.com: ") {
		t.Fatalf("the failed group is named: %v", err)
	}
	if len(infos) != 1 || infos[0].Group != "works.example.com" || infos[0].Kind != "Widget" {
		t.Fatalf("the read group still lists: %+v", infos)
	}
}

// The fallback is bounded by the caller's deadline, not the client's
// (implementation review 1).
func TestListCRDsFromDiscovery_HonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api" {
			_ = json.NewEncoder(w).Encode(metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}})
			return
		}
		_ = json.NewEncoder(w).Encode(metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}})
	}))
	defer server.Close()
	defer close(release)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClusterConn("x", cs, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.ListCRDsFromDiscovery(ctx)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("discovery outlived the caller's deadline: %v", took)
	}
	if err == nil {
		t.Fatal("a cancelled discovery returned success")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.ListCRDsFromDiscovery(cancelled); err == nil {
		t.Fatal("an already-cancelled context returned success")
	}
}

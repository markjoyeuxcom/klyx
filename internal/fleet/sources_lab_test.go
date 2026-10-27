package fleet_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3a §9, the lab: a real Alertmanager in the kind lab, read
// through its host as an installation, serving the lab cluster by name
// with an extra match key. Read-only; it refuses any context but the lab
// cluster, and the lab's namespace must carry the lab's label.
//
//	KLYX_S3A_LAB_CONTEXT=kind-klyx go test ./internal/fleet -run '^TestInstallationLab$' -v -count=1
//
// The lab's alerts are the S8a lab's (scratchpad lab-s8a/alerts.sh): those
// labelled cluster=kind-klyx pass the acquisition filter; the ones without a
// namespace then match no member's scope; cluster=elsewhere never reaches
// the read.
func TestInstallationLab(t *testing.T) {
	ctxName := os.Getenv("KLYX_S3A_LAB_CONTEXT")
	if ctxName == "" {
		t.Skip("set KLYX_S3A_LAB_CONTEXT=kind-klyx to run the installation lab")
	}
	if ctxName != "kind-klyx" {
		t.Fatalf("the installation lab runs on kind-klyx only, not %q", ctxName)
	}
	const ns = "klyx-s8-lab"
	cc := config.ClusterConfig{Name: "kind-klyx", Context: ctxName,
		Alertmanager: &config.EndpointConfig{Source: "lab-am", Match: map[string]string{"namespace": ns}}}
	rc, err := cluster.RESTConfig(cc)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	nsObj, err := typed.CoreV1().Namespaces().Get(context.Background(), ns, metav1.GetOptions{})
	if err != nil || nsObj.Labels["klyx.dev/lab"] != "s8a" {
		t.Fatalf("the lab namespace %s is not the S8a lab's: %v", ns, err)
	}
	src := config.SourceConfig{ID: "lab-am", Kind: config.SourceKindAlertmanager, Host: "kind-klyx",
		ServiceRef: &config.MetricsServiceRef{Namespace: ns, Name: "alertmanager", Port: "web"}, MemberLabel: "cluster",
		Serves: []config.ServeConfig{{Cluster: "kind-klyx"}}}
	cfg := &config.Config{Clusters: []config.ClusterConfig{cc}, Sources: []config.SourceConfig{src}}
	if _, err := config.ResolveAlertmanager(cfg.Sources, cfg.Clusters); err != nil {
		t.Fatal(err)
	}
	creds := fleet.Credentials{Store: &secretstore.Memory{}, Revisions: endpointcred.NewRegistry()}
	reg := fleet.NewRegistry(cfg, fleet.SubscribingFactory(cfg.Sources, fleet.DefaultConnFactory(clock.Real{}, creds)))
	sources, err := fleet.NewAlertSources(cfg.Sources, cfg.Clusters)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.EnableAlertSources(ctx, sources, creds, clock.Real{})
	reg.Start(ctx)
	conn, ok := reg.Conn("kind-klyx")
	if !ok {
		t.Fatal("no connection to the lab")
	}
	c := conn.(*fleet.ClusterConn)
	defer c.Stop(10 * time.Second)

	var r alertmanager.Read
	deadline := time.Now().Add(2 * time.Minute)
	for {
		rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
		got, _, err := c.ReadAlertsAt(rctx, c.AlertsIdentity())
		if errors.Is(err, fleet.ErrSourceChanged) {
			got, _, err = c.ReadAlertsAt(rctx, got.Identity)
		}
		rcancel()
		if err == nil {
			r = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lab installation was not read: %v", err)
		}
		time.Sleep(2 * time.Second) // the connection is still syncing
	}
	var names []string
	for _, a := range r.Alerts {
		names = append(names, a.Display.Name)
	}
	sort.Strings(names)
	got := strings.Join(names, ",")
	for _, want := range []string{"KlyxLabPodCrashLooping", "KlyxLabDeploymentReplicasMismatch", "KlyxLabNamespaceQuota", "KlyxLabSilenced"} {
		if !strings.Contains(got, want) {
			t.Errorf("the member's share lacks %s: %s", want, got)
		}
	}
	for _, never := range []string{"KlyxLabElsewhere", "KlyxLabClusterWide", "Watchdog", "KlyxLabNodeMemoryPressure"} {
		if strings.Contains(got, never) {
			t.Errorf("the member's share holds %s, outside its scope: %s", never, got)
		}
	}
	read, ok := reg.InstallationRead("lab-am")
	if !ok || read.Unmatched != 3 || read.Err != "" {
		t.Errorf("the lab's alerts without a namespace match no member: %+v", read)
	}
	if !strings.HasPrefix(r.Source, "from lab-am, hosted by kind-klyx, filtered by cluster=kind-klyx, namespace="+ns) {
		t.Errorf("scope sentence: %q", r.Source)
	}
	if r.Lineage.Installation != "lab-am" || r.Lineage.Host != "kind-klyx" || r.Lineage.HostConn == 0 || r.Lineage.InstRev == 0 || r.Lineage.SubRev == 0 {
		t.Errorf("lineage: %+v", r.Lineage)
	}
	t.Logf("share: %s; unmatched %d; %s", got, read.Unmatched, r.Source)
}

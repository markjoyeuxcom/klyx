package appbridge

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/crd"
)

// Codex's batch-2 review probes, adopted as regression tests.

// R5: a compute that STARTED before a reconnect must not certify "live" for
// the new watch, even if it finishes after it.
func TestLiveList_ComputeSpanningReconnectDoesNotCertifyLive(t *testing.T) {
	r := newLiveRegistry()
	r.interval = time.Hour // no ticker recompute during the test
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	edges := make(chan bool, 10)
	var onLive func(bool)
	go func() {
		r.open("review",
			func(_ func(), live func(bool)) (func(), error) { onLive = live; live(true); return func() {}, nil },
			func() (any, bool) { close(started); <-release; return "pre-reconnect snapshot", true },
			func(any) {}, func(v bool) { edges <- v })
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("compute did not start")
	}
	onLive(false)
	onLive(true)
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("open did not finish")
	}
	defer r.closeAll()
	for len(edges) > 0 {
		if <-edges {
			t.Fatal("a pre-reconnect compute certified live before any post-reconnect compute")
		}
	}
}

// R3: a read failure on a connected cluster reaches the frontend as an error
// DTO, not just live=false (which left the view loading with no reason).
func TestLiveList_ReadFailureReachesFrontend(t *testing.T) {
	conn := &fakeLivePodConn{computeFail: true}
	em := &anyEmitter{}
	svc := NewEventsService(func(string) (EventsConn, bool) { return conn, true }, em)
	svc.live.interval = time.Hour
	defer svc.CloseAll()
	if r := svc.OpenLiveEvents("c", "ns"); !r.OK {
		t.Fatalf("open failed: %+v", r)
	}
	for _, ev := range em.snapshot() {
		if dto, ok := ev.data.(EventsResultDTO); ok && dto.Error != "" {
			return
		}
	}
	t.Fatal("an RBAC/timeout read failure must be emitted with its reason")
}

// R1: a reference KEY without the reference SHAPE (here inside chart values)
// is not exempt from redaction - neither in redactManifestForAI nor in the
// prompt SuggestFix actually sends.
func TestAI_ReferenceKeyInValuesIsRedacted(t *testing.T) {
	const raw = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
spec:
  values:
    credentials:
      secretRef:
        password: b2-review-private-password
`
	out, _, err := redactManifestForAI(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "b2-review-private-password") {
		t.Fatalf("credential reached the AI manifest: %s", out)
	}

	root := crd.InstanceDetail{Kind: "HelmRelease", Namespace: "app", Name: "app", YAML: raw}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"helm.toolkit.fluxcd.io/helmreleases/app/app": root}}
	cl := &fakeCompleter{reply: "ok"}
	svc := newAI(cl, "hosted", true, false, conn)
	if r := svc.SuggestFix("c", "helm.toolkit.fluxcd.io", "v2", "helmreleases", "app", "app", "app-uid"); !r.OK {
		t.Fatalf("fix failed: %+v", r)
	}
	if strings.Contains(cl.gotUser, "b2-review-private-password") {
		t.Fatal("credential under a values secretRef reached the model prompt")
	}
}

// Codex batch-2 re-review R1 probes, adopted: reference SHAPE alone does not
// prove a reference - in free-form data the value must still be redacted from
// the actual prompt, while a genuine Pod reference stays readable.
func TestAI_ShapeAloneDoesNotProveReference(t *testing.T) {
	cases := []struct{ name, group, plural, kind, raw string }{
		{"ConfigMap string data key", "", "configmaps", "ConfigMap", `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  secretName: b2-rereview-inline-credential
`},
		{"Helm values inherited sensitive identifier", "helm.toolkit.fluxcd.io", "helmreleases", "HelmRelease", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
spec:
  values:
    credentials:
      secretRef:
        name: chart-option
        key: b2-rereview-inline-credential
`},
		{"Helm values inherited sensitive scalar", "helm.toolkit.fluxcd.io", "helmreleases", "HelmRelease", `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: app
spec:
  values:
    credentials:
      existingSecret: b2-rereview-inline-credential
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := crd.InstanceDetail{Kind: tc.kind, Namespace: "app", Name: "app", YAML: tc.raw}
			conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{tc.group + "/" + tc.plural + "/app/app": root}}
			cl := &fakeCompleter{reply: "ok"}
			svc := newAI(cl, "hosted", true, false, conn)
			if r := svc.SuggestFix("c", tc.group, "v1", tc.plural, "app", "app", "app-uid"); !r.OK {
				t.Fatalf("fix failed: %+v", r)
			}
			if strings.Contains(cl.gotUser, "b2-rereview-inline-credential") {
				t.Fatal("an inline credential with a reference shape reached the model prompt")
			}
		})
	}
}

// Codex batch-2 final R1 probes, adopted: a foreign CRD's credential subtree
// that reuses the Pod parent keys (secret, valueFrom) is not an API reference
// location - the root kind plus the full path decides - so its value must not
// reach the actual prompt.
func TestAI_ForeignCredentialParentIsNotAPIReference(t *testing.T) {
	cases := map[string]string{
		"secret parent is not a volume": `apiVersion: example.com/v1
kind: Widget
metadata:
  name: app
spec:
  credentials:
    secret:
      secretName: b2-final-private-value
`,
		"valueFrom parent is not pod env": `apiVersion: example.com/v1
kind: Widget
metadata:
  name: app
spec:
  credentials:
    valueFrom:
      secretKeyRef:
        name: custom-auth
        key: b2-final-private-value
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			root := crd.InstanceDetail{Kind: "Widget", Namespace: "app", Name: "app", YAML: raw}
			conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"example.com/widgets/app/app": root}}
			cl := &fakeCompleter{reply: "ok"}
			svc := newAI(cl, "hosted", true, false, conn)
			if r := svc.SuggestFix("c", "example.com", "v1", "widgets", "app", "app", "app-uid"); !r.OK {
				t.Fatalf("fix failed: %+v", r)
			}
			if strings.Contains(cl.gotUser, "b2-final-private-value") {
				t.Fatal("a foreign CRD credential under a matching parent key reached the model prompt")
			}
		})
	}
}

// A workload Pod template's references stay readable at the kind's real path
// (CronJob nests the template two levels deeper), for init containers too.
func TestAI_WorkloadTemplateReferenceRemainsReadable(t *testing.T) {
	const raw = `apiVersion: batch/v1
kind: CronJob
metadata:
  name: app
spec:
  jobTemplate:
    spec:
      template:
        spec:
          imagePullSecrets:
          - name: cron-pull
          initContainers:
          - name: migrate
            envFrom:
            - secretRef:
                name: cron-env
`
	out, _, err := redactManifestForAI(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"cron-pull", "cron-env"} {
		if !strings.Contains(out, ref) {
			t.Errorf("CronJob template reference %q must stay readable:\n%s", ref, out)
		}
	}
}

func TestAI_GenuinePodReferenceRemainsReadable(t *testing.T) {
	const raw = `apiVersion: v1
kind: Pod
metadata:
  name: app
spec:
  volumes:
  - name: creds
    secret:
      secretName: app-secret-name
  containers:
  - name: app
    image: nginx
    env:
    - name: API_TOKEN
      valueFrom:
        secretKeyRef:
          name: app-secret-name
          key: token
`
	out, _, err := redactManifestForAI(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "app-secret-name") != 2 {
		t.Fatalf("genuine Pod references (volume + env) must stay readable:\n%s", out)
	}
}

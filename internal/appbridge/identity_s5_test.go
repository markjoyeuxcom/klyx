package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	wid "github.com/moomora/klyx/internal/identity"
)

const s5Needle = "S5LABneedle"

type fakeIdentityConn struct{ sa wid.SAReport }

func (f fakeIdentityConn) ServiceAccountIdentity(context.Context, string, string, string) (wid.SAReport, error) {
	return f.sa, nil
}
func (f fakeIdentityConn) PodIdentity(context.Context, string, string, string) (wid.PodInspectorReport, error) {
	return wid.PodInspectorReport{}, nil
}
func (f fakeIdentityConn) StoreIdentity(context.Context, string, string, string, string) (wid.StoreReport, error) {
	return wid.StoreReport{}, nil
}

func needlePod(i int) wid.Pod {
	return wid.PodOf(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: fmt.Sprintf("p%03d", i), UID: "u"},
		Spec: corev1.PodSpec{ServiceAccountName: "web", Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{
			{Name: "DB_PASSWORD", Value: s5Needle},
			{Name: wid.EnvRoleARN, Value: "arn:aws:iam::123456789012:role/" + s5Needle + "?q"},
			{Name: wid.EnvCredsURI, Value: "http://u:" + s5Needle + "@169.254.170.23/v1/credentials"},
			{Name: wid.EnvAzureAuthHost, Value: "https://login.example.com/" + s5Needle},
			{Name: wid.EnvAzureClient, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: s5Needle}}},
		}}}}})
}

// Coverage S5 §7: no non-identity env value, no credential planted in an
// allowed variable and no Secret reference crosses the reply; §6: the reply
// is bounded, pods dropped from the end and counted.
func TestS5IdentityReplyBoundary(t *testing.T) {
	d := wid.DeclaredOf(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web",
		Annotations: map[string]string{wid.AnnRoleARN: s5Needle, wid.AnnAzureClient: s5Needle}}})
	var pods []wid.PodReport
	for i := 0; i < 50; i++ {
		pods = append(pods, wid.PodReportOf(d, needlePod(i), wid.Env{}))
	}
	svc := NewIdentityService(func(string) (IdentityConn, bool) {
		return fakeIdentityConn{sa: wid.SAReport{State: wid.StateRead, Declared: &d, Pods: pods}}, true
	})
	out := svc.ServiceAccount("c", "app", "web", "u")
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), s5Needle) {
		t.Fatalf("a protected value crossed: %s", raw[:400])
	}
	// the bound: many large pods drop from the end, counted
	big := wid.SAReport{State: wid.StateRead}
	for i := 0; i < 200; i++ {
		p := needlePod(i)
		for j := 0; j < 15; j++ {
			p.Containers = append(p.Containers, p.Containers[0])
		}
		big.Pods = append(big.Pods, wid.PodReport{Pod: p})
	}
	svc = NewIdentityService(func(string) (IdentityConn, bool) { return fakeIdentityConn{sa: big}, true })
	out = svc.ServiceAccount("c", "app", "web", "u")
	raw, _ = json.Marshal(out)
	if len(raw) > maxIdentityDTO || out.PodsDropped == 0 || len(out.Report.Pods)+out.PodsDropped != 200 {
		t.Fatalf("bound: %d bytes, dropped %d, kept %d", len(raw), out.PodsDropped, len(out.Report.Pods))
	}
}

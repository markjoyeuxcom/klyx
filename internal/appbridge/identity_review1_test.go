package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	wid "github.com/moomora/klyx/internal/identity"
)

// Implementation review 1 (S5 #2): every reply holds the bound, measured
// over the final DTO with its omission counts.

type boundIdentityConn struct {
	pod   wid.PodInspectorReport
	store wid.StoreReport
}

func (f boundIdentityConn) ServiceAccountIdentity(context.Context, string, string, string) (wid.SAReport, error) {
	return wid.SAReport{}, nil
}
func (f boundIdentityConn) PodIdentity(context.Context, string, string, string) (wid.PodInspectorReport, error) {
	return f.pod, nil
}
func (f boundIdentityConn) StoreIdentity(context.Context, string, string, string, string) (wid.StoreReport, error) {
	return f.store, nil
}

// a pod at every documented per-field bound: 16 containers, each with
// identity values just under 512 bytes
func boundPod(name string) wid.Pod {
	role := "arn:aws:iam::123456789012:role/" + strings.Repeat("a", 450)
	path := "/" + strings.Repeat("b", 470)
	var cs []corev1.Container
	for i := 0; i < wid.MaxContainers; i++ {
		cs = append(cs, corev1.Container{Name: fmt.Sprintf("c%d", i), Env: []corev1.EnvVar{{Name: wid.EnvRoleARN, Value: role}, {Name: wid.EnvWebTokenFile, Value: path},
			{Name: wid.EnvCredsToken, Value: path}, {Name: wid.EnvAzureToken, Value: path}}})
	}
	return wid.PodOf(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "eso", Name: name, UID: types.UID("u-" + name)}, Spec: corev1.PodSpec{Containers: cs}})
}

func TestReview1EveryReplyBound(t *testing.T) {
	f := boundIdentityConn{store: wid.StoreReport{State: wid.StateRead, Handlers: &wid.Handlers{}}}
	for g := 0; g < 3; g++ {
		group := wid.Group{Namespace: "eso", ServiceAccount: fmt.Sprintf("sa-%d", g), Candidate: "eso/eso"}
		for n := 0; n < 10; n++ {
			group.Pods = append(group.Pods, boundPod(fmt.Sprintf("p%d-%d", g, n)))
		}
		f.store.Handlers.Groups = append(f.store.Handlers.Groups, group)
	}
	big := boundPod("big")
	for len(big.Containers) < 200 { // past any one pod's documented bound, to force the trim
		c := big.Containers[0]
		c.Name = fmt.Sprintf("x%d", len(big.Containers))
		big.Containers = append(big.Containers, c)
	}
	pr := wid.PodReportOf(wid.Declared{}, big, wid.Env{})
	f.pod = wid.PodInspectorReport{State: wid.StateRead, Report: &pr}
	svc := NewIdentityService(func(string) (IdentityConn, bool) { return f, true })

	st := svc.Store("c", "SecretStore", "app", "s", "u")
	raw, _ := json.Marshal(st)
	kept := 0
	for _, g := range st.Report.Handlers.Groups {
		kept += len(g.Pods)
	}
	if len(raw) > maxIdentityDTO || st.PodsDropped == 0 || kept+st.PodsDropped != 30 || len(st.Report.Handlers.Groups) != 3 || st.Error != "" {
		t.Fatalf("store: %d bytes, kept %d, dropped %d, groups %d, err %q", len(raw), kept, st.PodsDropped, len(st.Report.Handlers.Groups), st.Error)
	}
	pd := svc.Pod("c", "eso", "big", "u")
	raw, _ = json.Marshal(pd)
	if len(raw) > maxIdentityDTO || pd.ContainersDropped == 0 || pd.Report.Report == nil || len(pd.Report.Report.Pod.Containers)+pd.ContainersDropped != 200 || pd.Error != "" {
		t.Fatalf("pod: %d bytes, dropped %d, err %q", len(raw), pd.ContainersDropped, pd.Error)
	}
	if len(pd.Report.Report.Readings) != len(pr.Readings) {
		t.Fatalf("the readings stay: %d of %d", len(pd.Report.Report.Readings), len(pr.Readings))
	}
}

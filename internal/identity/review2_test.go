package identity

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Implementation review 2 (S5): the re-review's regressions.

// #1: a relative token path (or a drive-relative one) resolves against a
// working directory Klyx does not read: unresolved, never compared; a
// relative mount path never matches.
func TestReview2RelativePathsAreUnresolved(t *testing.T) {
	for _, file := range []string{"tokens/token", `C:tokens\token`, "token"} {
		p := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, file), []corev1.VolumeMount{{Name: "aws", MountPath: "/tokens"}}, []corev1.Volume{tokenVol("aws", "token", AudienceSTS)})
		m := p.Containers[0].Mechanisms[0]
		r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), p, PlatformOther), AdapterAWS)
		if m.State != ChainUnresolved || !strings.Contains(m.Missing, "relative") || r.State != StateUnresolved {
			t.Errorf("%q: %+v / %+v", file, m, r)
		}
	}
	for _, file := range []string{"/tokens/token", `C:\tokens\token`, `C:/tokens/token`, `\tokens\token`} {
		p := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, file), []corev1.VolumeMount{{Name: "aws", MountPath: "/tokens"}}, []corev1.Volume{tokenVol("aws", "token", AudienceSTS)})
		if m := p.Containers[0].Mechanisms[0]; m.State != ChainComplete {
			t.Errorf("%q is absolute: %+v", file, m)
		}
	}
	rel := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, "/tokens/token"), []corev1.VolumeMount{{Name: "aws", MountPath: "tokens"}}, []corev1.Volume{tokenVol("aws", "token", AudienceSTS)})
	if m := rel.Containers[0].Mechanisms[0]; m.State != ChainPartial || !strings.Contains(m.Missing, "no mount") {
		t.Errorf("a relative mount path never matches: %+v", m)
	}
}

// #3: an identity value past MaxValue is never compared: a token audience
// (even equal to the declaration), and a declared role, audience, client
// or tenant.
func TestReview2OversizedNeverCompared(t *testing.T) {
	big := strings.Repeat("x", MaxValue+1)
	p := irsaPod(roleA, func(p *corev1.Pod) { p.Spec.Volumes[0].Projected.Sources[0].ServiceAccountToken.Audience = big })
	m := p.Containers[0].Mechanisms[0]
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA, AnnAudience: big}), p, PlatformOther), AdapterAWS); m.State != ChainUnresolved || r.State == StateMatch {
		t.Fatalf("an oversized token audience: %+v / %+v", m, r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA, AnnAudience: big}), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateUnresolved || !strings.Contains(r.Text, AnnAudience) {
		t.Fatalf("an oversized declared audience: %+v", r)
	}
	bigRole := "arn:aws:iam::123456789012:role/" + strings.Repeat("r", MaxValue)
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: bigRole}), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateUnresolved || !strings.Contains(r.Text, AnnRoleARN) {
		t.Fatalf("an oversized declared role: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnAzureClient: guidA, AnnAzureTenant: big}), azurePod(guidA, guidB), PlatformOther), AdapterAzure); r.State != StateUnresolved || !strings.Contains(r.Text, AnnAzureTenant) {
		t.Fatalf("an oversized declared tenant: %+v", r)
	}
}

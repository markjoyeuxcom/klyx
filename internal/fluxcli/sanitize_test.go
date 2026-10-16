package fluxcli

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const diffNeedle = "FLUXDIFFneedle3R"

// flux diff output with a payload kind's drift beside an ordinary one, a
// recursive Kustomization and a credential URL, as flux v2.9 prints it.
var fluxDiffOut = strings.Join([]string{
	"► Deployment/shop/web drifted",
	"",
	"spec.replicas",
	"  ± value change",
	"    - 2",
	"    + 3",
	"",
	"► HelmRelease/shop/api drifted",
	"",
	"spec.values.password",
	"  ± value change",
	"    - old" + diffNeedle,
	"    + new" + diffNeedle,
	"",
	"📁 Kustomization/flux-system/apps changed",
	"► ResourceSet/shop/tenants drifted",
	"spec.resourcesTemplate",
	"  ± " + diffNeedle,
	"► ConfigMap/shop/cfg drifted",
	"data.url",
	"  + https://bob:" + diffNeedle + "@git.example/repo",
	"► Kustomization/flux-system/infra created",
}, "\n")

func TestSanitizeDiffWithholdsPayloadBlocks(t *testing.T) {
	got := sanitizeDiff(fluxDiffOut)
	if strings.Contains(got, diffNeedle) {
		t.Fatalf("payload or credential crossed:\n%s", got)
	}
	for _, want := range []string{
		"► Deployment/shop/web drifted", "    + 3", // ordinary kinds keep their changes
		"► HelmRelease/shop/api drifted\n" + DiffWithheld,
		"📁 Kustomization/flux-system/apps changed\n► ResourceSet/shop/tenants drifted\n" + DiffWithheld,
		"► ConfigMap/shop/cfg drifted\ndata.url\n  + https://git.example/repo",
		"► Kustomization/flux-system/infra created",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSanitizeErrorWithholdsProtectedSubjects(t *testing.T) {
	stderr := "✗ HelmRelease/shop/api dry-run failed (Invalid): HelmRelease.helm.toolkit.fluxcd.io \"api\" is invalid: spec.values: Invalid value: " + diffNeedle + "\n" +
		"  more text " + diffNeedle + "\nSecret/shop/s dry-run failed: admission webhook \"x\" denied the request: " + diffNeedle
	got := sanitizeError(stderr)
	if strings.Contains(got, diffNeedle) {
		t.Fatalf("crossed: %s", got)
	}
	for _, want := range []string{"HelmRelease/shop/api dry-run failed (Invalid)", "Secret/shop/s dry-run failed", ErrorWithheld} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	plain := "✗ Deployment/shop/web dry-run failed (Invalid): spec.replicas: Invalid value: -1 from https://u:" + diffNeedle + "@h/x"
	if got := sanitizeError(plain); strings.Contains(got, diffNeedle) || !strings.Contains(got, "Invalid value: -1") {
		t.Errorf("an ordinary kind keeps its message, URL-scrubbed: %s", got)
	}
}

type fixedRunner struct {
	stdout, stderr string
	err            error
}

func (f fixedRunner) Run(context.Context, ...string) ([]byte, string, error) {
	return []byte(f.stdout), f.stderr, f.err
}

// The sanitiser runs inside DiffKustomization, so every consumer of the
// result (the Flux inspector, AI evidence) receives withheld text.
func TestDiffKustomizationSanitises(t *testing.T) {
	r := DiffKustomization(context.Background(), fixedRunner{stdout: fluxDiffOut, stderr: "HelmRelease/shop/api dry-run failed: " + diffNeedle, err: errors.New("exit status 1")}, "", "flux-system", "apps", "/tmp/x")
	if strings.Contains(r.Output+r.Warning+r.Err, diffNeedle) || !r.HasChanges {
		t.Fatalf("result: %+v", r)
	}
	r = DiffKustomization(context.Background(), fixedRunner{stderr: "ResourceSet/shop/t dry-run failed: " + diffNeedle, err: errors.New("exit status 1")}, "", "flux-system", "apps", "/tmp/x")
	if strings.Contains(r.Err, diffNeedle) || r.Err == "" {
		t.Fatalf("err: %+v", r)
	}
}

package fluxcli

import (
	"context"
	"errors"
	"os"
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
	// ordinary kinds before the first payload block keep their changes
	if !strings.Contains(got, "► Deployment/shop/web drifted") || !strings.Contains(got, "    + 3") {
		t.Errorf("an ordinary block lost its changes:\n%s", got)
	}
	// the payload block's body, and everything after it, is withheld
	if !strings.HasSuffix(got, "► HelmRelease/shop/api drifted\n"+DiffWithheld) {
		t.Errorf("not withheld from the payload block on:\n%s", got)
	}
	for _, after := range []string{"ConfigMap/shop/cfg", "ResourceSet/shop/tenants", "Kustomization/flux-system/infra"} {
		if strings.Contains(got, after) {
			t.Errorf("%s follows the payload block and must be withheld:\n%s", after, got)
		}
	}
	// before any payload block, the URL rule applies, and a recursive
	// Kustomization's header does not withhold its children
	pre := "📁 Kustomization/flux-system/apps changed\n► ConfigMap/shop/cfg drifted\ndata.url\n  + https://bob:" + diffNeedle + "@git.example/repo"
	if got := sanitizeDiff(pre); strings.Contains(got, diffNeedle) || !strings.Contains(got, "  + https://git.example/repo") {
		t.Errorf("before a payload block:\n%s", got)
	}
}

// Codex's S0b re-review, finding 2: real flux v2.9.6 output in which a
// HelmRelease values key carries a line that looks like a header. The
// value after it must stay withheld.
func TestSanitizeDiffForgedHeaderInBody(t *testing.T) {
	raw, err := os.ReadFile("testdata/diff-forged-header.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "R1NEEDLE") || !strings.Contains(string(raw), "ConfigMap/shop/pretend drifted") {
		t.Fatal("fixture: the captured output must hold the forged header and the value")
	}
	if got := sanitizeDiff(string(raw)); strings.Contains(got, "R1NEEDLE") {
		t.Fatalf("a forged header ended the withholding:\n%s", got)
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
	// no protected subject is no evidence of safety: withheld whole
	plain := "✗ Deployment/shop/web dry-run failed (Invalid): spec.replicas: Invalid value: -1 from https://u:" + diffNeedle + "@h/x"
	if got := sanitizeError(plain); got != UnclassifiedWithheld {
		t.Errorf("an unclassified error: %s", got)
	}
}

// Codex's S0b re-review, finding 3: real flux v2.9.6 stderr from a build
// whose inline patch is echoed before any object is named.
func TestSanitizeErrorBuildEchoesPatch(t *testing.T) {
	raw, err := os.ReadFile("testdata/build-error-patch.txt")
	if err != nil {
		t.Fatal(err)
	}
	r := DiffKustomization(context.Background(), fixedRunner{stderr: string(raw), err: errors.New("exit status 1")}, "", "shop", "apps", "/tmp/x")
	if strings.Contains(r.Err, "R1NEEDLE") || r.Err != UnclassifiedWithheld {
		t.Fatalf("err: %q", r.Err)
	}
	// Go's own error, with no flux text, is kept
	r = DiffKustomization(context.Background(), fixedRunner{err: errors.New("exec: \"flux\": executable file not found in $PATH")}, "", "shop", "apps", "/tmp/x")
	if !strings.Contains(r.Err, "executable file not found") {
		t.Fatalf("a runner error: %q", r.Err)
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

package appbridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// AI A1 §3.2, §6 and §8: two renders compared through chat's projection,
// with every raw change the projection does not show found as a path
// marker - and no hidden value, in any form, in the result.

var testResources = map[string]string{
	"Deployment": "deployments", "Secret": "secrets", "ConfigMap": "configmaps", "HelmRelease": "helmreleases",
	"ResourceSet": "resourcesets", "ResourceClaimTemplate": "resourceclaimtemplates", "Service": "services",
}

func testResolve(gv schema.GroupVersion, kind string) schema.GroupVersionResource {
	r, ok := testResources[kind]
	if !ok {
		r = strings.ToLower(kind) + "s"
	}
	return gv.WithResource(r)
}

func deploy(name, image, logLevel string, command ...any) *unstructured.Unstructured {
	c := map[string]any{"name": "web", "image": image, "env": []any{map[string]any{"name": "LOG_LEVEL", "value": logLevel}}}
	if len(command) > 0 {
		c["command"] = command
	}
	return uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": "shop"},
		"spec": map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{c}}}}})
}

func object(apiVersion, kind, ns, name string, rest map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns}}
	for k, v := range rest {
		o[k] = v
	}
	return uobj(o)
}

func withAnnotation(u *unstructured.Unstructured, k, v string) *unstructured.Unstructured {
	u = u.DeepCopy()
	u.SetAnnotations(map[string]string{k: v})
	return u
}

func changesOf(t *testing.T, old, new []*unstructured.Unstructured) ([]chatRenderChange, int, string) {
	t.Helper()
	p := &projector{}
	cs, total := compareRenders(old, new, testResolve, p)
	b, err := json.Marshal(cs)
	if err != nil {
		t.Fatal(err)
	}
	return cs, total, string(b)
}

func find(cs []chatRenderChange, kind, name string) *chatRenderChange {
	for i := range cs {
		if cs[i].Kind == kind && cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func hiddenPaths(c *chatRenderChange) string {
	var out []string
	for _, h := range c.Hidden {
		out = append(out, h.Path+" "+h.Change)
	}
	return strings.Join(out, "; ")
}

func TestRenderDiffNeverSendsAHiddenValue(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	needles := []string{"NeedleEnvOld", "NeedleEnvNew", "NeedleCmdOld", "NeedleCmdNew", "NeedleSecretOld", "NeedleSecretNew",
		b64([]byte("NeedleSecretOld")), b64([]byte("NeedleSecretNew")), "NeedleCfgA", "NeedleCfgB", "NeedleTagOld", "NeedleTagNew",
		"NeedleAnnOld", "NeedleAnnNew", "NeedleInputOld", "NeedleInputNew", "NeedleCELOld", "NeedleCELNew"}
	secret := func(v string) *unstructured.Unstructured {
		return object("v1", "Secret", "shop", "creds", map[string]any{"type": "Opaque", "data": map[string]any{"password": b64([]byte(v))}})
	}
	cfg := func(v string) *unstructured.Unstructured {
		return object("v1", "ConfigMap", "shop", "settings", map[string]any{"data": map[string]any{"app.ini": "level=" + v}})
	}
	hr := func(tag string) *unstructured.Unstructured {
		return object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "shop", "web", map[string]any{"spec": map[string]any{"interval": "5m",
			"chart": map[string]any{"spec": map[string]any{"chart": "web"}}, "values": map[string]any{"image": map[string]any{"tag": tag}}}})
	}
	rs := func(v string) *unstructured.Unstructured {
		return object("fluxcd.controlplane.io/v1", "ResourceSet", "shop", "tenants", map[string]any{"spec": map[string]any{
			"inputs": []any{map[string]any{"password": v}}}})
	}
	claim := func(cel string) *unstructured.Unstructured {
		return object("resource.k8s.io/v1", "ResourceClaimTemplate", "shop", "gpu", map[string]any{"spec": map[string]any{"spec": map[string]any{
			"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu.example.com",
				"selectors": []any{map[string]any{"cel": map[string]any{"expression": cel}}}}}}}}}})
	}
	svc := object("v1", "Service", "shop", "web", map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80)}}}})
	old := []*unstructured.Unstructured{deploy("web", "ghcr.io/acme/web:1.0", "NeedleEnvOld", "run", "NeedleCmdOld"), secret("NeedleSecretOld"),
		cfg("NeedleCfgA"), hr("NeedleTagOld"), withAnnotation(svc, "acme.io/note", "NeedleAnnOld"), rs("NeedleInputOld"), claim("NeedleCELOld")}
	new := []*unstructured.Unstructured{deploy("web", "ghcr.io/acme/web:1.1", "NeedleEnvNew", "run", "NeedleCmdNew"), secret("NeedleSecretNew"),
		cfg("NeedleCfgB"), hr("NeedleTagNew"), withAnnotation(svc, "acme.io/note", "NeedleAnnNew"), rs("NeedleInputNew"), claim("NeedleCELNew")}
	cs, total, out := changesOf(t, old, new)
	for _, n := range needles {
		if strings.Contains(out, n) {
			t.Fatalf("hidden value %q in the result: %s", n, out)
		}
	}
	if total != 7 || len(cs) != 7 {
		t.Fatalf("%d changed: %s", total, out)
	}
	d := find(cs, "Deployment", "web")
	if len(d.Visible) != 1 || d.Visible[0].Path != "spec.template.spec.containers[web].image" || d.Visible[0].Old != "ghcr.io/acme/web:1.0" || d.Visible[0].New != "ghcr.io/acme/web:1.1" {
		t.Fatalf("the image is a visible change at its manifest path: %+v", d.Visible)
	}
	if got := hiddenPaths(d); got != "spec.template.spec.containers[web].command changed (not shown); spec.template.spec.containers[web].env[LOG_LEVEL] changed (not shown)" {
		t.Fatalf("env and command are markers: %s", got)
	}
	for kind, want := range map[string]string{
		"Secret":      "data.password changed (not shown)",
		"ConfigMap":   "data[app.ini] changed (not shown)",
		"HelmRelease": "spec.values changed (not shown)",
		"Service":     "metadata.annotations[acme.io/note] changed (not shown)",
		// S0b's free-form rule keeps a payload's keys (chat sends them) and
		// omits its values: the marker names no more than chat already does
		"ResourceSet": "spec.inputs[0].password changed (not shown)",
		// the DRA projection re-shapes the spec: the marker stops at it
		"ResourceClaimTemplate": "spec changed (not shown)",
	} {
		var c *chatRenderChange
		for i := range cs {
			if cs[i].Kind == kind {
				c = &cs[i]
			}
		}
		if c == nil || len(c.Visible) != 0 || hiddenPaths(c) != want {
			t.Fatalf("%s: a change the projection does not show is a marker, and only that: %+v", kind, c)
		}
	}
}

func TestRenderDiffSaysAConfigMapsSizeAndItsMarker(t *testing.T) {
	cfg := func(v string) *unstructured.Unstructured {
		return object("v1", "ConfigMap", "shop", "settings", map[string]any{"data": map[string]any{"app.ini": v}})
	}
	cs, _, out := changesOf(t, []*unstructured.Unstructured{cfg("a=NeedleX")}, []*unstructured.Unstructured{cfg("a=NeedleLonger")})
	c := cs[0]
	if len(c.Visible) != 1 || c.Visible[0].Path != "spec.dataKeyBytes[app.ini]" || c.Visible[0].Old != "9" || c.Visible[0].New != "14" ||
		hiddenPaths(&c) != "data[app.ini] changed (not shown)" || strings.Contains(out, "Needle") {
		t.Fatalf("a size change is visible; the value change is still a marker: %s", out)
	}
}

func TestRenderDiffSaysObjectsThatMoveAppearOrGoByIdentity(t *testing.T) {
	cm := func(ns, v string) *unstructured.Unstructured {
		return object("v1", "ConfigMap", ns, "settings", map[string]any{"data": map[string]any{"k": v}})
	}
	same := deploy("same", "x:1", "a")
	cs, total, out := changesOf(t, []*unstructured.Unstructured{cm("a", "NeedleMoved"), deploy("gone", "x:1", "NeedleGone"), same},
		[]*unstructured.Unstructured{cm("b", "NeedleMoved"), deploy("web", "x:1", "NeedleNew"), same.DeepCopy()})
	if total != 4 || strings.Contains(out, "Needle") {
		t.Fatalf("%s", out)
	}
	for _, c := range cs {
		if c.Change == "changed" || len(c.Visible) != 0 || len(c.Hidden) != 0 {
			t.Fatalf("an object that appears or goes is said by its identity only: %+v", c)
		}
	}
	if find(cs, "ConfigMap", "settings").Change != "removed" || cs[1].Namespace != "b" || cs[1].Change != "added" {
		t.Fatalf("a moved object is removed in one namespace and added in another: %s", out)
	}
}

func TestRenderDiffSaysEncryptedChanges(t *testing.T) {
	sec := func(v string) *unstructured.Unstructured {
		return object("v1", "Secret", "shop", "creds", map[string]any{"stringData": map[string]any{"token": v}})
	}
	d := func(v string) *unstructured.Unstructured {
		u := deploy("web", "x:1", "info")
		_ = unstructured.SetNestedField(u.Object, v, "metadata", "labels", "sops")
		return u
	}
	cs, _, out := changesOf(t, []*unstructured.Unstructured{sec("ENC[AES256_GCM,data:NeedleOne,type:str]"), d("ENC[AES256_GCM,data:NeedleL1]")},
		[]*unstructured.Unstructured{sec("ENC[AES256_GCM,data:NeedleTwo,type:str]"), d("ENC[AES256_GCM,data:NeedleL2]")})
	if strings.Contains(out, "Needle") {
		t.Fatalf("%s", out)
	}
	if got := hiddenPaths(find(cs, "Secret", "creds")); got != "stringData.token changed (encrypted)" {
		t.Fatalf("%s", got)
	}
	v := find(cs, "Deployment", "web").Visible
	if len(v) != 1 || v[0].Path != "metadata.labels.sops" || v[0].Note != "changed (encrypted)" || v[0].Old != "" {
		t.Fatalf("an encrypted visible field shows no ciphertext: %+v", v)
	}
}

func TestRenderDiffBounds(t *testing.T) {
	var old, new []*unstructured.Unstructured
	for i := 0; i < 25; i++ {
		n := fmt.Sprintf("app-%02d", i)
		old = append(old, deploy(n, "x:1", "a"))
		new = append(new, deploy(n, "x:2", "a"))
	}
	labels := func(v string) *unstructured.Unstructured {
		u := deploy("aaa-labelled", "x:1", v)
		l := map[string]any{}
		for i := 0; i < 30; i++ {
			l[fmt.Sprintf("l%02d", i)] = v
		}
		_ = unstructured.SetNestedMap(u.Object, l, "metadata", "labels")
		return u
	}
	old, new = append([]*unstructured.Unstructured{labels("a")}, old...), append([]*unstructured.Unstructured{labels("b")}, new...)
	cs, total, _ := changesOf(t, old, new)
	if total != 26 || len(cs) != renderObjectsMax {
		t.Fatalf("%d of %d", len(cs), total)
	}
	c := find(cs, "Deployment", "aaa-labelled")
	if c == nil || c.PathsTotal != 31 || len(c.Visible)+len(c.Hidden) != renderPathsMax || len(c.Hidden) != 1 ||
		c.Hidden[0].Path != "spec.template.spec.containers[web].env[LOG_LEVEL]" {
		t.Fatalf("visible changes never crowd out a marker: %+v", c)
	}
}

func TestRenderDiffNamesNothingInsideHelmValues(t *testing.T) {
	hr := func(v string) *unstructured.Unstructured {
		return object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "shop", "web", map[string]any{"spec": map[string]any{"interval": "5m",
			"values": map[string]any{"env": map[string]any{"data": map[string]any{"NeedleKey": v}}}}})
	}
	cs, _, out := changesOf(t, []*unstructured.Unstructured{hr("a")}, []*unstructured.Unstructured{hr("b")})
	if hiddenPaths(&cs[0]) != "spec.values changed (not shown)" || strings.Contains(out, "Needle") {
		t.Fatalf("a field named like a hidden subtree inside Helm values is not named: %s", out)
	}
}

func TestRenderDiffNamesAPodTemplatesLabelsNotTheirValues(t *testing.T) {
	d := func(v string) *unstructured.Unstructured {
		u := deploy("web", "x:1", "a")
		_ = unstructured.SetNestedField(u.Object, v, "spec", "template", "metadata", "labels", "build")
		return u
	}
	cs, _, out := changesOf(t, []*unstructured.Unstructured{d("NeedleB1")}, []*unstructured.Unstructured{d("NeedleB2")})
	if len(cs[0].Visible) != 0 || hiddenPaths(&cs[0]) != "spec.template.metadata.labels changed (not shown)" || strings.Contains(out, "Needle") {
		t.Fatalf("%s", out)
	}
}

func TestRenderDiffAnnotationOnlyChangeNamesTheAnnotationOnly(t *testing.T) {
	// A-C1's counterexample: an annotation-only diff names the annotation,
	// nothing else
	svc := object("v1", "Service", "shop", "web", map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80)}}}})
	cs, _, _ := changesOf(t, []*unstructured.Unstructured{withAnnotation(svc, "acme.io/rollout", "1")},
		[]*unstructured.Unstructured{withAnnotation(svc, "acme.io/rollout", "2")})
	if len(cs) != 1 || len(cs[0].Visible) != 0 || hiddenPaths(&cs[0]) != "metadata.annotations[acme.io/rollout] changed (not shown)" {
		t.Fatalf("%+v", cs)
	}
}

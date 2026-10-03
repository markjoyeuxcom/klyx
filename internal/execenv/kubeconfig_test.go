package execenv

import (
	"reflect"
	"testing"
)

func TestKubeconfigEnv(t *testing.T) {
	if got := KubeconfigEnv(""); got != nil {
		t.Errorf("empty kubeconfig: got %v, want nil (inherit default resolution)", got)
	}
	if got := KubeconfigEnv("   "); got != nil {
		t.Errorf("blank kubeconfig: got %v, want nil", got)
	}
	want := []string{"KUBECONFIG=/a/dev:/a/shared"}
	if got := KubeconfigEnv("/a/dev:/a/shared"); !reflect.DeepEqual(got, want) {
		t.Errorf("path list: got %v, want %v (list passed through intact)", got, want)
	}
}

func TestEnviron(t *testing.T) {
	if got := Environ(nil); got != nil {
		t.Errorf("no extra: got %d entries, want nil so os/exec inherits", len(got))
	}
	t.Setenv("KUBECONFIG", "/inherited")
	got := Environ([]string{"KUBECONFIG=/selected"})
	if len(got) == 0 || got[len(got)-1] != "KUBECONFIG=/selected" {
		t.Fatalf("extra entry must come last so it wins over the inherited value; got tail %v", got[len(got)-1:])
	}
}

package keda

import "testing"

const img = "ghcr.io/kedacore/keda:2.21.0"

func pod(env map[string]EnvEntry, envFrom bool, image string) OperatorPod {
	return OperatorPod{Name: "p", Namespace: "keda", ServiceAccount: "keda-operator", Image: image, Env: env, EnvFrom: envFrom, Readable: true}
}

func lit(v string) EnvEntry { return EnvEntry{Literal: true, Value: v} }

// §6.1: how one allowlisted name resolves.
func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		name  string
		p     OperatorPod
		res   Resolution
		unset bool
		value string
	}{
		{"a literal value", pod(map[string]EnvEntry{EnvWatchNamespace: lit("team")}, false, img), Resolved, false, "team"},
		{"valueFrom is unresolved", pod(map[string]EnvEntry{EnvWatchNamespace: {ValueFrom: true}}, false, img), Unresolved, false, ""},
		{"no entry with envFrom is unresolved", pod(nil, true, img), Unresolved, false, ""},
		{"no entry, KEDA's own image: unset", pod(nil, false, img), Resolved, true, ""},
		{"no entry, a custom image: unresolved", pod(nil, false, "registry.example/keda:2.21.0"), Unresolved, false, ""},
		{"an unreadable pod", OperatorPod{Image: img}, Unresolved, false, ""},
	} {
		s := resolveOne(tc.p, EnvWatchNamespace)
		if s.Res != tc.res || s.Unset != tc.unset || s.Value != tc.value || (s.Res == Unresolved && s.Reason == "") {
			t.Errorf("%s: %+v", tc.name, s)
		}
	}
	// a rollout: the running pods disagree
	c := ConfigOf("keda-operator", "keda", []OperatorPod{
		pod(map[string]EnvEntry{EnvWatchNamespace: lit("a"), EnvWatchSelector: lit("")}, false, img),
		pod(map[string]EnvEntry{EnvWatchNamespace: lit("b"), EnvWatchSelector: lit("")}, false, "ghcr.io/kedacore/keda:2.20.2"),
	})
	if c.Settings[EnvWatchNamespace].Res != Unresolved || len(c.Disagree) != 1 || c.Version.Known {
		t.Fatalf("rollout: %+v", c)
	}
	if one := ConfigOf("keda-operator", "keda", []OperatorPod{pod(nil, false, img)}); !one.Version.Known || one.Version.Minor != 21 || one.ServiceAccount != "keda-operator" {
		t.Fatalf("one pod: %+v", one)
	}
}

func cfg(ns string, env map[string]EnvEntry) Config {
	return ConfigOf("keda-operator", ns, []OperatorPod{pod(env, false, img)})
}

// §6.1: the handler comes from the user's own scope; empty scopes as KEDA
// reads them; invalid selectors and cut labels are unresolved.
func TestHandler(t *testing.T) {
	all := cfg("keda", map[string]EnvEntry{EnvWatchNamespace: lit("")})
	quoted := cfg("keda", map[string]EnvEntry{EnvWatchNamespace: lit(`""`), EnvWatchSelector: lit(`""`)})
	teams := cfg("keda-teams", map[string]EnvEntry{EnvWatchNamespace: lit("team-a,team-b")})
	prod := cfg("keda-prod", map[string]EnvEntry{EnvWatchNamespace: lit(""), EnvWatchSelector: lit("env=prod")})
	bad := cfg("keda-bad", map[string]EnvEntry{EnvWatchNamespace: lit(""), EnvWatchSelector: lit("env in (")})
	unres := ConfigOf("keda-x", "keda-x", []OperatorPod{pod(map[string]EnvEntry{EnvWatchNamespace: {ValueFrom: true}}, false, img)})

	if all.Watches("any", nil, false) != Yes || quoted.Watches("any", nil, false) != Yes {
		t.Fatal("empty scopes are unscoped")
	}
	if teams.Watches("team-b", nil, false) != Yes || teams.Watches("team-c", nil, false) != No {
		t.Fatal("the namespace list")
	}
	if prod.Watches("x", map[string]string{"env": "prod"}, false) != Yes || prod.Watches("x", map[string]string{"env": "dev"}, false) != No ||
		prod.Watches("x", map[string]string{"env": "dev"}, true) != Unknown {
		t.Fatal("the label selector, and cut labels")
	}
	if bad.Watches("x", nil, false) != Unknown {
		t.Fatal("an invalid selector is unresolved")
	}
	if h := HandlerOf([]Config{teams, prod}, "team-a", map[string]string{"env": "dev"}, false); h.Handler == nil || h.Handler.Namespace != "keda-teams" {
		t.Fatalf("one handler: %+v", h)
	}
	if h := HandlerOf([]Config{teams}, "team-z", nil, false); !h.None {
		t.Fatalf("no handler: %+v", h)
	}
	if h := HandlerOf([]Config{all, teams}, "team-a", nil, false); h.Handler != nil || len(h.Candidates) != 2 {
		t.Fatalf("two handlers are unverified: %+v", h)
	}
	if h := HandlerOf([]Config{teams, unres}, "team-a", nil, false); h.Handler != nil || len(h.Candidates) != 2 {
		t.Fatalf("an unresolved candidate makes it unverified: %+v", h)
	}
	// authentication visibility is separate from handling
	authSel := cfg("keda", map[string]EnvEntry{EnvWatchNamespace: lit(""), EnvWatchAuthSelector: lit("keda=yes")})
	if h := HandlerOf([]Config{authSel}, "team", nil, false); h.Handler == nil {
		t.Fatal("the authentication selector decided the handler")
	}
	if authSel.SeesAuth(map[string]string{"keda": "no"}, false) != No || authSel.SeesAuth(map[string]string{"keda": "yes"}, false) != Yes {
		t.Fatal("authentication visibility")
	}
}

// §6.2's namespace table.
func TestReadNamespace(t *testing.T) {
	ta := &Auth{Meta: Meta{Namespace: "team", Name: "ta"}}
	cta := &Auth{Meta: Meta{Name: "cta"}, Cluster: true}
	secret, cm := Ref{Kind: "Secret", Name: "s"}, Ref{Kind: "ConfigMap", Name: "c"}
	plain := cfg("keda", nil)
	restricted := cfg("keda", map[string]EnvEntry{EnvRestrictSecretAcces: lit("TRUE")})
	override := cfg("keda", map[string]EnvEntry{EnvClusterObjectNS: lit("keda-objects"), EnvRestrictSecretAcces: lit("true")})
	unres := ConfigOf("keda-operator", "keda", []OperatorPod{pod(nil, true, img)})
	for _, tc := range []struct {
		name string
		c    Config
		a    *Auth
		r    Ref
		ns   string
		ok   bool
	}{
		{"a TriggerAuthentication's Secret", plain, ta, secret, "team", true},
		{"restricted: the cluster-object namespace", restricted, ta, secret, "keda", true},
		{"restricted does not move a ConfigMap", restricted, ta, cm, "team", true},
		{"restricted with an override", override, ta, secret, "keda-objects", true},
		{"a ClusterTriggerAuthentication: the operator's namespace", plain, cta, secret, "keda", true},
		{"a ClusterTriggerAuthentication: the override", override, cta, cm, "keda-objects", true},
		{"unresolved env", unres, cta, secret, "", false},
		{"unresolved restriction", unres, ta, secret, "", false},
		{"a ConfigMap does not need the restriction", unres, ta, cm, "team", true},
	} {
		if ns, ok := tc.c.ReadNamespace(tc.a, tc.r); ns != tc.ns || ok != tc.ok {
			t.Errorf("%s: %q %v", tc.name, ns, ok)
		}
	}
}

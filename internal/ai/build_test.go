package ai

import (
	"errors"
	"strings"
	"testing"
)

// fakeKeys is an in-memory KeyStore; err, when set, is what every call returns.
type fakeKeys struct {
	keys map[ProviderID]string
	err  error
}

func (f *fakeKeys) Get(p ProviderID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if k, ok := f.keys[p]; ok {
		return k, nil
	}
	return "", ErrNoKey
}
func (f *fakeKeys) Set(p ProviderID, k string) error { f.keys[p] = k; return f.err }
func (f *fakeKeys) Delete(p ProviderID) error        { delete(f.keys, p); return f.err }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestBuild_Off(t *testing.T) {
	for _, p := range []ProviderID{"", ProviderOff} {
		b, err := Build(Settings{Provider: p}, nil, nil)
		if b != nil || err != nil {
			t.Fatalf("%q: want (nil, nil), got (%v, %v)", p, b, err)
		}
	}
}

func TestBuild_AnthropicDefaults(t *testing.T) {
	b, err := Build(Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain}, &fakeKeys{keys: map[ProviderID]string{ProviderAnthropic: "sk-ant-k"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Model != DefaultAnthropicModel || b.Origin != DefaultAnthropicOrigin || b.OnMachine || b.KeySource != KeySourceKeychain {
		t.Fatalf("built = %+v", b)
	}
	if b.Provider == nil || b.Lister == nil {
		t.Fatal("provider and lister must be set")
	}
}

// One explicit source, no fallback (spec §2.1).
func TestBuild_KeySources(t *testing.T) {
	keys := &fakeKeys{keys: map[ProviderID]string{}}
	cases := []struct {
		name string
		s    Settings
		k    KeyStore
		env  map[string]string
		want func(*Built, error) bool
	}{
		{"keychain empty is no key, even with an env key around", Settings{Provider: ProviderOpenAI, Model: "m", KeySource: KeySourceKeychain, APIKeyEnv: "OPENAI_API_KEY"}, keys, map[string]string{"OPENAI_API_KEY": "sk-env"},
			func(_ *Built, err error) bool { return errors.Is(err, ErrNoKey) }},
		{"keychain locked fails closed, distinct from no key", Settings{Provider: ProviderOpenAI, Model: "m", KeySource: KeySourceKeychain}, &fakeKeys{err: &KeychainError{Err: errors.New("locked")}}, nil,
			func(_ *Built, err error) bool {
				var kc *KeychainError
				return errors.As(err, &kc) && !errors.Is(err, ErrNoKey)
			}},
		{"none means no key for a hosted vendor", Settings{Provider: ProviderGemini, Model: "m", KeySource: KeySourceNone, LegacyKey: "legacy-key"}, keys, nil,
			func(_ *Built, err error) bool { return errors.Is(err, ErrNoKey) }},
		{"env reads only the named variable", Settings{Provider: ProviderGemini, Model: "m", KeySource: KeySourceEnv, APIKeyEnv: "MY_GEMINI"}, keys, map[string]string{"MY_GEMINI": "AIza-env"},
			func(b *Built, err error) bool { return err == nil && b.KeySource == KeySourceEnv }},
		{"env unset is an error", Settings{Provider: ProviderGemini, Model: "m", KeySource: KeySourceEnv, APIKeyEnv: "MY_GEMINI"}, keys, nil,
			func(_ *Built, err error) bool { return err != nil && strings.Contains(err.Error(), "MY_GEMINI") }},
		{"legacy key works", Settings{Provider: ProviderAnthropic, KeySource: KeySourceLegacy, LegacyKey: "sk-ant-legacy"}, keys, nil,
			func(b *Built, err error) bool { return err == nil && b.KeySource == KeySourceLegacy }},
		{"a local server needs no key", Settings{Provider: ProviderOpenAICompatible, Model: "llama", BaseURL: "http://localhost:11434", KeySource: KeySourceKeychain}, keys, nil,
			func(b *Built, err error) bool { return err == nil && b.OnMachine }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Build(tc.s, tc.k, env(tc.env))
			if !tc.want(b, err) {
				t.Fatalf("got (%+v, %v)", b, err)
			}
		})
	}
}

// Origins (spec §1.2): HTTPS for anything that receives a key, no userinfo,
// query or fragment; a keyless LAN server may be HTTP and is reported as
// off-machine.
func TestBuild_Origins(t *testing.T) {
	keys := &fakeKeys{keys: map[ProviderID]string{ProviderAnthropic: "sk-ant-k", ProviderOpenAICompatible: "local-key"}}
	cases := []struct {
		name    string
		s       Settings
		wantErr string
		origin  string
		onMach  bool
	}{
		{"custom https origin for a vendor", Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain, BaseURL: "https://gateway.example:8443/anthropic"}, "", "https://gateway.example:8443", false},
		{"http origin for a vendor key", Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain, BaseURL: "http://gateway.example"}, "HTTPS", "", false},
		{"userinfo refused", Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain, BaseURL: "https://u:p@gateway.example"}, "username", "", false},
		{"query refused", Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain, BaseURL: "https://gateway.example/?key=x"}, "query", "", false},
		{"keyless LAN server over http", Settings{Provider: ProviderOpenAICompatible, Model: "m", KeySource: KeySourceNone, BaseURL: "http://192.168.1.10:11434"}, "", "http://192.168.1.10:11434", false},
		{"credentialed LAN server over http", Settings{Provider: ProviderOpenAICompatible, Model: "m", KeySource: KeySourceKeychain, BaseURL: "http://192.168.1.10:11434"}, "HTTPS", "", false},
		{"credentialed loopback over http", Settings{Provider: ProviderOpenAICompatible, Model: "m", KeySource: KeySourceKeychain, BaseURL: "http://127.0.0.1:11434"}, "", "http://127.0.0.1:11434", true},
		{"tlsSkipVerify only for a local server", Settings{Provider: ProviderAnthropic, KeySource: KeySourceKeychain, TLSSkipVerify: true}, "tlsSkipVerify", "", false},
		{"CLI not in this build", Settings{Provider: ProviderClaudeCLI}, "not available", "", false},
		{"unknown provider", Settings{Provider: "wat"}, "unknown", "", false},
		{"OpenAI needs a model", Settings{Provider: ProviderOpenAI, KeySource: KeySourceLegacy, LegacyKey: "sk"}, "model", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Build(tc.s, keys, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got (%+v, %v)", tc.wantErr, b, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if b.Origin != tc.origin || b.OnMachine != tc.onMach {
				t.Fatalf("origin=%q onMachine=%v, want %q %v", b.Origin, b.OnMachine, tc.origin, tc.onMach)
			}
		})
	}
}

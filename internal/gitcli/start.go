package gitcli

import (
	"context"
)

// Start prepares Git access once at startup (slice 6 note §3): it resolves
// and version-checks git, reads the owner's credential helpers, creates the
// cache root (base, localstore's GitCache) and writes the generated global
// config. Any failure is an
// *ErrOff naming why, and every Git feature stays off.
func Start(ctx context.Context, base string, env Env) (Runner, Helpers, error) {
	g, err := Locate(ctx, env)
	if err != nil {
		return Runner{}, Helpers{}, err
	}
	h := ReadHelpers(ctx, g, env)
	if base == "" {
		return Runner{}, h, &ErrOff{Reason: "there is no fleet file to keep the Git cache beside"}
	}
	if err := EnsureBase(base); err != nil {
		return Runner{}, h, &ErrOff{Reason: "the Git cache folder could not be created: " + err.Error()}
	}
	path := GlobalConfigPath(base)
	if err := WriteGlobalConfig(path, h, ""); err != nil {
		return Runner{}, h, &ErrOff{Reason: "the generated git config could not be written: " + err.Error()}
	}
	return Runner{Git: g, Env: env, GlobalConfig: path}, h, nil
}

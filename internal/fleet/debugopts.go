package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Coverage S6 §6: the debug verb's image and profile, and the kubectl
// version gate. kubectl debug --profile's ephemeral-container effects were
// read from kubectl's source and are identical from 1.30.0 to 1.37.1
// (1.27's restricted lacked allowPrivilegeEscalation and seccomp; sysadmin
// arrived in 1.30); the flag's default changed over time, so Klyx always
// passes the profile.

// DebugOptions are the debug verb's choices.
type DebugOptions struct {
	Image   string
	Profile string
}

const (
	DefaultDebugImage   = "busybox:1.36"
	DefaultDebugProfile = "general"
	// The kubectl range whose profile effects are verified.
	debugMinMinor      = 30
	debugVerifiedMinor = 37
)

// DebugProfiles is every offered profile and what it adds to the debug
// container (legacy is not offered: deprecated, removed in kubectl 1.39).
var DebugProfiles = map[string]string{
	"general":    "adds SYS_PTRACE, for inspecting the target's processes where the runtime supports --target",
	"baseline":   "no added privilege (clears the security context)",
	"restricted": "non-root, all capabilities dropped, no privilege escalation, RuntimeDefault seccomp; the image must run as non-root unless the pod sets runAsUser",
	"netadmin":   "adds NET_ADMIN and NET_RAW",
	"sysadmin":   "privileged: this container has full access to the node",
}

// imageRef is an OCI reference: an optional registry host (with port), a
// lower-case path, and a tag and/or digest.
var imageRef = regexp.MustCompile(`^(([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)(\.([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?))*(:[0-9]{1,5})?/)?[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(/[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

// ValidateDebugOptions refuses an unknown profile or an image that is not
// an OCI reference; empty fields take the defaults.
func ValidateDebugOptions(o DebugOptions) (DebugOptions, error) {
	if o.Image == "" {
		o.Image = DefaultDebugImage
	}
	if o.Profile == "" {
		o.Profile = DefaultDebugProfile
	}
	if _, ok := DebugProfiles[o.Profile]; !ok {
		return o, fmt.Errorf("unknown debug profile %q", o.Profile)
	}
	if len(o.Image) > 255 || !imageRef.MatchString(o.Image) {
		return o, fmt.Errorf("%q is not an image reference (registry/path:tag or @sha256 digest)", o.Image)
	}
	return o, nil
}

// DebugKubectl is what the gate read of the kubectl the terminal runs.
type DebugKubectl struct {
	Path    string
	Version string // gitVersion, e.g. v1.37.1
	Minor   int
	// OK: the debug verb may launch. Reason says why not.
	OK     bool
	Reason string
	// Qualified: newer than the verified range, said in the confirmation.
	Qualified string
}

// DebugKubectlCheck reads the client version of the same resolved kubectl
// binary the terminal command runs. Unresolved, unreadable or older than
// 1.30.0 refuses; newer than 1.37 is qualified.
func (c *ClusterConn) DebugKubectlCheck(ctx context.Context) DebugKubectl {
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return DebugKubectl{Reason: "kubectl was not found by Klyx, so the kubectl the terminal would run cannot be checked"}
	}
	out := DebugKubectl{Path: bin}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, bin, "version", "--client", "-o", "json").Output() //nolint:gosec // fixed argv, binary resolved via clibin
	if err != nil {
		out.Reason = "the kubectl client version could not be read: " + err.Error()
		return out
	}
	var v struct {
		ClientVersion struct {
			Major, Minor, GitVersion string
		} `json:"clientVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.ClientVersion.Major != "1" {
		out.Reason = "the kubectl client version could not be read"
		return out
	}
	minor, err := strconv.Atoi(strings.TrimRight(v.ClientVersion.Minor, "+"))
	if err != nil {
		out.Reason = "the kubectl client version could not be read"
		return out
	}
	out.Version, out.Minor = v.ClientVersion.GitVersion, minor
	return gateDebugKubectl(out)
}

func gateDebugKubectl(k DebugKubectl) DebugKubectl {
	switch {
	case k.Minor < debugMinMinor:
		k.OK, k.Reason = false, fmt.Sprintf("kubectl %s is older than 1.30, whose debug profile effects Klyx has verified", k.Version)
	case k.Minor > debugVerifiedMinor:
		k.OK, k.Qualified = true, fmt.Sprintf("profile effects verified for kubectl 1.30-1.%d; this is %s", debugVerifiedMinor, k.Version)
	default:
		k.OK = true
	}
	return k
}

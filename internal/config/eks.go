package config

import (
	"regexp"
	"strings"
)

// AWS region and 12-digit account shapes. Region tolerates the multi-segment
// gov/iso partitions (us-gov-west-1) beyond the common two-segment form.
var (
	eksRegionRe  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)
	eksAccountRe = regexp.MustCompile(`^\d{12}$`)
)

// EKSContext is the parsed identity encoded in AWS' default kubeconfig context
// ARN: arn:aws:eks:<region>:<account>:cluster/<name>.
type EKSContext struct {
	Partition string
	Region    string
	Account   string
	Name      string
}

// ParseEKSContext returns the EKS identity encoded in an AWS-generated
// kubeconfig context ARN. Non-EKS context names return ok=false.
func ParseEKSContext(ctx string) (out EKSContext, ok bool) {
	parts := strings.SplitN(ctx, ":", 6)
	if len(parts) != 6 {
		return EKSContext{}, false
	}
	if parts[0] != "arn" || parts[2] != "eks" {
		return EKSContext{}, false
	}
	if !eksRegionRe.MatchString(parts[3]) || !eksAccountRe.MatchString(parts[4]) {
		return EKSContext{}, false
	}
	const prefix = "cluster/"
	// SplitN(...,6) leaves any further ':' inside parts[5]; a real EKS resource
	// segment (cluster/<name>) never carries one, so reject it.
	if strings.Contains(parts[5], ":") || !strings.HasPrefix(parts[5], prefix) {
		return EKSContext{}, false
	}
	name := strings.TrimPrefix(parts[5], prefix)
	if name == "" {
		return EKSContext{}, false
	}
	return EKSContext{
		Partition: parts[1],
		Region:    parts[3],
		Account:   parts[4],
		Name:      name,
	}, true
}

// ClusterConfigForContext builds the fleet entry used when Settings imports a
// kubeconfig context. Plain contexts remain name=context. EKS contexts get a
// human name while retaining the ARN as the kube context and useful AWS tags.
func ClusterConfigForContext(ctx string) ClusterConfig {
	cc := ClusterConfig{Name: ctx, Context: ctx}
	if eks, ok := ParseEKSContext(ctx); ok {
		cc.Name = eks.Name
		cc.Tags = tagsForEKS(eks, nil)
	}
	// Default a production-looking hot-added cluster to protected, and persist it
	// so the protection survives restart and shows in Settings - never fail open
	// on a prod context imported mid-session. IsProtected also infers at guard
	// time, but recording it keeps disk and enforcement in agreement.
	cc.Protected = cc.IsProtected()
	return cc
}

// EffectiveTags returns explicit tags plus Klyx-derived identity tags for
// contexts whose provider identity is encoded in the kubeconfig context name.
// Explicit user tags always win.
func (c ClusterConfig) EffectiveTags() map[string]string {
	out := copyTags(c.Tags)
	key := c.Context
	if key == "" {
		key = c.Name
	}
	if eks, ok := ParseEKSContext(key); ok {
		out = tagsForEKS(eks, out)
	}
	return out
}

func (c *ClusterConfig) applyDerivedDefaults() {
	tags := c.EffectiveTags()
	if len(tags) > 0 {
		c.Tags = tags
	}
}

func tagsForEKS(eks EKSContext, tags map[string]string) map[string]string {
	out := copyTags(tags)
	if out == nil {
		out = map[string]string{}
	}
	setDefault(out, "account", eks.Account)
	setDefault(out, "cloud", "aws")
	setDefault(out, "provider", "eks")
	setDefault(out, "region", eks.Region)
	if eks.Partition != "" && eks.Partition != "aws" {
		setDefault(out, "partition", eks.Partition)
	}
	return out
}

func copyTags(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func setDefault(m map[string]string, k, v string) {
	if v == "" {
		return
	}
	if m[k] == "" {
		m[k] = v
	}
}

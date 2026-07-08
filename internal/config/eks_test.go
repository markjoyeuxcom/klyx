package config

import "testing"

func TestParseEKSContext(t *testing.T) {
	ctx := "arn:aws:eks:us-east-1:934692410245:cluster/eks-tooling"
	got, ok := ParseEKSContext(ctx)
	if !ok {
		t.Fatal("want EKS context parsed")
	}
	if got.Partition != "aws" || got.Region != "us-east-1" || got.Account != "934692410245" || got.Name != "eks-tooling" {
		t.Fatalf("parsed wrong: %+v", got)
	}
}

func TestParseEKSContextRejectsNonEKS(t *testing.T) {
	for _, ctx := range []string{
		"homelab-nelli",
		"arn:aws:iam::934692410245:role/admin",
		"arn:aws:eks:us-east-1:934692410245:nodegroup/eks-tooling/ng/abc",
		"arn:aws:eks:us-east-1:934692410245:cluster/",
		"arn:aws:eks:not-a-region:934692410245:cluster/eks-tooling", // malformed region
		"arn:aws:eks:us-east-1:12345:cluster/eks-tooling",           // short account
		"arn:aws:eks:us-east-1:934692410245:cluster/foo:bar",        // extra ':' in resource
	} {
		if got, ok := ParseEKSContext(ctx); ok {
			t.Fatalf("%q parsed unexpectedly as %+v", ctx, got)
		}
	}
}

func TestClusterConfigForContextEnrichesEKS(t *testing.T) {
	cc := ClusterConfigForContext("arn:aws:eks:eu-west-2:123456789012:cluster/prd-platform")
	if cc.Name != "prd-platform" {
		t.Fatalf("name: %q", cc.Name)
	}
	if cc.Context != "arn:aws:eks:eu-west-2:123456789012:cluster/prd-platform" {
		t.Fatalf("context: %q", cc.Context)
	}
	if cc.Tags["provider"] != "eks" || cc.Tags["cloud"] != "aws" || cc.Tags["region"] != "eu-west-2" || cc.Tags["account"] != "123456789012" {
		t.Fatalf("tags wrong: %+v", cc.Tags)
	}
}

func TestEffectiveTagsDerivesEKSWithoutOverwritingUserTags(t *testing.T) {
	cc := ClusterConfig{
		Name:    "friendly",
		Context: "arn:aws-us-gov:eks:us-gov-west-1:123456789012:cluster/friendly",
		Tags:    map[string]string{"provider": "custom", "env": "lab"},
	}
	got := cc.EffectiveTags()
	if got["provider"] != "custom" || got["env"] != "lab" {
		t.Fatalf("explicit tags must win: %+v", got)
	}
	if got["cloud"] != "aws" || got["region"] != "us-gov-west-1" || got["account"] != "123456789012" || got["partition"] != "aws-us-gov" {
		t.Fatalf("derived tags missing: %+v", got)
	}
}

func TestClusterConfigForContextDefaultsProdProtected(t *testing.T) {
	// A production-looking context is marked protected and recorded so it persists.
	prod := ClusterConfigForContext("arn:aws:eks:eu-west-1:123456789012:cluster/eks-prd-we")
	if !prod.Protected {
		t.Fatalf("prod-named EKS context must default to protected: %+v", prod)
	}
	// A non-prod context stays unprotected.
	dev := ClusterConfigForContext("arn:aws:eks:eu-west-1:123456789012:cluster/eks-dev-we")
	if dev.Protected {
		t.Fatalf("non-prod context must not be protected: %+v", dev)
	}
	// A plain (non-EKS) prod context too.
	if plain := ClusterConfigForContext("prod-cluster"); !plain.Protected {
		t.Fatalf("plain prod context must default to protected: %+v", plain)
	}
}

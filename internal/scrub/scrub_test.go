package scrub

import (
	"strings"
	"testing"
)

func TestCredentialsReplacesShapesWhole(t *testing.T) {
	in := "fetch https://alice:review-password@repo.invalid/a failed; token=review-credential; Authorization: Bearer abcdefghijklmnop"
	out, n := Credentials(in)
	for _, leak := range []string{"alice", "review-password", "review-credential", "abcdefghijklmnop"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q survived: %s", leak, out)
		}
	}
	if n < 3 || !strings.Contains(out, "https://[redacted]@repo.invalid/a") {
		t.Fatalf("replacements %d: %s", n, out)
	}
}

// Coverage S10 §8: the identifier shapes, each replaced whole.
func TestIdentifiers(t *testing.T) {
	cases := map[string]string{
		"subnet /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-x/providers/Microsoft.Network/virtualNetworks/v/subnets/s not found": "subnet <azure-resource-id> not found",
		"(/SUBSCRIPTIONS/AAAAAAAA-0000-0000-0000-000000000000/resourcegroups/RG)":                                                                          "(<azure-resource-id>)",
		"role arn:aws:iam::123456789012:role/NodeRole, denied":                                                                                             "role <aws-arn>, denied",
		"arn:aws-us-gov:ec2:us-gov-west-1:123456789012:instance/i-0abc":                                                                                    "<aws-arn>",
		"subnets subnet-0a1b2c3d4e5f60718 and sg-0a1b2c3d; ami-12345678 vpc-0a1b2c3d4e5f60718":                                                             "subnets <aws-id> and <aws-id>; <aws-id> <aws-id>",
		"lt-0123456789abcdef0 cr-0a1b2c3d4e5f60718 pg-0a1b2c3d eni-0a1b2c3d":                                                                               "<aws-id> <aws-id> <aws-id> <aws-id>",
		// not identifiers: too short, not hex, or another prefix
		"subnet-123 sg-zzzzzzzz ami-0a1b2c3d4e i-0a1b2c3d4e5f60718 my-subnets": "subnet-123 sg-zzzzzzzz ami-0a1b2c3d4e i-0a1b2c3d4e5f60718 my-subnets",
	}
	for in, want := range cases {
		if got, _ := Identifiers(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	if _, n := Identifiers("a subnet-0a1b2c3d4e5f60718 and an arn:aws:iam::1:role/x"); n != 2 {
		t.Errorf("count %d", n)
	}
}

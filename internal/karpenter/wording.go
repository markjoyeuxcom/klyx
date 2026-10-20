package karpenter

// Klyx's fixed wording (coverage S10 §8). Karpenter's condition messages
// never cross to AI or chat, for any Karpenter kind: a provider writes
// identifiers into them (an Azure subnet's ARM ID, an instance profile's
// bare name) and core copies a class's text into every pool that uses it.
// What crosses is a condition's type, status and reason token, and the
// sentence below for that reason. The sentences are Klyx's own; they name
// no object.

// Unworded is the sentence for a reason the table does not know.
const Unworded = "see the condition in Klyx"

// sentences maps a reason token to its sentence. A reason is read the same
// whichever condition carries it: core copies class reasons into
// NodeClassReady, and NodeRegistrationHealthy carries a claim's launch
// reason.
var sentences = map[string]string{
	// core: NodePool
	"NodeClassNotFound":         "the NodeClass was not found",
	"NodeClassTerminating":      "the NodeClass is terminating",
	"NodeClassReadinessUnknown": "the NodeClass's readiness is unknown",
	"NodePoolValidationFailed":  "the NodePool failed validation",
	"UnhealthyDependents":       "a condition it depends on is not ready",
	"DependenciesNotReady":      "a condition it depends on is not ready",
	"RegistrationFailed":        "nodes launched for this pool failed to register",

	// core: NodeClaim
	"LaunchFailed":                 "the instance failed to launch",
	"InsufficientCapacity":         "no capacity was available for the requested instance types",
	"InsufficientCapacityError":    "no capacity was available for the requested instance types",
	"InstanceTypeResolutionFailed": "the instance types could not be resolved",
	"SubnetResolutionFailed":       "the subnets could not be resolved",
	"NodeNotFound":                 "no node has registered for the claim",
	"MultipleNodesFound":           "more than one node matched the claim",
	"RegistrationHookPending":      "a registration hook is pending",
	"NotInitialized":               "the node is not initialized",
	"NodeNotReady":                 "the node is not ready",
	"StartupTaintsExist":           "startup taints remain on the node",
	"KnownEphemeralTaintsExist":    "ephemeral taints remain on the node",
	"ResourceNotRegistered":        "a requested extended resource is not registered on the node",
	"DRADriverPoolsNotPublished":   "a DRA driver has not published its resource pool",
	"ConsistencyCheckFailed":       "the node failed a consistency check",

	// AWS: EC2NodeClass and instance launch
	"AMINotFound":                         "no AMI matched the selector terms, or it is not usable",
	"UnsupportedAlias":                    "the AMI alias is not supported",
	"InvalidAMIID":                        "the AMI ID is invalid",
	"AMIAuthorizationFailure":             "the controller is not authorised to use the AMI",
	"SubnetsNotFound":                     "no subnets matched the selector terms",
	"SecurityGroupsNotFound":              "no security groups matched the selector terms",
	"SecurityGroupSubnetVPCMismatch":      "the security groups and subnets are in different VPCs",
	"InstanceProfileNotFound":             "the instance profile was not found",
	"InstanceProfileNameInvalid":          "the instance profile in the NodeClass status does not exist",
	"PlacementGroupNotFound":              "the placement group was not found",
	"PlacementGroupResolutionFailed":      "the placement group could not be resolved",
	"CreateFleetAuthCheckFailed":          "the controller is not authorised to create EC2 fleets",
	"RunInstancesAuthCheckFailed":         "the controller is not authorised to run instances",
	"CreateLaunchTemplateAuthCheckFailed": "the controller is not authorised to create launch templates",
	"Unauthorized":                        "the controller is not authorised for the operation",
	"SpotSLRCreationFailed":               "the controller may not create the Spot service-linked role",
	"LaunchTemplateNotFound":              "the launch template was not found",
	"RequestLimitExceeded":                "the cloud API's request limit was exceeded",
	"TagValidationFailed":                 "a tag is invalid or uses a restricted key",
	"UserDataSizeLimitExceeded":           "the rendered user data exceeds the size limit",
	"InvalidKubeletConfiguration":         "the kubelet configuration is invalid",
	"UnsupportedKubeletConfiguration":     "the kubelet configuration is not supported",
	"KubeletExpressionInvalid":            "a kubelet expression is invalid",
	"KubeletExpressionEvaluationFailed":   "a kubelet expression could not be evaluated",
	"KubeletExpressionsDisabled":          "kubelet expressions are disabled",

	// EKS Auto Mode (community evidence, §1.4)
	"UnauthorizedNodeRole": "the node role has no EKS access entry",

	// Azure: AKSNodeClass and instance launch
	"SubnetIDInvalid":                           "the subnet ID is invalid",
	"SubnetNotFound":                            "the subnet was not found",
	"SubnetUnknownError":                        "the subnet could not be checked",
	"ImagesNotFound":                            "no node images were found",
	"ImagesNotReady":                            "the node images are not ready",
	"NodeImageVersionInvalid":                   "the node image version is invalid",
	"RequestedNodeImageVersionUnavailable":      "the requested node image version is unavailable",
	"SIGRequiredForFIPS":                        "FIPS node images need a Shared Image Gallery",
	"KubernetesVersionChanged":                  "the node images wait on a Kubernetes version change",
	"KubernetesUpgrade":                         "the node images wait on a Kubernetes upgrade",
	"KubernetesPinning":                         "the node images wait on a pinned Kubernetes version",
	"RollbackTargetKubernetesVersionMismatch":   "the rollback target does not match the Kubernetes version",
	"KubernetesVersionControlPlaneIncompatible": "the Kubernetes version is incompatible with the control plane",
	"KubernetesVersionInvalidFormat":            "the Kubernetes version is malformed",
	"KubernetesVersionUnsupported":              "the Kubernetes version is not supported",
	"CheckingClusterRequirementsFailed":         "LocalDNS's cluster requirements could not be checked",
	"ClusterCIDRResolutionFailed":               "the cluster CIDR could not be resolved",
	"AuthorizationFailed":                       "the controller is not authorised for the operation",
	"SubscriptionQuotaReached":                  "the subscription's quota was reached",
	"SKUNotAvailable":                           "the VM size is not available",
	"ZonalAllocationFailure":                    "Azure could not allocate the VM in the zone",
	"AllocationFailure":                         "Azure could not allocate the VM",
	"OverconstrainedZonalAllocationFailure":     "the request was too constrained to allocate in the zone",
	"OverconstrainedAllocationFailure":          "the request was too constrained to allocate",

	"CapacityReservationGroupNotFound":                   "the capacity reservation group was not found",
	"CapacityReservationGroupIDInvalid":                  "the capacity reservation group ID is invalid",
	"CapacityReservationGroupAccessDenied":               "access to the capacity reservation group was denied",
	"CapacityReservationGroupRegionMismatch":             "the capacity reservation group is in another region",
	"CapacityReservationGroupSubscriptionMismatch":       "the capacity reservation group is in another subscription",
	"CapacityReservationGroupNoReservations":             "the capacity reservation group holds no reservations",
	"CapacityReservationGroupNoEligibleReservations":     "the capacity reservation group holds no eligible reservations",
	"CapacityReservationGroupNoCompatibleReservations":   "the capacity reservation group holds no compatible reservations",
	"CapacityReservationGroupUnsupportedCloud":           "capacity reservation groups are not supported in this cloud",
	"CapacityReservationGroupUnsupportedReservationType": "the capacity reservation type is not supported",
	"CapacityReservationGroupUnknownError":               "the capacity reservation group could not be checked",
}

// Sentence is the fixed sentence for a reason token; Unworded for an empty,
// withheld or unknown one.
func Sentence(reason string) string {
	if s, ok := sentences[reason]; ok {
		return s
	}
	return Unworded
}

// Phrase is a condition in Klyx's fixed wording: "SubnetsReady False:
// SubnetsNotFound: no subnets matched the selector terms". It never reads
// the message. A withheld reason says so; an empty one is omitted.
func Phrase(c *Cond) string {
	if c == nil {
		return ""
	}
	s := typeStatus(c)
	switch {
	case c.ReasonWithheld:
		s += ": reason withheld: " + Unworded
	case c.Reason != "":
		s += ": " + c.Reason + ": " + Sentence(c.Reason)
	default:
		s += ": " + Unworded
	}
	return s
}

// Native is a condition in the provider's own words, for the local
// inspector only: no AI or chat path reads it (§8).
func Native(c *Cond) string {
	if c == nil {
		return ""
	}
	s := typeStatus(c)
	if c.Reason != "" {
		s += ": " + c.Reason
	}
	if c.Message != "" {
		s += ": " + c.Message
	}
	return s
}

func typeStatus(c *Cond) string {
	if c.Status == "" {
		return c.Type
	}
	return c.Type + " " + c.Status
}
